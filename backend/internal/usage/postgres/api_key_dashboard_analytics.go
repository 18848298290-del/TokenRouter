package postgres

import (
	"context"
	"database/sql"
	"time"
)

// Public API-key summaries cover the key's currently retained raw history,
// including records dated after the observed current time. The initial raw
// minimum supplies the cumulative lower bound; a missing minimum returns zero
// totals even when rollups remain. Covered UTC buckets are combined with raw
// edges, and the raw tail extends to PostgreSQL infinity.
//
// Today's summary begins at Calendar.Today or the later oldest record. It can
// use raw rows when its range has no usable rollup segment, while
// cumulative-query failures let GetAPIKeyDashboardStats retry the full raw
// calculation. Duration averaging divides the combined duration sum by
// nonnull-duration counts, keeping requests with missing timings out of that
// denominator.

// getAPIKeyDashboardStatsFromAnalytics returns cumulative and today statistics
// plus whether this analytics attempt can answer the key's query. Disabled
// aggregation returns false. When the initial raw-history lookup finds no
// record, the attempt returns successful empty statistics even when old
// rollups exist.
//
// Cumulative totals start at the oldest retained record, and today starts at
// the later of that record and Calendar.Today. An unavailable today rollup
// segment uses raw records; cumulative unavailability or a query error lets the
// public caller retry the full raw calculation. Both totals include the
// unbounded raw tail.
// @project-doc docs/operations/pre_aggregation.md#query_routing_and_fallback
func (r *Store) getAPIKeyDashboardStatsFromAnalytics(ctx context.Context, keyID int64) (*UserDashboardStats, bool, error) {
	if r.preAggregation == nil || !r.preAggregation.UsageEnabled(ctx) {
		return nil, false, nil
	}
	var oldest sql.NullTime
	if err := scanSingleRow(ctx, r.sql, `SELECT MIN(created_at) FROM usage_logs WHERE api_key_id = $1`, []any{keyID}, &oldest); err != nil {
		return nil, false, err
	}
	stats := &UserDashboardStats{}
	if !oldest.Valid {
		return stats, true, nil
	}
	now := r.calendar.Now()
	total, ok, err := r.openEndedAPIKeyAnalytics(ctx, keyID, oldest.Time, now)
	if err != nil || !ok {
		return nil, false, err
	}
	if err := r.scanAPIKeyAnalytics(ctx, total,
		&stats.TotalRequests, &stats.TotalInputTokens, &stats.TotalOutputTokens,
		&stats.TotalCacheCreationTokens, &stats.TotalCacheReadTokens,
		&stats.TotalCost, &stats.TotalActualCost, &stats.AverageDurationMs); err != nil {
		return nil, false, err
	}
	today := r.calendar.Today()
	if oldest.Time.After(today) {
		today = oldest.Time
	}
	daily, ok, err := r.openEndedAPIKeyAnalytics(ctx, keyID, today, now)
	if err != nil {
		return nil, false, err
	}
	var ignoredAverage float64
	if ok {
		err = r.scanAPIKeyAnalytics(ctx, daily,
			&stats.TodayRequests, &stats.TodayInputTokens, &stats.TodayOutputTokens,
			&stats.TodayCacheCreationTokens, &stats.TodayCacheReadTokens,
			&stats.TodayCost, &stats.TodayActualCost, &ignoredAverage)
	} else {
		// Today can begin after the usable aggregate segment. Read its full raw range
		// so the cumulative analytics result can still be returned.
		err = scanSingleRow(ctx, r.sql, `SELECT COUNT(*), COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(total_cost), 0), COALESCE(SUM(actual_cost), 0)
			FROM usage_logs WHERE api_key_id = $1 AND created_at >= $2`, []any{keyID, today},
			&stats.TodayRequests, &stats.TodayInputTokens, &stats.TodayOutputTokens,
			&stats.TodayCacheCreationTokens, &stats.TodayCacheReadTokens,
			&stats.TodayCost, &stats.TodayActualCost)
	}
	return stats, err == nil, err
}

// openEndedAPIKeyAnalytics constructs a combined source whose raw tail includes
// future-dated records for this API key. It first resolves the covered rollup
// intervals against now and the stored watermark. Unavailable aggregation
// returns false with the builder's error result.
//
// On a usable query, replacing its second argument with PostgreSQL infinity
// changes the raw upper bound. The separately resolved hourly and daily bounds
// remain fixed, so extending the tail does not reinterpret coverage or include
// future rollup buckets.
func (r *Store) openEndedAPIKeyAnalytics(ctx context.Context, keyID int64, start, now time.Time) (usageAnalyticsQuery, bool, error) {
	query, ok, err := r.buildUsageAnalyticsQuery(ctx, UsageLogFilters{APIKeyID: keyID}, start, now, true)
	if ok && err == nil {
		// The builder assigns $2 to the raw upper endpoint; changing that argument
		// includes future raw records while keeping independently resolved rollup
		// bounds.
		query.args[1] = "infinity"
	}
	return query, ok, err
}

// scanAPIKeyAnalytics scans request, token, cost, and duration averages from a
// combined analytics source into dest. Callers supply eight compatible
// destinations in SQL column order. Empty sums and an absent duration
// denominator return zero values.
//
// Duration counts represent observations with nonnull timings, so the average
// uses summed duration divided by summed duration_count across raw and rollup
// rows. It does not divide by total requests, which also includes unmeasured
// requests. Source filters and arguments pass through unchanged to the query
// executor.
func (r *Store) scanAPIKeyAnalytics(ctx context.Context, query usageAnalyticsQuery, dest ...any) error {
	// NULLIF makes an empty measured-duration count produce SQL NULL, then
	// COALESCE supplies the zero average expected by dashboard consumers.
	return scanSingleRow(ctx, r.sql, query.cte+` SELECT
		COALESCE(SUM(total_requests), 0), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_creation_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
		COALESCE(SUM(total_cost), 0), COALESCE(SUM(actual_cost), 0),
		COALESCE(SUM(total_duration_ms)::double precision / NULLIF(SUM(duration_count), 0), 0)
		FROM combined `+query.where, query.args, dest...)
}
