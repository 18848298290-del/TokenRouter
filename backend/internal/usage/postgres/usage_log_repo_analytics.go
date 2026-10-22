package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
	"golang.org/x/sync/errgroup"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// usage_log_repo_analytics.go splits report ranges across raw usage and covered
// analytics buckets and combines dashboard and batch-cost results.
// A report can start before continuous analytics coverage and end after the live
// watermark. The query window keeps those unaggregated edges as raw records
// while the covered middle uses UTC hourly buckets and a date-based daily span
// derived from the rounded bucket bounds.
//
// The final live hour may contain data only through the watermark, so its bucket
// endpoint differs from the raw tail's starting instant. Rounding during UTC's
// final hour can include the current date in the daily span, while live daily
// rebuilding handles closed dates. Daily date parameters are kept separate from
// hourly timestamps to preserve PostgreSQL's inferred types. User dashboards
// and batch cost reports then combine each source's metrics while retaining
// actor/team ownership, independently chosen today ranges, and raw recent-rate
// reads. Missing usable coverage asks callers to choose a complete raw query.

// usageAnalyticsWindow describes report instants, covered bucket bounds, and raw
// edges.
type usageAnalyticsWindow struct {
	// start and end bound the requested half-open UTC range. aggregateStart and
	// aggregateEnd select the covered bucket interval that supplies the report's
	// middle.
	start          time.Time // requested UTC range start
	end            time.Time // requested UTC range end
	aggregateStart time.Time // covered bucket range start
	aggregateEnd   time.Time // covered bucket range end
	// dailyStart and dailyEnd delimit date-based selection inside rounded aggregate
	// bounds. Their shape can cover the current date when its partial live hour
	// rounds into tomorrow; dailySplit retains hourly selection for an empty span.
	dailyStart time.Time // daily SQL span start
	dailyEnd   time.Time // daily SQL span end
	// rawTailStart resumes raw reads at the watermark when the final live bucket
	// is partial, and at aggregateEnd for a complete historical interval.
	rawTailStart time.Time // raw records resume instant
}

// usageAnalyticsDailySplit separates hourly timestamp parameters from daily date
// strings.
type usageAnalyticsDailySplit struct {
	// hourlyHeadEnd and hourlyTailStart limit the hourly portions around the daily
	// range. dailyStartDate and dailyEndDate use distinct SQL date arguments so
	// timestamp bounds cannot inherit date coercion from the daily query.
	hourlyHeadEnd   time.Time // first hourly portion end
	hourlyTailStart time.Time // second hourly portion start
	dailyStartDate  string    // YYYY-MM-DD
	dailyEndDate    string    // YYYY-MM-DD
}

// dailySplit returns separate timestamp and date bounds for daily-enabled SQL
// queries. The hourly head/tail timestamps surround the computed daily span,
// while daily values become date-only strings for SQL date predicates.
//
// When the computed date span is empty, both hourly split points use
// aggregateStart and both date strings use dailyEnd. The empty daily range leaves
// the full aggregate span for hourly selection. Keeping representations separate
// prevents date casts from coercing timestamp parameters and including earlier
// hours in a local-today query.
func (window usageAnalyticsWindow) dailySplit() usageAnalyticsDailySplit {
	hourlyHeadEnd := window.dailyStart
	hourlyTailStart := window.dailyEnd
	dailyStart := window.dailyStart
	dailyEnd := window.dailyEnd
	if !dailyEnd.After(dailyStart) {
		// An empty computed date span uses aggregateStart for both hourly split points,
		// keeping the hourly source inside the requested covered middle.
		hourlyHeadEnd = window.aggregateStart
		hourlyTailStart = window.aggregateStart
		dailyStart = window.dailyEnd
		dailyEnd = window.dailyEnd
	}
	return usageAnalyticsDailySplit{
		hourlyHeadEnd:   hourlyHeadEnd,
		hourlyTailStart: hourlyTailStart,
		dailyStartDate:  dailyStart.Format(time.DateOnly),
		dailyEndDate:    dailyEnd.Format(time.DateOnly),
	}
}

// args returns the split's hourly timestamps followed by daily date strings.
func (split usageAnalyticsDailySplit) args() []any {
	return []any{split.hourlyHeadEnd, split.hourlyTailStart, split.dailyStartDate, split.dailyEndDate}
}

// resolveUsageAnalyticsWindow derives a covered middle and raw edges for [start,
// end). Disabled settings, invalid ranges, absent coverage, or unusable
// watermarks return false so callers can choose raw records; state-query
// failures return an error.
//
// The aggregate start rounds the requested start to an hour and advances to
// persisted coverage when needed. Ranges after the live watermark retain that
// partial live-hour bucket but start raw tail reads at the watermark. Historical
// ends use complete hours and a raw partial final hour. Daily bounds derive
// from rounded aggregateEnd; in UTC's final hour they can include the current
// date, whose daily rows live aggregation has not rebuilt yet. This resolver
// checks persisted window state, leaving actual daily-row availability unchecked.
func (r *Store) resolveUsageAnalyticsWindow(ctx context.Context, start, end time.Time) (usageAnalyticsWindow, bool, error) {
	window := usageAnalyticsWindow{start: start.UTC(), end: end.UTC()}
	if r == nil || r.preAggregation == nil || !end.After(start) || !r.preAggregation.UsageEnabled(ctx) {
		return window, false, nil
	}
	var state struct {
		watermark time.Time    // persisted live processed instant
		coverage  sql.NullTime // optional continuous history start
	}
	if err := scanSingleRow(ctx, r.sql, `
		SELECT live_watermark, coverage_start
		FROM usage_analytics_aggregation_state
		WHERE id = 1
	`, nil, &state.watermark, &state.coverage); err != nil {
		return window, false, err
	}
	if !state.coverage.Valid || !state.watermark.After(time.Unix(0, 0)) {
		return window, false, nil
	}

	window.aggregateStart = ceilHourUTC(window.start)
	if coverageStart := state.coverage.Time.UTC(); coverageStart.After(window.aggregateStart) {
		// Persisted coverage starts after the requested first complete hour; advance
		// the aggregate head and let raw records answer the unbackfilled interval.
		window.aggregateStart = coverageStart
	}
	if end.UTC().After(state.watermark.UTC()) {
		// A partial live-hour bucket was scanned only through the watermark. Keep its
		// bucket in analytics and read later instants from raw rows exactly once.
		window.aggregateEnd = ceilHourUTC(state.watermark.UTC())
		window.rawTailStart = state.watermark.UTC()
	} else {
		// A historical end can split a full stored bucket; stop aggregate selection at
		// that hour boundary and let raw rows provide the requested partial hour.
		window.aggregateEnd = end.UTC().Truncate(time.Hour)
		window.rawTailStart = window.aggregateEnd
	}
	if window.rawTailStart.Before(window.aggregateStart) {
		window.rawTailStart = window.aggregateStart
	}
	if !window.aggregateEnd.After(window.aggregateStart) {
		return window, false, nil
	}
	if state.watermark.UTC().Before(window.aggregateStart) {
		return window, false, nil
	}
	window.dailyStart = ceilDayUTC(window.aggregateStart)
	window.dailyEnd = window.aggregateEnd.Truncate(24 * time.Hour)
	// The daily span uses rounded aggregateEnd. An empty span keeps hourly
	// selection, but these bounds do not establish that the selected daily rows
	// exist.
	return window, true, nil
}

// ceilHourUTC returns the first UTC hour boundary at or after value. Exact
// boundaries stay unchanged; other instants advance from their truncated UTC
// hour by one elapsed hour.
func ceilHourUTC(value time.Time) time.Time {
	value = value.UTC()
	// UTC hours use fixed elapsed-hour boundaries for bucket selection.
	floor := value.Truncate(time.Hour)
	if value.Equal(floor) {
		return floor
	}
	return floor.Add(time.Hour)
}

// ceilDayUTC returns the first UTC midnight at or after value. Exact boundaries
// stay unchanged, while other instants advance from their UTC day floor by
// twenty-four elapsed hours.
func ceilDayUTC(value time.Time) time.Time {
	value = value.UTC()
	// UTC days use fixed twenty-four-hour boundaries for bucket selection.
	floor := value.Truncate(24 * time.Hour)
	if value.Equal(floor) {
		return floor
	}
	return floor.Add(24 * time.Hour)
}

// getOwnedTeamID returns the team ID from a current owner membership. No
// matching row produces zero through the nullable scalar query; database errors
// propagate, and membership authorization remains the caller's responsibility.
func (r *Store) getOwnedTeamID(ctx context.Context, userID int64) (int64, error) {
	var teamID sql.NullInt64
	err := scanSingleRow(ctx, r.sql, `
		SELECT (
			SELECT tm.team_id
			FROM team_memberships tm
			WHERE tm.user_id = $1 AND tm.role = 'owner' AND tm.left_at IS NULL
		) AS team_id
	`, []any{userID}, &teamID)
	if err != nil || !teamID.Valid {
		return 0, err
	}
	return teamID.Int64, nil
}

// getUserDashboardStatsFromAnalytics returns actor and owned-team dashboard
// metrics when analytics coverage is usable. Missing settings/source bounds or
// unusable coverage return false; query failures let the public caller retry its
// complete raw dashboard.
//
// Retained totals combine daily/hourly buckets with raw edges, while today's
// metrics use Calendar midnight through a separately sampled current time. Key
// counts, totals, today, and recent rates execute through an errgroup, and any
// query failure discards the combined response. Duration averages divide by
// nonnull duration count; raw five-minute rates keep their own denominator and
// token definition.
func (r *Store) getUserDashboardStatsFromAnalytics(ctx context.Context, userID int64) (*usage.UserDashboardStats, bool, error) {
	if r == nil || r.preAggregation == nil || !r.preAggregation.UsageEnabled(ctx) {
		return nil, false, nil
	}
	var sourceOldest sql.NullTime
	if err := scanSingleRow(ctx, r.sql, `
		SELECT source_oldest_at FROM usage_analytics_aggregation_state WHERE id = 1
	`, nil, &sourceOldest); err != nil {
		return nil, false, err
	}
	if !sourceOldest.Valid {
		return nil, false, nil
	}
	now := time.Now().UTC()
	window, ok, err := r.resolveUsageAnalyticsWindow(ctx, sourceOldest.Time, now)
	if err != nil || !ok {
		return nil, false, err
	}
	teamID, err := r.getOwnedTeamID(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	dailySplit := window.dailySplit()

	stats := &usage.UserDashboardStats{}
	var totalDuration, durationCount int64
	var todayStats *UsageStats
	var rpm, tpm int64
	group, groupCtx := errgroup.WithContext(ctx)

	// These queries write disjoint result fields. Wait for their errgroup before
	// combining totals so a failed member cannot publish a partial dashboard.
	group.Go(func() error {
		return scanSingleRow(groupCtx, r.sql, `
			WITH scoped AS (
				SELECT status FROM api_keys WHERE deleted_at IS NULL AND user_id = $1
				UNION ALL
				SELECT status FROM api_keys
				WHERE deleted_at IS NULL AND $2 > 0 AND team_id = $2 AND user_id <> $1
			)
			SELECT COUNT(*), COUNT(*) FILTER (WHERE status = $3) FROM scoped
		`, []any{userID, teamID, identity.StatusActive}, &stats.TotalAPIKeys, &stats.ActiveAPIKeys)
	})
	group.Go(func() error {
		return scanSingleRow(groupCtx, r.sql, `
		WITH combined AS (
			SELECT
				total_requests, input_tokens, output_tokens,
				cache_creation_tokens, cache_read_tokens,
				total_cost, actual_cost, total_duration_ms, duration_count
			FROM usage_analytics_daily
			WHERE bucket_date >= $10::date AND bucket_date < $11::date
			  AND user_id = $1
			UNION ALL
			SELECT
				total_requests, input_tokens, output_tokens,
				cache_creation_tokens, cache_read_tokens,
				total_cost, actual_cost, total_duration_ms, duration_count
			FROM usage_analytics_daily
			WHERE bucket_date >= $10::date AND bucket_date < $11::date
			  AND $2 > 0 AND team_id = $2 AND user_id <> $1
			UNION ALL
			SELECT
				total_requests, input_tokens, output_tokens,
				cache_creation_tokens, cache_read_tokens,
				total_cost, actual_cost, total_duration_ms, duration_count
			FROM usage_analytics_hourly
			WHERE ((bucket_start >= $5 AND bucket_start < $8)
			    OR (bucket_start >= $9 AND bucket_start < $6))
			  AND user_id = $1
			UNION ALL
			SELECT
				total_requests, input_tokens, output_tokens,
				cache_creation_tokens, cache_read_tokens,
				total_cost, actual_cost, total_duration_ms, duration_count
			FROM usage_analytics_hourly
			WHERE ((bucket_start >= $5 AND bucket_start < $8)
			    OR (bucket_start >= $9 AND bucket_start < $6))
			  AND $2 > 0 AND team_id = $2 AND user_id <> $1
			UNION ALL
			SELECT
				1, input_tokens, output_tokens,
				cache_creation_tokens, cache_read_tokens,
				total_cost, actual_cost, COALESCE(duration_ms, 0),
				CASE WHEN duration_ms IS NULL THEN 0 ELSE 1 END
			FROM usage_logs
			WHERE user_id = $1
			  AND ((created_at >= $3 AND created_at < $5)
			    OR (created_at >= $7 AND created_at < $4))
			UNION ALL
			SELECT
				1, input_tokens, output_tokens,
				cache_creation_tokens, cache_read_tokens,
				total_cost, actual_cost, COALESCE(duration_ms, 0),
				CASE WHEN duration_ms IS NULL THEN 0 ELSE 1 END
			FROM usage_logs
			WHERE $2 > 0 AND team_id = $2 AND user_id <> $1
			  AND ((created_at >= $3 AND created_at < $5)
			    OR (created_at >= $7 AND created_at < $4))
		)
		SELECT
			COALESCE(SUM(total_requests), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(total_cost), 0),
			COALESCE(SUM(actual_cost), 0),
			COALESCE(SUM(total_duration_ms), 0),
			COALESCE(SUM(duration_count), 0)
		FROM combined
		`, append([]any{
			userID, teamID, window.start, window.end, window.aggregateStart,
			window.aggregateEnd, window.rawTailStart,
		}, dailySplit.args()...),
			&stats.TotalRequests, &stats.TotalInputTokens, &stats.TotalOutputTokens,
			&stats.TotalCacheCreationTokens, &stats.TotalCacheReadTokens,
			&stats.TotalCost, &stats.TotalActualCost, &totalDuration, &durationCount,
		)
	})
	group.Go(func() error {
		// Calendar midnight can split a UTC daily bucket. Build a separate bounded
		// today query so the full retained range does not determine today's edges.
		todayStart := r.calendar.Today()
		todayEnd := time.Now().UTC()
		todayFilters := UsageLogFilters{
			UserID: userID, IncludeOwnedTeam: true,
			StartTime: &todayStart, EndTime: &todayEnd,
		}
		var todayOK bool
		var todayErr error
		todayStats, todayOK, todayErr = r.getUsageStatsFromAnalytics(groupCtx, todayFilters)
		if todayErr != nil {
			return todayErr
		}
		if !todayOK {
			return errors.New("today's usage is outside pre-aggregation coverage")
		}
		return nil
	})
	group.Go(func() error {
		var performanceErr error
		rpm, tpm, performanceErr = r.getPerformanceStatsForScope(groupCtx, userID, teamID)
		return performanceErr
	})
	if err := group.Wait(); err != nil {
		return nil, false, err
	}
	stats.TodayRequests = todayStats.TotalRequests
	stats.TodayInputTokens = todayStats.TotalInputTokens
	stats.TodayOutputTokens = todayStats.TotalOutputTokens
	stats.TodayCacheCreationTokens = todayStats.TotalCacheCreationTokens
	stats.TodayCacheReadTokens = todayStats.TotalCacheReadTokens
	stats.TodayCost = todayStats.TotalCost
	stats.TodayActualCost = todayStats.TotalActualCost
	if durationCount > 0 {
		stats.AverageDurationMs = float64(totalDuration) / float64(durationCount)
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens
	stats.Rpm = rpm
	stats.Tpm = tpm
	return stats, true, nil
}

// getPerformanceStatsForScope returns integer five-minute average RPM and text
// TPM for the actor and supplied team. A positive teamID adds other team actors
// to the actor's own retained usage; a nonpositive value selects that actor alone.
//
// Excluding userID from the team branch keeps the actor's team-key requests from
// entering both counters twice. Both branches use one lower time bound, so
// future-dated records participate. Input/output tokens determine text TPM,
// cache tokens are excluded, and integer division discards fractional
// per-minute counts before the dashboard receives them.
func (r *Store) getPerformanceStatsForScope(ctx context.Context, userID, teamID int64) (int64, int64, error) {
	var requests, tokens int64
	err := scanSingleRow(ctx, r.sql, `
		WITH scoped AS (
			SELECT input_tokens, output_tokens FROM usage_logs
			WHERE user_id = $1 AND created_at >= $3
			UNION ALL
			SELECT input_tokens, output_tokens FROM usage_logs
			WHERE $2 > 0 AND team_id = $2 AND user_id <> $1 AND created_at >= $3
		)
		SELECT COUNT(*), COALESCE(SUM(input_tokens + output_tokens), 0) FROM scoped
	`, []any{userID, teamID, time.Now().Add(-5 * time.Minute)}, &requests, &tokens)
	// Convert five-minute counts to integer per-minute averages, truncating
	// fractions.
	return requests / 5, tokens / 5, err
}

// getBatchAPIKeyUsageStatsFromAnalytics returns bounded-range and today charges
// for the supplied key IDs when both sources are usable. Requested keys start
// with zero-valued entries, and any query or coverage failure discards the
// partial batch result.
//
// The first scan combines daily/hourly buckets and raw edges over start/end. A
// separate hourly-enabled combined query covers Calendar midnight through the
// current instant and appends the key array after that query's time arguments.
// Keeping each parameter position with its query prevents the array from
// occupying a date/timestamp slot when daily bounds are absent.
func (r *Store) getBatchAPIKeyUsageStatsFromAnalytics(ctx context.Context, apiKeyIDs []int64, start, end time.Time) (map[int64]*usage.BatchAPIKeyUsageStats, bool, error) {
	window, ok, err := r.resolveUsageAnalyticsWindow(ctx, start, end)
	if err != nil || !ok {
		return nil, false, err
	}
	result := make(map[int64]*usage.BatchAPIKeyUsageStats, len(apiKeyIDs))
	for _, id := range apiKeyIDs {
		result[id] = &usage.BatchAPIKeyUsageStats{APIKeyID: id}
	}
	dailySplit := window.dailySplit()
	rows, err := r.sql.QueryContext(ctx, `
		WITH combined AS (
			SELECT api_key_id, actual_cost
			FROM usage_analytics_daily
			WHERE api_key_id = ANY($1) AND bucket_date >= $9::date AND bucket_date < $10::date
			UNION ALL
			SELECT api_key_id, actual_cost
			FROM usage_analytics_hourly
			WHERE api_key_id = ANY($1)
			  AND ((bucket_start >= $4 AND bucket_start < $7)
			    OR (bucket_start >= $8 AND bucket_start < $5))
			UNION ALL
			SELECT api_key_id, actual_cost
			FROM usage_logs
			WHERE api_key_id = ANY($1)
			  AND ((created_at >= $2 AND created_at < $4)
			    OR (created_at >= $6 AND created_at < $3))
		)
		SELECT api_key_id, COALESCE(SUM(actual_cost), 0)
		FROM combined
		GROUP BY api_key_id
	`, append([]any{
		pq.Array(apiKeyIDs), window.start, window.end, window.aggregateStart,
		window.aggregateEnd, window.rawTailStart,
	}, dailySplit.args()...)...)
	if err != nil {
		return nil, false, err
	}
	// Release range rows after scanning or an early return. This helper ignores
	// close errors while returning query, scan, and iteration failures to its
	// caller.
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var total float64
		if err := rows.Scan(&id, &total); err != nil {
			return nil, false, err
		}
		if stats := result[id]; stats != nil {
			stats.TotalActualCost = total
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	todayStart := r.calendar.Today()
	todayEnd := time.Now().UTC()
	todayQuery, todayOK, todayErr := r.buildUsageAnalyticsQuery(ctx, UsageLogFilters{}, todayStart, todayEnd, false)
	if todayErr != nil || !todayOK {
		return nil, false, todayErr
	}
	todayQuery.args = append(todayQuery.args, pq.Array(apiKeyIDs))
	apiKeyIDsPosition := len(todayQuery.args)
	todayRows, err := r.sql.QueryContext(ctx, todayQuery.cte+fmt.Sprintf(`
		SELECT api_key_id, COALESCE(SUM(actual_cost), 0)
		FROM combined
		WHERE api_key_id = ANY($%d)
		GROUP BY api_key_id
	`, apiKeyIDsPosition), todayQuery.args...)
	if err != nil {
		return nil, false, err
	}
	// Release the independent today result even when merging fails; close failures
	// are ignored here, while scan and iteration errors discard the partial batch.
	defer func() { _ = todayRows.Close() }()
	for todayRows.Next() {
		var id int64
		var today float64
		if err := todayRows.Scan(&id, &today); err != nil {
			return nil, false, err
		}
		if stats := result[id]; stats != nil {
			stats.TodayActualCost = today
		}
	}
	if err := todayRows.Err(); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

// getBatchUserUsageStatsFromAnalytics returns actor charges and platform rows
// from covered analytics plus raw edges. Requested users begin with zero totals;
// unusable coverage or any query failure makes the public caller choose a
// complete raw batch.
//
// Range totals group by user and stored platform, with positive-cost filtering
// on raw edges. Today's separate combined query filters positive actual_cost
// across its source and uses Calendar midnight through the current instant.
// Those today rows merge into matching platform entries or add a new platform,
// while empty platform labels still contribute to user totals.
func (r *Store) getBatchUserUsageStatsFromAnalytics(ctx context.Context, userIDs []int64, start, end time.Time) (map[int64]*usage.BatchUserUsageStats, bool, error) {
	window, ok, err := r.resolveUsageAnalyticsWindow(ctx, start, end)
	if err != nil || !ok {
		return nil, false, err
	}
	result := make(map[int64]*usage.BatchUserUsageStats, len(userIDs))
	for _, id := range userIDs {
		result[id] = &usage.BatchUserUsageStats{UserID: id}
	}
	dailySplit := window.dailySplit()
	rows, err := r.sql.QueryContext(ctx, `
		WITH combined AS (
			SELECT user_id, platform, actual_cost
			FROM usage_analytics_daily
			WHERE user_id = ANY($1) AND bucket_date >= $9::date AND bucket_date < $10::date
			UNION ALL
			SELECT user_id, platform, actual_cost
			FROM usage_analytics_hourly
			WHERE user_id = ANY($1)
			  AND ((bucket_start >= $4 AND bucket_start < $7)
			    OR (bucket_start >= $8 AND bucket_start < $5))
			UNION ALL
			SELECT ul.user_id, ul.platform, ul.actual_cost
			FROM usage_logs ul
			LEFT JOIN groups g ON g.id = ul.group_id
			LEFT JOIN providers a ON a.id = ul.provider_id
			WHERE ul.user_id = ANY($1) AND ul.actual_cost > 0
			  AND ((ul.created_at >= $2 AND ul.created_at < $4)
			    OR (ul.created_at >= $6 AND ul.created_at < $3))
		)
		SELECT user_id, platform, COALESCE(SUM(actual_cost), 0)
		FROM combined
		GROUP BY user_id, platform
	`, append([]any{
		pq.Array(userIDs), window.start, window.end, window.aggregateStart,
		window.aggregateEnd, window.rawTailStart,
	}, dailySplit.args()...)...)
	if err != nil {
		return nil, false, err
	}
	// Release range rows after scanning or an early return. This helper ignores
	// close errors while returning query, scan, and iteration failures to its
	// caller.
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var platform string
		var total float64
		if err := rows.Scan(&id, &platform, &total); err != nil {
			return nil, false, err
		}
		stats := result[id]
		if stats == nil {
			continue
		}
		stats.TotalActualCost += total
		if platform != "" && total != 0 {
			stats.ByPlatform = append(stats.ByPlatform, usage.PlatformUsage{
				Platform: platform, TotalActualCost: total,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	todayStart := r.calendar.Today()
	todayEnd := time.Now().UTC()
	todayQuery, todayOK, todayErr := r.buildUsageAnalyticsQuery(ctx, UsageLogFilters{}, todayStart, todayEnd, false)
	if todayErr != nil || !todayOK {
		return nil, false, todayErr
	}
	todayQuery.args = append(todayQuery.args, pq.Array(userIDs))
	userIDsPosition := len(todayQuery.args)
	todayRows, err := r.sql.QueryContext(ctx, todayQuery.cte+fmt.Sprintf(`
		SELECT user_id, platform, COALESCE(SUM(actual_cost), 0)
		FROM combined
		WHERE user_id = ANY($%d) AND actual_cost > 0
		GROUP BY user_id, platform
	`, userIDsPosition), todayQuery.args...)
	if err != nil {
		return nil, false, err
	}
	// Release the independent today result even when merging fails; close failures
	// are ignored here, while scan and iteration errors discard the partial batch.
	defer func() { _ = todayRows.Close() }()
	for todayRows.Next() {
		var id int64
		var platform string
		var today float64
		if err := todayRows.Scan(&id, &platform, &today); err != nil {
			return nil, false, err
		}
		stats := result[id]
		if stats == nil {
			continue
		}
		stats.TodayActualCost += today
		updated := false
		for index := range stats.ByPlatform {
			if stats.ByPlatform[index].Platform == platform {
				stats.ByPlatform[index].TodayActualCost = today
				updated = true
				break
			}
		}
		if !updated && platform != "" {
			stats.ByPlatform = append(stats.ByPlatform, usage.PlatformUsage{Platform: platform, TodayActualCost: today})
		}
	}
	if err := todayRows.Err(); err != nil {
		return nil, false, err
	}
	return result, true, nil
}
