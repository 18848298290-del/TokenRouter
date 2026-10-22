package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lib/pq"
	"golang.org/x/sync/errgroup"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// usage_log_repo_stats.go calculates filtered usage summaries, provider costs,
// batch charges, and endpoint statistics.
// Usage summaries keep base cost, actual user charges, and provider expense as
// separate measures. Provider reports combine recorded provider-cost bases with
// their multipliers, while actor and key summaries retain the monetary meanings
// of each selected column even when a provider restriction is present.
//
// Different callers ask for different windows: bounded reports use half-open
// endpoints, provider windows and raw today sums have only lower bounds, and
// optional endpoint details supply their own default epoch-to-now span. Filtered
// summaries can recover from analytics to a complete raw scan and tolerate
// individual endpoint-detail failures. The chosen SQL executor determines
// concurrent versus serial dispatch, and duration averages count either all rows
// or observed values at the owning entry point.

// UsageStats contains request, token, cost, timing, and endpoint summary values.
type UsageStats = usage.UsageStats

// BatchUserUsageStats holds actor range/today charges with recorded platform
// breakdowns.
type BatchUserUsageStats = usage.BatchUserUsageStats

// PlatformUsage holds range and today user charges for one recorded platform.
type PlatformUsage = usage.PlatformUsage

// BatchAPIKeyUsageStats holds range and today charges for one selected key.
type BatchAPIKeyUsageStats = usage.BatchAPIKeyUsageStats

// ProviderUsageHistory contains a populated provider day with separate monetary
// measures.
type ProviderUsageHistory = usage.ProviderUsageHistory

// ProviderUsageSummary holds provider report totals, populated-day averages, and
// peak-day values.
type ProviderUsageSummary = usage.ProviderUsageSummary

// ProviderUsageStatsResponse groups provider history, summary, models, and
// endpoint report rows.
type ProviderUsageStatsResponse = usage.ProviderUsageStatsResponse

// EndpointStat contains endpoint requests, cache-inclusive tokens, base cost,
// and user charges.
type EndpointStat = usage.EndpointStat

// GetProviderWindowStatsPair returns two provider summaries starting at their
// supplied instants. Both starts are inclusive with no upper bound, so
// future-dated records can contribute to either result.
//
// One SQL scan begins at the earlier start, and separate FILTER aggregates
// assign each row to the applicable window. Overlapping rows contribute to both
// summaries rather than being divided between them. Each summary keeps provider
// cost, base cost, user charge, requests, and all token buckets distinct,
// allowing callers to replace two separate window reads with one consistent
// statement.
func (r *Store) GetProviderWindowStatsPair(ctx context.Context, id int64, firstStart, secondStart time.Time) (*usage.ProviderStats, *usage.ProviderStats, error) {
	const query = `SELECT
	 COUNT(*) FILTER (WHERE created_at >= $2),
	 COALESCE(SUM(input_tokens+output_tokens+cache_creation_tokens+cache_read_tokens) FILTER (WHERE created_at >= $2),0),
	 COALESCE(SUM(COALESCE(provider_stats_cost,total_cost)*COALESCE(provider_rate_multiplier,1)) FILTER (WHERE created_at >= $2),0),
	 COALESCE(SUM(total_cost) FILTER (WHERE created_at >= $2),0),
	 COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $2),0),
	 COUNT(*) FILTER (WHERE created_at >= $3),
	 COALESCE(SUM(input_tokens+output_tokens+cache_creation_tokens+cache_read_tokens) FILTER (WHERE created_at >= $3),0),
	 COALESCE(SUM(COALESCE(provider_stats_cost,total_cost)*COALESCE(provider_rate_multiplier,1)) FILTER (WHERE created_at >= $3),0),
	 COALESCE(SUM(total_cost) FILTER (WHERE created_at >= $3),0),
	 COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $3),0)
	 FROM usage_logs WHERE provider_id=$1 AND created_at >= LEAST($2,$3)`
	a, b := &usage.ProviderStats{}, &usage.ProviderStats{}
	if err := scanSingleRow(ctx, r.sql, query, []any{id, firstStart, secondStart},
		&a.Requests, &a.Tokens, &a.Cost, &a.StandardCost, &a.UserCost,
		&b.Requests, &b.Tokens, &b.Cost, &b.StandardCost, &b.UserCost); err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

// GetUserStatsAggregated returns raw actor totals for the supplied half-open
// range. Its SQL aggregates treat missing duration as zero in the average;
// provider-cost totals remain unset in this simpler summary.
func (r *Store) GetUserStatsAggregated(ctx context.Context, userID int64, startTime, endTime time.Time) (*usage.UsageStats, error) {
	query := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(COALESCE(duration_ms, 0)), 0) as avg_duration_ms
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
	`

	var stats usage.UsageStats
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{userID, startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	return &stats, nil
}

// GetAPIKeyStatsAggregated returns raw key totals within the supplied half-open
// range. Missing durations count as zero in the average, token totals include
// both cache buckets, and this simpler response leaves provider-cost totals
// unset.
func (r *Store) GetAPIKeyStatsAggregated(ctx context.Context, apiKeyID int64, startTime, endTime time.Time) (*usage.UsageStats, error) {
	query := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(COALESCE(duration_ms, 0)), 0) as avg_duration_ms
		FROM usage_logs
		WHERE api_key_id = $1 AND created_at >= $2 AND created_at < $3
	`

	var stats usage.UsageStats
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{apiKeyID, startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	return &stats, nil
}

// GetProviderStatsAggregated returns raw provider-filtered usage totals for a
// half-open range. TotalCost remains base cost and TotalActualCost remains user
// charges; missing durations count as zero, and provider-expense totals remain
// unset.
func (r *Store) GetProviderStatsAggregated(ctx context.Context, providerID int64, startTime, endTime time.Time) (*usage.UsageStats, error) {
	query := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(COALESCE(duration_ms, 0)), 0) as avg_duration_ms
		FROM usage_logs
		WHERE provider_id = $1 AND created_at >= $2 AND created_at < $3
	`

	var stats usage.UsageStats
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{providerID, startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	return &stats, nil
}

// GetModelStatsAggregated summarizes raw rows equal to the stored model value
// within the half-open range. It treats missing durations as zero and leaves
// provider-expense totals unset; dimension-aware model reports use separate
// methods.
func (r *Store) GetModelStatsAggregated(ctx context.Context, modelName string, startTime, endTime time.Time) (*usage.UsageStats, error) {
	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(COALESCE(duration_ms, 0)), 0) as avg_duration_ms
		FROM usage_logs
		WHERE %s = $1 AND created_at >= $2 AND created_at < $3
	`, rawUsageLogModelColumn)

	var stats usage.UsageStats
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{modelName, startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	return &stats, nil
}

// GetDailyStatsAggregated returns actor totals as date-labeled maps for the
// supplied half-open range. SQL binds the resolved reporting timezone, averages
// missing durations as zero, and returns an allocated empty slice when no dates
// match.
func (r *Store) GetDailyStatsAggregated(ctx context.Context, userID int64, startTime, endTime time.Time) (result []map[string]any, err error) {
	tzName := r.resolveUsageStatsTimezone()
	query := `
		SELECT
			-- 使用应用时区分组，避免数据库会话时区导致日边界偏移。
			TO_CHAR(created_at AT TIME ZONE $4, 'YYYY-MM-DD') as date,
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(COALESCE(duration_ms, 0)), 0) as avg_duration_ms
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY 1
		ORDER BY 1
	`

	rows, err := r.sql.QueryContext(ctx, query, userID, startTime, endTime, tzName)
	if err != nil {
		return nil, err
	}
	// Release daily rows on every exit. An otherwise successful Close failure
	// clears the date series before returning its error to the report caller.
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			result = nil
		}
	}()

	result = make([]map[string]any, 0)
	for rows.Next() {
		var (
			date              string
			totalRequests     int64
			totalInputTokens  int64
			totalOutputTokens int64
			totalCacheTokens  int64
			totalCost         float64
			totalActualCost   float64
			avgDurationMs     float64
		)
		if err = rows.Scan(
			&date,
			&totalRequests,
			&totalInputTokens,
			&totalOutputTokens,
			&totalCacheTokens,
			&totalCost,
			&totalActualCost,
			&avgDurationMs,
		); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{
			"date":                date,
			"total_requests":      totalRequests,
			"total_input_tokens":  totalInputTokens,
			"total_output_tokens": totalOutputTokens,
			"total_cache_tokens":  totalCacheTokens,
			"total_tokens":        totalInputTokens + totalOutputTokens + totalCacheTokens,
			"total_cost":          totalCost,
			"total_actual_cost":   totalActualCost,
			"average_duration_ms": avgDurationMs,
		})
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// resolveUsageStatsTimezone returns the SQL reporting-zone name selected by
// Calendar, TZ, then UTC. A nonempty Calendar name other than Local wins; TZ is
// trimmed but not validated by this helper.
func (r *Store) resolveUsageStatsTimezone() string {
	tzName := r.calendar.Location().String()
	if tzName != "" && tzName != "Local" {
		return tzName
	}
	if envTZ := strings.TrimSpace(os.Getenv("TZ")); envTZ != "" {
		return envTZ
	}
	return "UTC"
}

// GetProviderTodayStats summarizes provider records since Calendar's local
// midnight with no upper bound. Future-dated rows participate, and the result
// separates recorded provider expense, base cost, actual user charges, requests,
// and cache-inclusive tokens.
func (r *Store) GetProviderTodayStats(ctx context.Context, providerID int64) (*usage.ProviderStats, error) {
	today := r.calendar.Today()

	query := `
		SELECT
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens,
			COALESCE(SUM(COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)), 0) as cost,
			COALESCE(SUM(total_cost), 0) as standard_cost,
			COALESCE(SUM(actual_cost), 0) as user_cost
		FROM usage_logs
		WHERE provider_id = $1 AND created_at >= $2
	`

	stats := &usage.ProviderStats{}
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{providerID, today},
		&stats.Requests,
		&stats.Tokens,
		&stats.Cost,
		&stats.StandardCost,
		&stats.UserCost,
	); err != nil {
		return nil, err
	}
	return stats, nil
}

// GetProviderWindowStats returns provider totals at or after startTime with no
// upper bound. Future-dated rows participate, and provider expense uses the
// recorded cost base/multiplier independently of base cost and actual user
// charges.
func (r *Store) GetProviderWindowStats(ctx context.Context, providerID int64, startTime time.Time) (*usage.ProviderStats, error) {
	query := `
		SELECT
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens,
			COALESCE(SUM(COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)), 0) as cost,
			COALESCE(SUM(total_cost), 0) as standard_cost,
			COALESCE(SUM(actual_cost), 0) as user_cost
		FROM usage_logs
		WHERE provider_id = $1 AND created_at >= $2
	`

	stats := &usage.ProviderStats{}
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{providerID, startTime},
		&stats.Requests,
		&stats.Tokens,
		&stats.Cost,
		&stats.StandardCost,
		&stats.UserCost,
	); err != nil {
		return nil, err
	}
	return stats, nil
}

// GetProviderWindowStatsBatch returns lower-bounded totals for every requested
// provider ID. Empty input yields an empty map; providers with no rows receive
// zero summaries, and future-dated records remain inside the open-ended window.
func (r *Store) GetProviderWindowStatsBatch(ctx context.Context, providerIDs []int64, startTime time.Time) (map[int64]*usage.ProviderStats, error) {
	result := make(map[int64]*usage.ProviderStats, len(providerIDs))
	if len(providerIDs) == 0 {
		return result, nil
	}

	query := `
		SELECT
			provider_id,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens,
			COALESCE(SUM(COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)), 0) as cost,
			COALESCE(SUM(total_cost), 0) as standard_cost,
			COALESCE(SUM(actual_cost), 0) as user_cost
		FROM usage_logs
		WHERE provider_id = ANY($1) AND created_at >= $2
		GROUP BY provider_id
	`
	rows, err := r.sql.QueryContext(ctx, query, pq.Array(providerIDs), startTime)
	if err != nil {
		return nil, err
	}
	// Release batch rows after scanning or an early return. Close errors are
	// ignored here, while query, scan, and iteration failures reject the result.
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var providerID int64
		stats := &usage.ProviderStats{}
		if err := rows.Scan(
			&providerID,
			&stats.Requests,
			&stats.Tokens,
			&stats.Cost,
			&stats.StandardCost,
			&stats.UserCost,
		); err != nil {
			return nil, err
		}
		result[providerID] = stats
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, providerID := range providerIDs {
		if _, ok := result[providerID]; !ok {
			result[providerID] = &usage.ProviderStats{}
		}
	}
	return result, nil
}

// GetGeminiUsageTotalsBatch returns provider request, token, and expense totals
// split into Flash and Pro for [startTime, endTime). Empty input returns an
// empty map, and missing providers receive zero-valued entries.
//
// Stored model text containing flash or lite, ignoring case, enters Flash; every
// other selected model enters Pro. That textual classification applies to the
// rows supplied by provider scope, rather than validating model families. Cost
// uses the provider base and multiplier, while token totals include input/output
// and both cache buckets, keeping request counts and expense paired with the
// same classification.
func (r *Store) GetGeminiUsageTotalsBatch(ctx context.Context, providerIDs []int64, startTime, endTime time.Time) (map[int64]provider.GeminiUsageTotals, error) {
	result := make(map[int64]provider.GeminiUsageTotals, len(providerIDs))
	if len(providerIDs) == 0 {
		return result, nil
	}

	query := `
		SELECT
			provider_id,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN 1 ELSE 0 END), 0) AS flash_requests,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN 0 ELSE 1 END), 0) AS pro_requests,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN (input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) ELSE 0 END), 0) AS flash_tokens,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN 0 ELSE (input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) END), 0) AS pro_tokens,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1) ELSE 0 END), 0) AS flash_cost,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN 0 ELSE COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1) END), 0) AS pro_cost
		FROM usage_logs
		WHERE provider_id = ANY($1) AND created_at >= $2 AND created_at < $3
		GROUP BY provider_id
	`
	rows, err := r.sql.QueryContext(ctx, query, pq.Array(providerIDs), startTime, endTime)
	if err != nil {
		return nil, err
	}
	// Release batch rows after scanning or an early return. Close errors are
	// ignored here, while query, scan, and iteration failures reject the result.
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var providerID int64
		var totals provider.GeminiUsageTotals
		if err := rows.Scan(
			&providerID,
			&totals.FlashRequests,
			&totals.ProRequests,
			&totals.FlashTokens,
			&totals.ProTokens,
			&totals.FlashCost,
			&totals.ProCost,
		); err != nil {
			return nil, err
		}
		result[providerID] = totals
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, providerID := range providerIDs {
		if _, ok := result[providerID]; !ok {
			result[providerID] = provider.GeminiUsageTotals{}
		}
	}
	return result, nil
}

// normalizePositiveInt64IDs returns distinct positive IDs in first-occurrence
// order. Empty input returns nil, and a supplied list containing only invalid
// IDs returns an allocated empty slice after filtering.
func normalizePositiveInt64IDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// GetBatchUserUsageStats returns actor charges with stored-platform breakdowns
// for distinct positive user IDs. Zero bounds select thirty calendar dates
// before system now through a separately sampled now; empty normalized input
// returns an empty map.
//
// Usable analytics supplies the whole result. Otherwise one raw query sums
// positive actual_cost by user/platform, separating bounded range totals from
// lower-only Calendar-today totals, including future rows in today. Every
// requested user starts with zero fields, and absent platform labels contribute
// to totals while remaining absent from ByPlatform. Aggregate errors are logged
// before complete raw recovery.
func (r *Store) GetBatchUserUsageStats(ctx context.Context, userIDs []int64, startTime, endTime time.Time) (map[int64]*BatchUserUsageStats, error) {
	result := make(map[int64]*BatchUserUsageStats)
	normalizedUserIDs := normalizePositiveInt64IDs(userIDs)
	if len(normalizedUserIDs) == 0 {
		return result, nil
	}

	if startTime.IsZero() {
		startTime = time.Now().AddDate(0, 0, -30)
	}
	if endTime.IsZero() {
		endTime = time.Now()
	}
	// Unusable or failed analytics is discarded before this complete raw batch,
	// preventing partial source totals from being added to the same records twice.
	if stats, ok, err := r.getBatchUserUsageStatsFromAnalytics(ctx, normalizedUserIDs, startTime, endTime); err == nil && ok {
		return stats, nil
	} else if err != nil {
		r.logUsageAnalyticsFallback("batch_user_usage", err)
	}

	for _, id := range normalizedUserIDs {
		result[id] = &BatchUserUsageStats{UserID: id}
	}

	// Grouping keeps recorded platform attribution stable after configuration
	// changes. Accumulate each actor's row amounts before adding nonempty labels to
	// its breakdown.
	query := `
		SELECT
			ul.user_id,
			` + usageLogEffectivePlatformExpr + ` as platform,
			COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $2 AND ul.created_at < $3), 0) as total_cost,
			COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $4), 0) as today_cost
		FROM usage_logs ul
		LEFT JOIN groups g ON g.id = ul.group_id
		LEFT JOIN providers a ON a.id = ul.provider_id
		WHERE ul.user_id = ANY($1)
		  AND ul.created_at >= LEAST($2, $4)
		  AND ` + usageLogSuccessFilterUL + `
		GROUP BY ul.user_id, ` + usageLogEffectivePlatformExpr + `
	`
	today := r.calendar.Today()
	rows, err := r.sql.QueryContext(ctx, query, pq.Array(normalizedUserIDs), startTime, endTime, today)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var userID int64
		var platform sql.NullString
		var total float64
		var todayTotal float64
		if err := rows.Scan(&userID, &platform, &total, &todayTotal); err != nil {
			// A scan failure leaves a partial batch unusable. Release its result rows
			// before returning so the caller cannot consume the accumulated prefix.
			_ = rows.Close()
			return nil, err
		}
		stats, ok := result[userID]
		if !ok {
			continue
		}
		stats.TotalActualCost += total
		stats.TodayActualCost += todayTotal
		if platform.Valid && platform.String != "" {
			stats.ByPlatform = append(stats.ByPlatform, PlatformUsage{
				Platform:        platform.String,
				TotalActualCost: total,
				TodayActualCost: todayTotal,
			})
		}
	}
	// Close the completed batch before returning it. A cleanup failure is an error
	// even when all requested identities have already been populated.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// GetBatchAPIKeyUsageStats returns range and today user charges for distinct
// positive key IDs. Empty normalized IDs return an empty map, while zero bounds
// use thirty prior calendar dates through separately sampled system now.
//
// The complete analytics result is preferred when usable. Raw recovery
// initializes every requested key, then one query separately sums the bounded
// range and Calendar-today values. Today's raw predicate has no upper bound, so
// future rows remain included independently of endTime. Requested keys with no
// rows retain zero fields, and raw query failures discard the batch.
func (r *Store) GetBatchAPIKeyUsageStats(ctx context.Context, apiKeyIDs []int64, startTime, endTime time.Time) (map[int64]*BatchAPIKeyUsageStats, error) {
	result := make(map[int64]*BatchAPIKeyUsageStats)
	normalizedAPIKeyIDs := normalizePositiveInt64IDs(apiKeyIDs)
	if len(normalizedAPIKeyIDs) == 0 {
		return result, nil
	}

	if startTime.IsZero() {
		startTime = time.Now().AddDate(0, 0, -30)
	}
	if endTime.IsZero() {
		endTime = time.Now()
	}
	// Failed or unavailable analytics leaves every requested key to one complete
	// raw scan; discard the partial analytics result before initializing zero
	// entries.
	if stats, ok, err := r.getBatchAPIKeyUsageStatsFromAnalytics(ctx, normalizedAPIKeyIDs, startTime, endTime); err == nil && ok {
		return stats, nil
	} else if err != nil {
		r.logUsageAnalyticsFallback("batch_api_key_usage", err)
	}

	for _, id := range normalizedAPIKeyIDs {
		result[id] = &BatchAPIKeyUsageStats{APIKeyID: id}
	}

	query := `
		SELECT
			api_key_id,
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $2 AND created_at < $3), 0) as total_cost,
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $4), 0) as today_cost
		FROM usage_logs
		WHERE api_key_id = ANY($1)
		  AND created_at >= LEAST($2, $4)
		GROUP BY api_key_id
	`
	today := r.calendar.Today()
	rows, err := r.sql.QueryContext(ctx, query, pq.Array(normalizedAPIKeyIDs), startTime, endTime, today)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var apiKeyID int64
		var total float64
		var todayTotal float64
		if err := rows.Scan(&apiKeyID, &total, &todayTotal); err != nil {
			// A scan failure leaves a partial batch unusable. Release its result rows
			// before returning so the caller cannot consume the accumulated prefix.
			_ = rows.Close()
			return nil, err
		}
		if stats, ok := result[apiKeyID]; ok {
			stats.TotalActualCost = total
			stats.TodayActualCost = todayTotal
		}
	}
	// Close the completed batch before returning it. A cleanup failure is an error
	// even when all requested identities have already been populated.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// resolveEndpointColumn returns a fixed aliased endpoint expression for
// breakdown filters. Upstream and path select their defined expressions; every
// other label selects the inbound column, leaving caller input out of SQL
// identifier interpolation.
func resolveEndpointColumn(endpointType string) string {
	switch endpointType {
	case "upstream":
		return "ul.upstream_endpoint"
	case "path":
		return "ul.inbound_endpoint || ' -> ' || ul.upstream_endpoint"
	default:
		return "ul.inbound_endpoint"
	}
}

// GetGlobalStats summarizes raw records across all actors within the half-open
// range. Its duration average counts nonnull timings, all cache tokens enter
// TotalTokens, and provider-expense totals remain unset in this simpler
// response.
func (r *Store) GetGlobalStats(ctx context.Context, startTime, endTime time.Time) (*UsageStats, error) {
	query := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(duration_ms), 0) as avg_duration_ms
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
	`

	stats := &UsageStats{}
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	return stats, nil
}

// GetStatsWithFilters returns authorized-filter totals with optional inbound,
// upstream, and path details. Missing summary bounds leave its raw total
// unrestricted, while endpoint details default to epoch through now; detail
// failures can produce empty lists.
func (r *Store) GetStatsWithFilters(ctx context.Context, filters UsageLogFilters) (*UsageStats, error) {
	return r.getStatsWithFilters(ctx, filters, true)
}

// GetUserStatsWithFilters returns filtered totals and eligible inbound endpoint
// details. It omits upstream/path dispatch, shares the summary's scope and date
// defaults, and permits endpoint failures to return empty details while summary
// failures remain errors.
func (r *Store) GetUserStatsWithFilters(ctx context.Context, filters UsageLogFilters) (*UsageStats, error) {
	return r.getStatsWithFilters(ctx, filters, false)
}

// getStatsWithFilters assembles a summary and optional endpoint details for
// caller-authorized selectors. Raw filters give RequestType precedence over
// Stream, and optional times form half-open restrictions; absent times leave the
// summary's retained source open-ended.
//
// Compatible analytics supplies the whole summary or raw SQL replaces it.
// Endpoint windows separately default to epoch and now, and EndpointSource
// selects empty/all or one recognized detail. Their failures become empty lists,
// with non-cancellation errors logged, while summary failure aborts the
// response. Pool queries execute concurrently and transaction executors
// serially; returned provider cost stays allocated even when zero.
func (r *Store) getStatsWithFilters(ctx context.Context, filters UsageLogFilters, includeRawEndpointDetails bool) (*UsageStats, error) {
	conditions := make([]string, 0, 9)
	args := make([]any, 0, 9)

	source, scopeCondition, args, scopeErr := r.buildUsageLogScopeSource(ctx, args, filters.UserID, filters.IncludeOwnedTeam, "")
	if scopeErr != nil {
		return nil, scopeErr
	}
	if scopeCondition != "" {
		conditions = append(conditions, scopeCondition)
	}
	if filters.APIKeyID > 0 {
		conditions = append(conditions, fmt.Sprintf("api_key_id = $%d", len(args)+1))
		args = append(args, filters.APIKeyID)
	}
	if filters.ProviderID > 0 {
		conditions = append(conditions, fmt.Sprintf("provider_id = $%d", len(args)+1))
		args = append(args, filters.ProviderID)
	}
	if filters.GroupID > 0 {
		conditions = append(conditions, fmt.Sprintf("group_id = $%d", len(args)+1))
		args = append(args, filters.GroupID)
	}
	if filters.TeamID > 0 {
		conditions = append(conditions, fmt.Sprintf("team_id = $%d", len(args)+1))
		args = append(args, filters.TeamID)
	}
	if filters.PersonalOnly {
		conditions = append(conditions, "team_id IS NULL")
	}
	if id := strings.TrimSpace(filters.RequestID); id != "" {
		conditions = append(conditions, usageRequestIDCondition("", len(args)+1))
		args = append(args, id)
	}
	conditions, args = appendUsageLogModelWhereCondition(conditions, args, filters.Model, filters.ModelFilterSource)
	conditions, args = appendRequestTypeOrStreamWhereCondition(conditions, args, filters.RequestType, filters.Stream)
	conditions, args = appendNativeCompactionV2WhereCondition(conditions, args, filters.NativeCompactionV2, "")
	if filters.BillingType != nil {
		conditions = append(conditions, fmt.Sprintf("billing_type = $%d", len(args)+1))
		args = append(args, int16(*filters.BillingType))
	}
	conditions, args = appendUsageLogBillingModeWhereCondition(conditions, args, filters.BillingMode)
	if filters.StartTime != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", len(args)+1))
		args = append(args, *filters.StartTime)
	}
	if filters.EndTime != nil {
		conditions = append(conditions, fmt.Sprintf("created_at < $%d", len(args)+1))
		args = append(args, *filters.EndTime)
	}

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(SUM(COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)), 0) as total_provider_cost,
			COALESCE(AVG(duration_ms), 0) as avg_duration_ms
		FROM %s
		%s
	`, source, buildWhere(conditions))

	stats := &UsageStats{}
	var totalProviderCost float64
	// An unavailable or failed analytics summary leaves the complete raw query to
	// runSummary; partial analytics values do not become inputs to that read.
	if aggregated, ok, aggregateErr := r.getUsageStatsFromAnalytics(ctx, filters); aggregateErr == nil && ok {
		stats = aggregated
		if aggregated.TotalProviderCost != nil {
			totalProviderCost = *aggregated.TotalProviderCost
		}
	} else if aggregateErr != nil {
		r.logUsageAnalyticsFallback("usage_stats", aggregateErr)
	}

	start := time.Unix(0, 0).UTC()
	if filters.StartTime != nil {
		start = *filters.StartTime
	}
	end := time.Now().UTC()
	if filters.EndTime != nil {
		end = *filters.EndTime
	}

	var endpoints, upstreamEndpoints, endpointPaths []EndpointStat

	// TotalProviderCost marks a completed analytics summary. If absent, raw SQL
	// fills the summary, whose failure aborts the response rather than returning
	// details alone.
	runSummary := func(c context.Context) error {
		if stats.TotalProviderCost != nil {
			return nil
		}
		return scanSingleRow(
			c, r.sql, query, args,
			&stats.TotalRequests,
			&stats.TotalInputTokens,
			&stats.TotalOutputTokens,
			&stats.TotalCacheTokens,
			&stats.TotalCacheCreationTokens,
			&stats.TotalCacheReadTokens,
			&stats.TotalCost,
			&stats.TotalActualCost,
			&totalProviderCost,
			&stats.AverageDurationMs,
		)
	}
	// Inbound details can fail independently of summary totals. Retain an empty
	// list on failure and log non-cancellation errors before publishing the
	// summary.
	runEndpoints := func(c context.Context) {
		if filters.EndpointSource != "" && filters.EndpointSource != "inbound" {
			return
		}
		res, err := r.getEndpointStatsByColumnWithFilters(c, "inbound_endpoint", start, end, filters)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				logger.LegacyPrintf("repository.usage_log", "GetEndpointStatsWithFilters failed in GetStatsWithFilters: %v", err)
			}
			// Detail failure leaves summary values useful. Discard any partial endpoint
			// rows and return an empty detail list for this selected dimension.
			res = []EndpointStat{}
		}
		endpoints = res
	}
	runUpstream := func(c context.Context) {
		if filters.EndpointSource != "" && filters.EndpointSource != "upstream" {
			return
		}
		res, err := r.getEndpointStatsByColumnWithFilters(c, "upstream_endpoint", start, end, filters)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				logger.LegacyPrintf("repository.usage_log", "GetUpstreamEndpointStatsWithFilters failed in GetStatsWithFilters: %v", err)
			}
			// Detail failure leaves summary values useful. Discard any partial endpoint
			// rows and return an empty detail list for this selected dimension.
			res = []EndpointStat{}
		}
		upstreamEndpoints = res
	}
	runPaths := func(c context.Context) {
		if filters.EndpointSource != "" && filters.EndpointSource != "path" {
			return
		}
		res, err := r.getEndpointPathStatsWithFilters(c, start, end, filters)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				logger.LegacyPrintf("repository.usage_log", "getEndpointPathStatsWithFilters failed in GetStatsWithFilters: %v", err)
			}
			// Detail failure leaves summary values useful. Discard any partial endpoint
			// rows and return an empty detail list for this selected dimension.
			res = []EndpointStat{}
		}
		endpointPaths = res
	}

	if r.db != nil {
		// Summary and detail callbacks write separate fields through pool connections;
		// join every member before the selected lists enter the response.
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error { return runSummary(gctx) })
		g.Go(func() error { runEndpoints(gctx); return nil })
		if includeRawEndpointDetails {
			g.Go(func() error { runUpstream(gctx); return nil })
			g.Go(func() error { runPaths(gctx); return nil })
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
	} else {
		// A transaction executor shares its selected connection. Finish summary and
		// each detail query serially so result sets are released between statements.
		if err := runSummary(ctx); err != nil {
			return nil, err
		}
		runEndpoints(ctx)
		if includeRawEndpointDetails {
			runUpstream(ctx)
			runPaths(ctx)
		}
	}

	stats.TotalProviderCost = &totalProviderCost
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	stats.Endpoints = endpoints
	stats.UpstreamEndpoints = upstreamEndpoints
	stats.EndpointPaths = endpointPaths

	return stats, nil
}

// getEndpointStatsByColumnWithFilters returns endpoint totals for
// caller-authorized selectors in the supplied half-open range. endpointColumn is
// trusted SQL text from fixed callers, and filters.RequestID is trimmed before
// lookup. Explicit time arguments define the range.
//
// Inbound grouping can use compatible analytics; upstream grouping always
// follows raw fields. Raw actor and owned-team branches retain disjoint
// ownership, request type precedes stream, and blank endpoint values become
// unknown. Base cost and actual user charge remain separate even under provider
// filters. Scan, iteration, or successful-path cleanup failures reject the
// response.
func (r *Store) getEndpointStatsByColumnWithFilters(ctx context.Context, endpointColumn string, startTime, endTime time.Time, filters UsageLogFilters) (results []EndpointStat, err error) {
	requestID := strings.TrimSpace(filters.RequestID)

	if endpointColumn == "inbound_endpoint" {
		analyticsFilters := filters
		analyticsFilters.RequestID = requestID
		// Inbound analytics can answer only represented selectors. Declining or failing
		// that attempt leaves the whole endpoint grouping to the raw query below.
		if aggregated, ok, aggregateErr := r.getInboundEndpointStatsFromAnalytics(ctx, startTime, endTime, analyticsFilters); aggregateErr == nil && ok {
			return aggregated, nil
		} else if aggregateErr != nil {
			r.logUsageAnalyticsFallback("inbound_endpoint_stats", aggregateErr)
		}
	}
	// Provider scope narrows the rows, while endpoint actual_cost still sums user
	// charges; provider expense uses its distinct metric in provider reports.
	args := []any{startTime, endTime}
	source, scopeCondition, args, scopeErr := r.buildUsageLogScopeSource(ctx, args, filters.UserID, filters.IncludeOwnedTeam, "")
	if scopeErr != nil {
		return nil, scopeErr
	}
	query := fmt.Sprintf(`
		SELECT
			COALESCE(NULLIF(TRIM(%s), ''), 'unknown') AS endpoint,
			COUNT(*) AS requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS total_tokens,
			COALESCE(SUM(total_cost), 0) as cost,
			COALESCE(SUM(actual_cost), 0) as actual_cost
		FROM %s
		WHERE created_at >= $1 AND created_at < $2
	`, endpointColumn, source)

	if requestID != "" {
		query += " AND " + usageRequestIDCondition("", len(args)+1)
		args = append(args, requestID)
	}

	if scopeCondition != "" {
		query += " AND " + scopeCondition
	}
	if filters.APIKeyID > 0 {
		query += fmt.Sprintf(" AND api_key_id = $%d", len(args)+1)
		args = append(args, filters.APIKeyID)
	}
	if filters.ProviderID > 0 {
		query += fmt.Sprintf(" AND provider_id = $%d", len(args)+1)
		args = append(args, filters.ProviderID)
	}
	if filters.GroupID > 0 {
		query += fmt.Sprintf(" AND group_id = $%d", len(args)+1)
		args = append(args, filters.GroupID)
	}
	if filters.TeamID > 0 {
		query += fmt.Sprintf(" AND team_id = $%d", len(args)+1)
		args = append(args, filters.TeamID)
	}
	if filters.PersonalOnly {
		query += " AND team_id IS NULL"
	}
	query, args = appendUsageLogModelQueryFilter(query, args, filters.Model, filters.ModelFilterSource)
	query, args = appendRequestTypeOrStreamQueryFilter(query, args, filters.RequestType, filters.Stream)
	query, args = appendNativeCompactionV2QueryFilter(query, args, filters.NativeCompactionV2, "")
	if filters.BillingType != nil {
		query += fmt.Sprintf(" AND billing_type = $%d", len(args)+1)
		args = append(args, int16(*filters.BillingType))
	}
	query, args = appendUsageLogBillingModeQueryFilter(query, args, filters.BillingMode, "")
	query += " GROUP BY endpoint ORDER BY requests DESC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	// Release endpoint rows on every exit. Keep earlier failures, while successful
	// scanning still returns an error and clears results if closing fails.
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]EndpointStat, 0)
	for rows.Next() {
		var row EndpointStat
		if err := rows.Scan(&row.Endpoint, &row.Requests, &row.TotalTokens, &row.Cost, &row.ActualCost); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// getEndpointPathStatsWithFilters returns raw inbound-to-upstream path totals
// for caller-authorized selectors in the supplied half-open range. It trims
// filters.RequestID before lookup, and blank path endpoints use unknown; explicit
// time arguments define the range.
//
// Scope construction keeps actor and owned-team rows disjoint before grouping
// both endpoint labels into one path. Transport selectors use the raw precedence
// rule, and provider restrictions retain base cost and user-charge meanings.
// This path has no aggregate dimension, so it reads raw records; scan,
// iteration, or successful-path close failures discard the result.
func (r *Store) getEndpointPathStatsWithFilters(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters) (results []EndpointStat, err error) {
	requestID := strings.TrimSpace(filters.RequestID)

	// Path labels combine inbound and upstream evidence, while actual_cost keeps
	// the user-charge measure used by the separate endpoint groupings.
	args := []any{startTime, endTime}
	source, scopeCondition, args, scopeErr := r.buildUsageLogScopeSource(ctx, args, filters.UserID, filters.IncludeOwnedTeam, "")
	if scopeErr != nil {
		return nil, scopeErr
	}
	query := fmt.Sprintf(`
		SELECT
			CONCAT(
				COALESCE(NULLIF(TRIM(inbound_endpoint), ''), 'unknown'),
				' -> ',
				COALESCE(NULLIF(TRIM(upstream_endpoint), ''), 'unknown')
			) AS endpoint,
			COUNT(*) AS requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS total_tokens,
			COALESCE(SUM(total_cost), 0) as cost,
			COALESCE(SUM(actual_cost), 0) as actual_cost
		FROM %s
		WHERE created_at >= $1 AND created_at < $2
	`, source)

	if requestID != "" {
		query += " AND " + usageRequestIDCondition("", len(args)+1)
		args = append(args, requestID)
	}

	if scopeCondition != "" {
		query += " AND " + scopeCondition
	}
	if filters.APIKeyID > 0 {
		query += fmt.Sprintf(" AND api_key_id = $%d", len(args)+1)
		args = append(args, filters.APIKeyID)
	}
	if filters.ProviderID > 0 {
		query += fmt.Sprintf(" AND provider_id = $%d", len(args)+1)
		args = append(args, filters.ProviderID)
	}
	if filters.GroupID > 0 {
		query += fmt.Sprintf(" AND group_id = $%d", len(args)+1)
		args = append(args, filters.GroupID)
	}
	if filters.TeamID > 0 {
		query += fmt.Sprintf(" AND team_id = $%d", len(args)+1)
		args = append(args, filters.TeamID)
	}
	if filters.PersonalOnly {
		query += " AND team_id IS NULL"
	}
	query, args = appendUsageLogModelQueryFilter(query, args, filters.Model, filters.ModelFilterSource)
	query, args = appendRequestTypeOrStreamQueryFilter(query, args, filters.RequestType, filters.Stream)
	query, args = appendNativeCompactionV2QueryFilter(query, args, filters.NativeCompactionV2, "")
	if filters.BillingType != nil {
		query += fmt.Sprintf(" AND billing_type = $%d", len(args)+1)
		args = append(args, int16(*filters.BillingType))
	}
	query, args = appendUsageLogBillingModeQueryFilter(query, args, filters.BillingMode, "")
	query += " GROUP BY endpoint ORDER BY requests DESC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	// Release endpoint rows on every exit. Keep earlier failures, while successful
	// scanning still returns an error and clears results if closing fails.
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]EndpointStat, 0)
	for rows.Next() {
		var row EndpointStat
		if err := rows.Scan(&row.Endpoint, &row.Requests, &row.TotalTokens, &row.Cost, &row.ActualCost); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// GetEndpointStatsWithFilters returns inbound endpoint totals over a half-open
// range with the base selectors. Zero IDs and nil transport/billing selectors
// leave those dimensions unrestricted; the caller authorizes the selected scope.
func (r *Store) GetEndpointStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) ([]EndpointStat, error) {
	return r.getEndpointStatsByColumnWithFilters(ctx, "inbound_endpoint", startTime, endTime, UsageLogFilters{
		UserID:      userID,
		APIKeyID:    apiKeyID,
		ProviderID:  providerID,
		GroupID:     groupID,
		Model:       model,
		RequestType: requestType,
		Stream:      stream,
		BillingType: billingType,
	})
}

// GetUpstreamEndpointStatsWithFilters returns raw upstream endpoint totals over
// the supplied half-open range. Zero IDs and nil selectors leave dimensions
// unrestricted, and actual cost retains user charges even when selecting a
// provider.
func (r *Store) GetUpstreamEndpointStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) ([]EndpointStat, error) {
	return r.getEndpointStatsByColumnWithFilters(ctx, "upstream_endpoint", startTime, endTime, UsageLogFilters{
		UserID:      userID,
		APIKeyID:    apiKeyID,
		ProviderID:  providerID,
		GroupID:     groupID,
		Model:       model,
		RequestType: requestType,
		Stream:      stream,
		BillingType: billingType,
	})
}

// GetProviderUsageStats returns daily, model, and endpoint provider reports over
// the supplied half-open range. The underlying reader uses one grouped source
// scan and, for a pool, a dedicated read-only transaction with local JIT
// settings.
//
// Daily history supplies total provider expense, base cost, user charges, and
// maxima. Daily averages divide by populated history dates, using one for empty
// history; duration averages use observed timings. Summary.Days counts elapsed
// twenty-four-hour intervals plus one, with thirty for nonpositive counts.
// Today's matching label uses Calendar's current date, while history dates
// follow the provider reader's SQL grouping.
func (r *Store) GetProviderUsageStats(ctx context.Context, providerID int64, startTime, endTime time.Time) (resp *ProviderUsageStatsResponse, err error) {
	// Count elapsed twenty-four-hour intervals plus one for the summary's Days
	// value.
	daysCount := int(endTime.Sub(startTime).Hours()/24) + 1
	// Nonpositive elapsed counts retain the report's thirty-day display default;
	// the supplied SQL range still determines which records are actually returned.
	if daysCount <= 0 {
		daysCount = 30
	}

	data, err := r.readProviderReport(ctx, providerID, startTime, endTime)
	if err != nil {
		return nil, err
	}
	history := data.history

	var totalProviderCost, totalUserCost, totalStandardCost float64
	var totalRequests, totalTokens int64
	var highestCostDay, highestRequestDay *ProviderUsageHistory

	for i := range history {
		h := &history[i]
		totalProviderCost += h.ActualCost
		totalUserCost += h.UserCost
		totalStandardCost += h.Cost
		totalRequests += h.Requests
		totalTokens += h.Tokens

		if highestCostDay == nil || h.ActualCost > highestCostDay.ActualCost {
			highestCostDay = h
		}
		if highestRequestDay == nil || h.Requests > highestRequestDay.Requests {
			highestRequestDay = h
		}
	}

	actualDaysUsed := len(history)
	// Empty history uses one as the denominator so zero totals produce defined
	// daily averages while the returned history itself remains empty.
	if actualDaysUsed == 0 {
		actualDaysUsed = 1
	}

	var avgDuration float64
	if data.durationCount > 0 {
		avgDuration = float64(data.durationSum) / float64(data.durationCount)
	}

	summary := ProviderUsageSummary{
		Days:              daysCount,
		ActualDaysUsed:    actualDaysUsed,
		TotalCost:         totalProviderCost,
		TotalUserCost:     totalUserCost,
		TotalStandardCost: totalStandardCost,
		TotalRequests:     totalRequests,
		TotalTokens:       totalTokens,
		AvgDailyCost:      totalProviderCost / float64(actualDaysUsed),
		AvgDailyUserCost:  totalUserCost / float64(actualDaysUsed),
		AvgDailyRequests:  float64(totalRequests) / float64(actualDaysUsed),
		AvgDailyTokens:    float64(totalTokens) / float64(actualDaysUsed),
		AvgDurationMs:     avgDuration,
	}

	todayStr := r.calendar.Now().Format("2006-01-02")
	for i := range history {
		if history[i].Date == todayStr {
			summary.Today = &struct {
				Date     string  `json:"date"`      // report date label
				Cost     float64 `json:"cost"`      // provider expense in USD
				UserCost float64 `json:"user_cost"` // user charges in USD
				Requests int64   `json:"requests"`  // recorded request count
				Tokens   int64   `json:"tokens"`    // input output and cache tokens
			}{
				Date:     history[i].Date,
				Cost:     history[i].ActualCost,
				UserCost: history[i].UserCost,
				Requests: history[i].Requests,
				Tokens:   history[i].Tokens,
			}
			break
		}
	}

	if highestCostDay != nil {
		summary.HighestCostDay = &struct {
			Date     string  `json:"date"`      // report date label
			Label    string  `json:"label"`     // shortened date display label
			Cost     float64 `json:"cost"`      // provider expense in USD
			UserCost float64 `json:"user_cost"` // user charges in USD
			Requests int64   `json:"requests"`  // recorded request count
		}{
			Date:     highestCostDay.Date,
			Label:    highestCostDay.Label,
			Cost:     highestCostDay.ActualCost,
			UserCost: highestCostDay.UserCost,
			Requests: highestCostDay.Requests,
		}
	}

	if highestRequestDay != nil {
		summary.HighestRequestDay = &struct {
			Date     string  `json:"date"`      // report date label
			Label    string  `json:"label"`     // shortened date display label
			Requests int64   `json:"requests"`  // recorded request count
			Cost     float64 `json:"cost"`      // provider expense in USD
			UserCost float64 `json:"user_cost"` // user charges in USD
		}{
			Date:     highestRequestDay.Date,
			Label:    highestRequestDay.Label,
			Requests: highestRequestDay.Requests,
			Cost:     highestRequestDay.ActualCost,
			UserCost: highestRequestDay.UserCost,
		}
	}

	resp = &ProviderUsageStatsResponse{
		History:           history,
		Summary:           summary,
		Models:            data.models,
		Endpoints:         data.endpoints,
		UpstreamEndpoints: data.upstreamEndpoints,
	}
	return resp, nil
}
