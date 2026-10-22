package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// usage_log_repo_dashboard.go calculates administrator, user, and API-key
// dashboard counters and recent request rates.
// Dashboard counters combine current entity state with retained usage and recent
// request rates. User-visible scope includes the acting user's records and
// current owned-team usage, excluding owner records from the team branch so one
// request contributes once.
//
// Pool-backed stores run independent queries concurrently; single-connection
// report executors use the shared serial runner where called. Legacy
// administrator dashboards read daily/hourly dashboard buckets, while user and
// key dashboards can use multidimensional analytics with raw recovery. Duration
// denominators differ: administrator totals include every request, and user/key
// dashboards count observed durations. Calendar supplies local midnight, but raw
// administrator today spans use twenty-four elapsed hours and recent rates use
// separate system-clock windows.

// UserStats contains raw actor request, token, and actual-charge totals.
type UserStats struct {
	TotalRequests   int64   `json:"total_requests"`    // matching retained request count
	TotalTokens     int64   `json:"total_tokens"`      // input output and cache tokens
	TotalCost       float64 `json:"total_cost"`        // actual user charges in USD
	InputTokens     int64   `json:"input_tokens"`      // recorded input token count
	OutputTokens    int64   `json:"output_tokens"`     // recorded output token count
	CacheReadTokens int64   `json:"cache_read_tokens"` // recorded cache-read token count
}

// DashboardStats combines administrator entity, usage, activity, and recent-rate
// metrics.
type DashboardStats = usage.DashboardStats

// UserDashboardStats contains user or selected-key totals and current activity
// metrics.
type UserDashboardStats = usage.UserDashboardStats

// getPerformanceStats returns integer five-minute RPM and text TPM for the
// selected actor/owned-team scope. Nonpositive user IDs select global records;
// cache tokens are excluded, and the lower-only time filter includes
// future-dated usage.
func (r *Store) getPerformanceStats(ctx context.Context, userID int64, includeOwnedTeam bool) (rpm, tpm int64, err error) {
	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
	args := []any{fiveMinutesAgo}
	source, scopeCondition, args, err := r.buildUsageLogScopeSource(ctx, args, userID, includeOwnedTeam, "")
	if err != nil {
		return 0, 0, err
	}
	query := `
		SELECT
			COUNT(*) as request_count,
			COALESCE(SUM(input_tokens + output_tokens), 0) as token_count
		FROM ` + source + `
		WHERE created_at >= $1`
	if scopeCondition != "" {
		query += " AND " + scopeCondition
	}

	var requestCount int64
	var tokenCount int64
	if err := scanSingleRow(ctx, r.sql, query, args, &requestCount, &tokenCount); err != nil {
		return 0, 0, err
	}
	// Convert five-minute counts to integer per-minute averages, truncating
	// fractions.
	return requestCount / 5, tokenCount / 5, nil
}

// GetUserStats returns raw actor totals for the supplied half-open range.
// TotalCost contains actual user charges, while TotalTokens includes
// creation/read-cache tokens; the caller authorizes the actor before exposing
// its summary.
func (r *Store) GetUserStats(ctx context.Context, userID int64, startTime, endTime time.Time) (*UserStats, error) {
	query := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(actual_cost), 0) as total_cost,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
	`

	stats := &UserStats{}
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{userID, startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalTokens,
		&stats.TotalCost,
		&stats.InputTokens,
		&stats.OutputTokens,
		&stats.CacheReadTokens,
	); err != nil {
		return nil, err
	}
	return stats, nil
}

// runDashboardQueries executes independent callbacks using the store's
// connection capability. A store without a concrete pool runs them serially and
// returns the first error, allowing transaction-backed callbacks to share one
// connection.
//
// Pool-backed callbacks run through an errgroup whose context cancels siblings
// after a failure. Callbacks own their disjoint output fields and complete
// before the group result returns. The method joins the group before callers
// publish aggregate response fields, so a failed member can make the caller
// discard all partially populated dashboard data.
func (r *Store) runDashboardQueries(ctx context.Context, queries ...func(context.Context) error) error {
	// Single-connection executors reuse their selected transaction connection;
	// finish each callback before the next begins so its result rows can be
	// released.
	if r.db == nil {
		for _, query := range queries {
			if err := query(ctx); err != nil {
				return err
			}
		}
		return nil
	}

	// Independent pool queries can use different connections. Join their results
	// before publishing the dashboard, and let an error cancel remaining callbacks.
	group, groupCtx := errgroup.WithContext(ctx)
	for _, query := range queries {
		group.Go(func() error { return query(groupCtx) })
	}
	return group.Wait()
}

// GetDashboardStats returns current entity counts, retained dashboard totals,
// and raw recent rates. A bound service disabling usage aggregation selects the
// raw range method from epoch through the current system time.
//
// Other calls combine legacy daily dashboard totals with current-hour activity,
// using Calendar's now and today values for entity/time conditions. Independent
// count, usage, and rate queries run through the store's executor-aware runner
// and failures discard the response. The method reads dashboard buckets
// directly, so missing bucket rows and aggregation availability follow that
// reader's rules rather than multidimensional coverage checks.
func (r *Store) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	if r.preAggregation != nil && !r.preAggregation.UsageEnabled(ctx) {
		// Disabled aggregation bypasses legacy buckets and computes the full dashboard
		// from raw usage over the retained epoch-to-now analytical window.
		return r.GetDashboardStatsWithRange(ctx, time.Unix(0, 0), time.Now())
	}
	stats := &DashboardStats{}
	now := r.calendar.Now()
	todayStart := r.calendar.Today()

	// Counts, usage, and rates fill separate fields; join all required reads before
	// assigning final rate values or returning a partially populated dashboard.
	var rpm, tpm int64
	err := r.runDashboardQueries(
		ctx,
		func(queryCtx context.Context) error {
			return r.fillDashboardEntityStats(queryCtx, stats, todayStart, now)
		},
		func(queryCtx context.Context) error {
			return r.fillDashboardUsageStatsAggregated(queryCtx, stats, todayStart, now)
		},
		func(queryCtx context.Context) error {
			var err error
			rpm, tpm, err = r.getPerformanceStats(queryCtx, 0, false)
			return err
		},
	)
	if err != nil {
		return nil, err
	}
	stats.Rpm = rpm
	stats.Tpm = tpm

	return stats, nil
}

// GetDashboardStatsWithRange returns current entity counts and raw totals for a
// valid half-open range. It converts bounds to UTC and rejects end at or before
// start before querying.
//
// Today and current-hour activity are separate windows based on Calendar's
// date/current instant, while rates use their own recent system-clock range. The
// raw total and today sums share a union scan, and entity counts remain current
// rather than range-filtered historical values. Every required query failure
// aborts the response, allowing the caller to distinguish an unavailable
// dashboard from zero usage.
func (r *Store) GetDashboardStatsWithRange(ctx context.Context, start, end time.Time) (*DashboardStats, error) {
	startUTC := start.UTC()
	endUTC := end.UTC()
	if !endUTC.After(startUTC) {
		return nil, errors.New("invalid statistics time range")
	}

	stats := &DashboardStats{}
	now := r.calendar.Now()
	todayStart := r.calendar.Today()

	if err := r.fillDashboardEntityStats(ctx, stats, todayStart, now); err != nil {
		return nil, err
	}
	if err := r.fillDashboardUsageStatsFromUsageLogs(ctx, stats, startUTC, endUTC, todayStart, now); err != nil {
		return nil, err
	}

	rpm, tpm, err := r.getPerformanceStats(ctx, 0, false)
	if err != nil {
		return nil, err
	}
	stats.Rpm = rpm
	stats.Tpm = tpm

	return stats, nil
}

// fillDashboardEntityStats fills current nondeleted user, key, and provider
// counters in stats. Calendar midnight identifies today's new users, and now
// tests active rate-limit/overload deadlines; provider categories can overlap
// and callbacks write separate fields.
func (r *Store) fillDashboardEntityStats(ctx context.Context, stats *DashboardStats, todayUTC, now time.Time) error {
	userStatsQuery := `
		SELECT
			COUNT(*) as total_users,
			COUNT(CASE WHEN created_at >= $1 THEN 1 END) as today_new_users
		FROM users
		WHERE deleted_at IS NULL
	`
	apiKeyStatsQuery := `
		SELECT
			COUNT(*) as total_api_keys,
			COUNT(CASE WHEN status = $1 THEN 1 END) as active_api_keys
		FROM api_keys
		WHERE deleted_at IS NULL
	`
	providerStatsQuery := `
		SELECT
			COUNT(*) as total_providers,
			COUNT(CASE WHEN status = $1 AND schedulable = true THEN 1 END) as normal_providers,
			COUNT(CASE WHEN status = $2 THEN 1 END) as error_providers,
			COUNT(CASE WHEN rate_limited_at IS NOT NULL AND rate_limit_reset_at > $3 THEN 1 END) as ratelimit_providers,
			COUNT(CASE WHEN overload_until IS NOT NULL AND overload_until > $4 THEN 1 END) as overload_providers
		FROM providers
		WHERE deleted_at IS NULL
	`
	return r.runDashboardQueries(
		ctx,
		func(queryCtx context.Context) error {
			return scanSingleRow(queryCtx, r.sql, userStatsQuery, []any{todayUTC}, &stats.TotalUsers, &stats.TodayNewUsers)
		},
		func(queryCtx context.Context) error {
			return scanSingleRow(queryCtx, r.sql, apiKeyStatsQuery, []any{identity.StatusActive}, &stats.TotalAPIKeys, &stats.ActiveAPIKeys)
		},
		func(queryCtx context.Context) error {
			return scanSingleRow(
				queryCtx, r.sql, providerStatsQuery,
				[]any{identity.StatusActive, provider.StatusError, now, now},
				&stats.TotalProviders, &stats.NormalProviders, &stats.ErrorProviders,
				&stats.RateLimitProviders, &stats.OverloadProviders,
			)
		},
	)
}

// fillDashboardUsageStatsAggregated fills administrator usage from retained
// daily buckets and current-hour activity. Missing current-hour rows leave zero
// activity, while other SQL errors reject the result.
//
// Total and today metrics come from distinct sums of the same daily source.
// TotalTokens and TodayTokens combine input/output plus cache creation/read, and
// AverageDurationMs divides stored duration sum by all requests, including rows
// whose duration was absent. The hourly key is an elapsed-hour truncation of
// Calendar's now instant, and the public caller supplies the completed
// entity/rate fields.
func (r *Store) fillDashboardUsageStatsAggregated(ctx context.Context, stats *DashboardStats, todayUTC, now time.Time) error {
	combinedStatsQuery := `
		SELECT
			COALESCE(SUM(total_requests), 0), COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(total_cost), 0),
			COALESCE(SUM(actual_cost), 0), COALESCE(SUM(provider_cost), 0),
			COALESCE(SUM(total_duration_ms), 0),
			COALESCE(SUM(total_requests) FILTER (WHERE bucket_date = $1::date), 0),
			COALESCE(SUM(input_tokens) FILTER (WHERE bucket_date = $1::date), 0),
			COALESCE(SUM(output_tokens) FILTER (WHERE bucket_date = $1::date), 0),
			COALESCE(SUM(cache_creation_tokens) FILTER (WHERE bucket_date = $1::date), 0),
			COALESCE(SUM(cache_read_tokens) FILTER (WHERE bucket_date = $1::date), 0),
			COALESCE(SUM(total_cost) FILTER (WHERE bucket_date = $1::date), 0),
			COALESCE(SUM(actual_cost) FILTER (WHERE bucket_date = $1::date), 0),
			COALESCE(SUM(provider_cost) FILTER (WHERE bucket_date = $1::date), 0),
			COALESCE(MAX(active_users) FILTER (WHERE bucket_date = $1::date), 0)
		FROM usage_dashboard_daily
	`
	var totalDurationMs int64
	combinedStats := func(queryCtx context.Context) error {
		return scanSingleRow(
			queryCtx, r.sql, combinedStatsQuery, []any{todayUTC},
			&stats.TotalRequests,
			&stats.TotalInputTokens,
			&stats.TotalOutputTokens,
			&stats.TotalCacheCreationTokens,
			&stats.TotalCacheReadTokens,
			&stats.TotalCost,
			&stats.TotalActualCost,
			&stats.TotalProviderCost,
			&totalDurationMs,
			&stats.TodayRequests,
			&stats.TodayInputTokens,
			&stats.TodayOutputTokens,
			&stats.TodayCacheCreationTokens,
			&stats.TodayCacheReadTokens,
			&stats.TodayCost,
			&stats.TodayActualCost,
			&stats.TodayProviderCost,
			&stats.ActiveUsers,
		)
	}

	hourlyActiveQuery := `
		SELECT active_users
		FROM usage_dashboard_hourly
		WHERE bucket_start = $1
	`
	hourStart := now.In(r.calendar.Location()).Truncate(time.Hour)
	hourlyActive := func(queryCtx context.Context) error {
		if err := scanSingleRow(queryCtx, r.sql, hourlyActiveQuery, []any{hourStart}, &stats.HourlyActiveUsers); err != nil {
			// An absent current-hour bucket represents no stored activity for that key;
			// retain zero while propagating every other query error to the dashboard
			// caller.
			if err != sql.ErrNoRows {
				return err
			}
		}
		return nil
	}
	if err := r.runDashboardQueries(ctx, combinedStats, hourlyActive); err != nil {
		return err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens
	if stats.TotalRequests > 0 {
		// The administrator denominator includes every request, treating missing
		// durations as zero.
		stats.AverageDurationMs = float64(totalDurationMs) / float64(stats.TotalRequests)
	}

	return nil
}

// fillDashboardUsageStatsFromUsageLogs fills bounded totals, fixed-length today
// values, and distinct actor activity from raw rows. todayUTC begins a
// twenty-four-hour elapsed window; now selects a separate UTC-aligned current
// hour.
//
// A shared scan covers the union of requested and today ranges, then FILTER sums
// keep each result's inclusion rule separate. Provider cost uses its recorded
// base/multiplier while actual_cost remains user charges. Null durations
// contribute zero to the all-request average. A second union scan counts
// distinct actors for today and the current hour, allowing the two activity
// measures to overlap without adding them together.
func (r *Store) fillDashboardUsageStatsFromUsageLogs(ctx context.Context, stats *DashboardStats, startUTC, endUTC, todayUTC, now time.Time) error {
	// Raw administrator today spans twenty-four elapsed hours from its midnight.
	todayEnd := todayUTC.Add(24 * time.Hour)
	combinedStatsQuery := `
		WITH scoped AS (
			SELECT
				created_at,
				input_tokens,
				output_tokens,
				cache_creation_tokens,
				cache_read_tokens,
				total_cost,
				actual_cost,
				COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1) AS provider_cost,
				COALESCE(duration_ms, 0) AS duration_ms
			FROM usage_logs
			WHERE created_at >= LEAST($1::timestamptz, $3::timestamptz)
				AND created_at < GREATEST($2::timestamptz, $4::timestamptz)
		)
		SELECT
			COUNT(*) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz) AS total_requests,
			COALESCE(SUM(input_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_input_tokens,
			COALESCE(SUM(output_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_output_tokens,
			COALESCE(SUM(cache_creation_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_cache_read_tokens,
			COALESCE(SUM(total_cost) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_cost,
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_actual_cost,
			COALESCE(SUM(provider_cost) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_provider_cost,
			COALESCE(SUM(duration_ms) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_duration_ms,
			COUNT(*) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz) AS today_requests,
			COALESCE(SUM(input_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_input_tokens,
			COALESCE(SUM(output_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_output_tokens,
			COALESCE(SUM(cache_creation_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_cache_read_tokens,
			COALESCE(SUM(total_cost) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_cost,
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_actual_cost,
			COALESCE(SUM(provider_cost) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_provider_cost
		FROM scoped
	`
	var totalDurationMs int64
	if err := scanSingleRow(
		ctx,
		r.sql,
		combinedStatsQuery,
		[]any{startUTC, endUTC, todayUTC, todayEnd},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.TotalProviderCost,
		&totalDurationMs,
		&stats.TodayRequests,
		&stats.TodayInputTokens,
		&stats.TodayOutputTokens,
		&stats.TodayCacheCreationTokens,
		&stats.TodayCacheReadTokens,
		&stats.TodayCost,
		&stats.TodayActualCost,
		&stats.TodayProviderCost,
	); err != nil {
		return err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	if stats.TotalRequests > 0 {
		// The administrator denominator includes every request, treating missing
		// durations as zero.
		stats.AverageDurationMs = float64(totalDurationMs) / float64(stats.TotalRequests)
	}

	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens

	hourStart := now.UTC().Truncate(time.Hour)
	hourEnd := hourStart.Add(time.Hour)
	activeUsersQuery := `
		WITH scoped AS (
			SELECT user_id, created_at
			FROM usage_logs
			WHERE created_at >= LEAST($1::timestamptz, $3::timestamptz)
				AND created_at < GREATEST($2::timestamptz, $4::timestamptz)
		)
		SELECT
			COUNT(DISTINCT CASE WHEN created_at >= $1::timestamptz AND created_at < $2::timestamptz THEN user_id END) AS active_users,
			COUNT(DISTINCT CASE WHEN created_at >= $3::timestamptz AND created_at < $4::timestamptz THEN user_id END) AS hourly_active_users
		FROM scoped
	`
	return scanSingleRow(ctx, r.sql, activeUsersQuery, []any{todayUTC, todayEnd, hourStart, hourEnd}, &stats.ActiveUsers, &stats.HourlyActiveUsers)
}

// GetUserDashboardStats returns retained actor and current owned-team metrics,
// key counts, and recent rates. Usable multidimensional analytics supplies the
// response; an unavailable or failed attempt chooses the complete raw dashboard.
//
// Raw actor and team branches exclude duplicate owner rows and use one scan for
// all-time and today sums. Today begins at Calendar midnight with no upper
// bound, including future-dated records. Duration averages count observed
// duration values, and key, usage, and rate callbacks write distinct fields
// before their executor-aware runner is joined. The caller authorizes the user's
// owner-visible scope.
func (r *Store) GetUserDashboardStats(ctx context.Context, userID int64) (*UserDashboardStats, error) {
	// Analytics availability or query failure selects a complete raw dashboard;
	// accumulated fields from an unsuccessful analytics attempt are discarded.
	if stats, ok, err := r.getUserDashboardStatsFromAnalytics(ctx, userID); err == nil && ok {
		return stats, nil
	} else if err != nil {
		r.logUsageAnalyticsFallback("user_dashboard", err)
	}
	stats := &UserDashboardStats{}
	today := r.calendar.Today()
	teamID, err := r.getOwnedTeamID(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Actor and owned-team branches share the selected scope. Excluding owner rows
	// from the team branch keeps the key counts consistent with usage ownership.
	loadAPIKeyStats := func(queryCtx context.Context) error {
		return scanSingleRow(queryCtx, r.sql, `
			WITH scoped AS (
				SELECT status FROM api_keys
				WHERE deleted_at IS NULL AND user_id = $1
				UNION ALL
				SELECT status FROM api_keys
				WHERE deleted_at IS NULL AND $2 > 0 AND team_id = $2 AND user_id <> $1
			)
			SELECT COUNT(*), COUNT(*) FILTER (WHERE status = $3) FROM scoped
		`, []any{userID, teamID, identity.StatusActive}, &stats.TotalAPIKeys, &stats.ActiveAPIKeys)
	}

	// The same disjoint actor/team source supplies retained and lower-only today
	// sums; one scan also counts observed durations for the user average.
	var totalDuration, durationCount int64
	usageStatsQuery := `
		WITH scoped AS (
			SELECT created_at, input_tokens, output_tokens, cache_creation_tokens,
			       cache_read_tokens, total_cost, actual_cost, duration_ms
			FROM usage_logs WHERE user_id = $1
			UNION ALL
			SELECT created_at, input_tokens, output_tokens, cache_creation_tokens,
			       cache_read_tokens, total_cost, actual_cost, duration_ms
			FROM usage_logs
			WHERE $2 > 0 AND team_id = $2 AND user_id <> $1
		)
		SELECT
			COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(total_cost), 0), COALESCE(SUM(actual_cost), 0),
			COALESCE(SUM(COALESCE(duration_ms, 0)), 0), COUNT(duration_ms),
			COUNT(*) FILTER (WHERE created_at >= $3),
			COALESCE(SUM(input_tokens) FILTER (WHERE created_at >= $3), 0),
			COALESCE(SUM(output_tokens) FILTER (WHERE created_at >= $3), 0),
			COALESCE(SUM(cache_creation_tokens) FILTER (WHERE created_at >= $3), 0),
			COALESCE(SUM(cache_read_tokens) FILTER (WHERE created_at >= $3), 0),
			COALESCE(SUM(total_cost) FILTER (WHERE created_at >= $3), 0),
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $3), 0)
		FROM scoped
	`
	loadUsageStats := func(queryCtx context.Context) error {
		return scanSingleRow(
			queryCtx,
			r.sql,
			usageStatsQuery,
			[]any{userID, teamID, today},
			&stats.TotalRequests,
			&stats.TotalInputTokens,
			&stats.TotalOutputTokens,
			&stats.TotalCacheCreationTokens,
			&stats.TotalCacheReadTokens,
			&stats.TotalCost,
			&stats.TotalActualCost,
			&totalDuration,
			&durationCount,
			&stats.TodayRequests,
			&stats.TodayInputTokens,
			&stats.TodayOutputTokens,
			&stats.TodayCacheCreationTokens,
			&stats.TodayCacheReadTokens,
			&stats.TodayCost,
			&stats.TodayActualCost,
		)
	}

	// Recent-rate rows use a separate lower time bound from retained totals,
	// keeping the short-window scan independent of the dashboard's all-time row
	// count.
	var rpm, tpm int64
	loadPerformanceStats := func(queryCtx context.Context) error {
		var performanceErr error
		rpm, tpm, performanceErr = r.getPerformanceStatsForScope(queryCtx, userID, teamID)
		return performanceErr
	}
	if err := r.runDashboardQueries(ctx, loadAPIKeyStats, loadUsageStats, loadPerformanceStats); err != nil {
		return nil, err
	}
	if durationCount > 0 {
		// User dashboard duration averages include observed timings and omit NULL
		// durations.
		stats.AverageDurationMs = float64(totalDuration) / float64(durationCount)
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens

	stats.Rpm = rpm
	stats.Tpm = tpm

	return stats, nil
}

// getPerformanceStatsByAPIKey returns integer five-minute RPM and TPM for the
// selected key. TPM includes creation/read-cache tokens along with input/output
// tokens, and its lower-only time range can count future-dated records.
func (r *Store) getPerformanceStatsByAPIKey(ctx context.Context, apiKeyID int64) (rpm, tpm int64, err error) {
	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
	query := `
		SELECT
			COUNT(*) as request_count,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as token_count
		FROM usage_logs
		WHERE created_at >= $1 AND api_key_id = $2`
	args := []any{fiveMinutesAgo, apiKeyID}

	var requestCount int64
	var tokenCount int64
	if err := scanSingleRow(ctx, r.sql, query, args, &requestCount, &tokenCount); err != nil {
		return 0, 0, err
	}
	// Convert five-minute counts to integer per-minute averages, truncating
	// fractions.
	return requestCount / 5, tokenCount / 5, nil
}

// GetAPIKeyDashboardStats returns retained key totals, today values, and raw
// recent rates. It first tries the key analytics helper; an error or unavailable
// source discards that attempt and computes both totals in one raw scan.
//
// Today begins at Calendar midnight and both totals include future-dated raw
// records. The duration average counts nonnull values, while reported
// TotalAPIKeys and ActiveAPIKeys are fixed at one for this selected-key view.
// Recent rates always read raw records independently, and any required read
// failure aborts the response.
func (r *Store) GetAPIKeyDashboardStats(ctx context.Context, apiKeyID int64) (*UserDashboardStats, error) {
	stats, aggregated, err := r.getAPIKeyDashboardStatsFromAnalytics(ctx, apiKeyID)
	if err != nil {
		r.logUsageAnalyticsFallback("api_key_dashboard", err)
	}
	if err != nil || !aggregated {
		stats = &UserDashboardStats{}
		// Failed or unavailable analytics leaves both summaries to one raw scan; the
		// lower-only today predicate includes future-dated rows along with retained
		// totals.
		query := `SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(total_cost), 0), COALESCE(SUM(actual_cost), 0), COALESCE(AVG(duration_ms), 0),
			COUNT(*) FILTER (WHERE created_at >= $2),
			COALESCE(SUM(input_tokens) FILTER (WHERE created_at >= $2), 0),
			COALESCE(SUM(output_tokens) FILTER (WHERE created_at >= $2), 0),
			COALESCE(SUM(cache_creation_tokens) FILTER (WHERE created_at >= $2), 0),
			COALESCE(SUM(cache_read_tokens) FILTER (WHERE created_at >= $2), 0),
			COALESCE(SUM(total_cost) FILTER (WHERE created_at >= $2), 0),
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $2), 0)
			FROM usage_logs WHERE api_key_id = $1`
		if err := scanSingleRow(ctx, r.sql, query, []any{apiKeyID, r.calendar.Today()},
			&stats.TotalRequests, &stats.TotalInputTokens, &stats.TotalOutputTokens,
			&stats.TotalCacheCreationTokens, &stats.TotalCacheReadTokens,
			&stats.TotalCost, &stats.TotalActualCost, &stats.AverageDurationMs,
			&stats.TodayRequests, &stats.TodayInputTokens, &stats.TodayOutputTokens,
			&stats.TodayCacheCreationTokens, &stats.TodayCacheReadTokens,
			&stats.TodayCost, &stats.TodayActualCost); err != nil {
			return nil, err
		}
	}
	stats.TotalAPIKeys = 1
	stats.ActiveAPIKeys = 1
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens
	// Read recent rates directly from usage records even when totals use analytics,
	// so their request/token counters do not wait for the rollup watermark.
	rpm, tpm, err := r.getPerformanceStatsByAPIKey(ctx, apiKeyID)
	if err != nil {
		return nil, err
	}
	stats.Rpm = rpm
	stats.Tpm = tpm
	return stats, nil
}
