package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/usage"
)

const (
	// usageAnalyticsRawDimensionsSQL normalizes raw columns in rollup order; callers alias usage_logs as ul.
	usageAnalyticsRawDimensionsSQL = `
			ul.user_id,
			COALESCE(ul.billing_user_id, ul.user_id),
			COALESCE(ul.team_id, 0),
			ul.api_key_id,
			COALESCE(ul.group_id, 0),
			COALESCE(NULLIF(TRIM(ul.model), ''), NULLIF(TRIM(ul.requested_model), ''), ''),
			CASE
				WHEN COALESCE(ul.request_type, 0) <> 0 THEN ul.request_type
				WHEN COALESCE(ul.openai_ws_mode, FALSE) THEN 3
				WHEN COALESCE(ul.stream, FALSE) THEN 2
				ELSE 1
			END,
			COALESCE(ul.stream, FALSE),
			COALESCE(ul.billing_type, 0),
			COALESCE(
				NULLIF(ul.billing_mode, ''),
				CASE
					WHEN COALESCE(ul.video_duration_seconds, 0) > 0 THEN 'video'
					WHEN COALESCE(ul.image_count, 0) > 0 THEN 'image'
					ELSE 'token'
				END
			),
			ul.platform,
			COALESCE(ul.inbound_endpoint, ''),`
)

// AggregateUsageAnalyticsRange rebuilds affected UTC hour and day buckets, scanning the final hour up to end.
func (r *AggregationStore) AggregateUsageAnalyticsRange(ctx context.Context, start, end time.Time) error {
	return r.aggregateUsageAnalyticsRange(ctx, start, end, false, true)
}

// AggregateUsageAnalyticsHourlyRange rebuilds affected UTC hour buckets without rebuilding daily data.
func (r *AggregationStore) AggregateUsageAnalyticsHourlyRange(ctx context.Context, start, end time.Time) error {
	return r.aggregateUsageAnalyticsRange(ctx, start, end, false, false)
}

// RecomputeUsageAnalyticsRange rebuilds complete UTC hour and day buckets for an inclusive cleanup range.
func (r *AggregationStore) RecomputeUsageAnalyticsRange(ctx context.Context, start, end time.Time) error {
	return r.aggregateUsageAnalyticsRange(ctx, start, end, true, true)
}

func (r *AggregationStore) aggregateUsageAnalyticsRange(ctx context.Context, start, end time.Time, includeEndBucket, rebuildDaily bool) error {
	if r == nil || r.sql == nil || !end.After(start) {
		return nil
	}
	hourStart := start.UTC().Truncate(time.Hour)
	endUTC := end.UTC()
	hourEnd := endUTC.Truncate(time.Hour)
	if includeEndBucket || endUTC.After(hourEnd) {
		hourEnd = hourEnd.Add(time.Hour)
	}
	if !hourEnd.After(hourStart) {
		return nil
	}

	scanEnd := endUTC
	if includeEndBucket {
		// Cleanup includes end; rescan the entire last bucket to retain records outside the deleted range.
		scanEnd = hourEnd
	}
	if scanEnd.After(hourEnd) {
		scanEnd = hourEnd
	}
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := NewAggregationStoreWithSQL(tx, r.calendar)
		if err := txRepo.aggregateUsageAnalyticsRangeInTx(ctx, hourStart, hourEnd, scanEnd, rebuildDaily); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.aggregateUsageAnalyticsRangeInTx(ctx, hourStart, hourEnd, scanEnd, rebuildDaily)
}

func (r *AggregationStore) aggregateUsageAnalyticsRangeInTx(ctx context.Context, hourStart, hourEnd, scanEnd time.Time, rebuildDaily bool) error {
	if _, err := r.sql.ExecContext(ctx, `
		DELETE FROM usage_analytics_hourly
		WHERE bucket_start >= $1 AND bucket_start < $2
	`, hourStart, hourEnd); err != nil {
		return err
	}

	// Normalize legacy request types and billing modes when writing rollups so readers use consistent dimensions.
	if _, err := r.sql.ExecContext(ctx, `
		INSERT INTO usage_analytics_hourly (
			bucket_start, user_id, billing_user_id, team_id, api_key_id, group_id,
			requested_model, request_type, stream, billing_type, billing_mode,
			platform, inbound_endpoint, total_requests, input_tokens, output_tokens,
			cache_creation_tokens, cache_read_tokens, total_cost, actual_cost,
			provider_cost, total_duration_ms, duration_count, computed_at
		)
		SELECT
			date_trunc('hour', ul.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
`+usageAnalyticsRawDimensionsSQL+`
			COUNT(*),
			COALESCE(SUM(ul.input_tokens), 0),
			COALESCE(SUM(ul.output_tokens), 0),
			COALESCE(SUM(ul.cache_creation_tokens), 0),
			COALESCE(SUM(ul.cache_read_tokens), 0),
			COALESCE(SUM(ul.total_cost), 0),
			COALESCE(SUM(ul.actual_cost), 0),
			COALESCE(SUM(COALESCE(ul.provider_stats_cost, ul.total_cost) * COALESCE(ul.provider_rate_multiplier, 1)), 0),
			COALESCE(SUM(COALESCE(ul.duration_ms, 0)), 0),
			COUNT(ul.duration_ms),
			NOW()
		FROM usage_logs ul
		LEFT JOIN groups g ON g.id = ul.group_id
		LEFT JOIN providers a ON a.id = ul.provider_id
		WHERE ul.created_at >= $1 AND ul.created_at < $2
		GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13
	`, hourStart, scanEnd); err != nil {
		return err
	}

	if !rebuildDaily {
		return nil
	}
	dayStart := truncateUsageAnalyticsDay(hourStart)
	lastHour := hourEnd.Add(-time.Nanosecond)
	dayEnd := truncateUsageAnalyticsDay(lastHour).AddDate(0, 0, 1)
	return r.rebuildUsageAnalyticsDailyRangeInTx(ctx, dayStart, dayEnd)
}

// RebuildUsageAnalyticsDailyRange rebuilds complete affected UTC days from hourly data.
func (r *AggregationStore) RebuildUsageAnalyticsDailyRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil || !end.After(start) {
		return nil
	}
	dayStart := truncateUsageAnalyticsDay(start)
	dayEnd := truncateUsageAnalyticsDay(end)
	if end.UTC().After(dayEnd) {
		dayEnd = dayEnd.AddDate(0, 0, 1)
	}
	if !dayEnd.After(dayStart) {
		return nil
	}
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := NewAggregationStoreWithSQL(tx, r.calendar)
		if err := txRepo.rebuildUsageAnalyticsDailyRangeInTx(ctx, dayStart, dayEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.rebuildUsageAnalyticsDailyRangeInTx(ctx, dayStart, dayEnd)
}

func (r *AggregationStore) rebuildUsageAnalyticsDailyRangeInTx(ctx context.Context, dayStart, dayEnd time.Time) error {
	if _, err := r.sql.ExecContext(ctx, `
		DELETE FROM usage_analytics_daily
		WHERE bucket_date >= $1::date AND bucket_date < $2::date
	`, dayStart, dayEnd); err != nil {
		return err
	}
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO usage_analytics_daily (
			bucket_date, user_id, billing_user_id, team_id, api_key_id, group_id,
			requested_model, request_type, stream, billing_type, billing_mode,
			platform, inbound_endpoint, total_requests, input_tokens, output_tokens,
			cache_creation_tokens, cache_read_tokens, total_cost, actual_cost,
			provider_cost, total_duration_ms, duration_count, computed_at
		)
		SELECT
			(bucket_start AT TIME ZONE 'UTC')::date,
			user_id, billing_user_id, team_id, api_key_id, group_id,
			requested_model, request_type, stream, billing_type, billing_mode,
			platform, inbound_endpoint,
			SUM(total_requests), SUM(input_tokens), SUM(output_tokens),
			SUM(cache_creation_tokens), SUM(cache_read_tokens), SUM(total_cost),
			SUM(actual_cost), SUM(provider_cost), SUM(total_duration_ms), SUM(duration_count), NOW()
		FROM usage_analytics_hourly
		WHERE bucket_start >= $1 AND bucket_start < $2
		GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13
	`, dayStart, dayEnd)
	return err
}

func truncateUsageAnalyticsDay(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

// GetUsageAnalyticsAggregationState reads the singleton progress row.
func (r *AggregationStore) GetUsageAnalyticsAggregationState(ctx context.Context) (*usage.UsageAnalyticsAggregationState, error) {
	state := &usage.UsageAnalyticsAggregationState{}
	var coverageStart, backfillCursor, sourceOldest, manualStart, manualCursor sql.NullTime
	var lastRun, lastSuccess, lastErrorAt sql.NullTime
	err := scanSingleRow(ctx, r.sql, `
		SELECT live_watermark, coverage_start, backfill_cursor, source_oldest_at,
		       manual_backfill_start, manual_backfill_cursor,
		       phase, last_run_at, last_success_at, last_error_at, last_error, last_duration_ms
		FROM usage_analytics_aggregation_state WHERE id = 1
	`, nil, &state.LiveWatermark, &coverageStart, &backfillCursor, &sourceOldest,
		&manualStart, &manualCursor, &state.Phase, &lastRun, &lastSuccess, &lastErrorAt,
		&state.LastError, &state.LastDurationMS)
	if err != nil {
		return nil, err
	}
	state.CoverageStart = nullableTimePointer(coverageStart)
	state.BackfillCursor = nullableTimePointer(backfillCursor)
	state.SourceOldestAt = nullableTimePointer(sourceOldest)
	state.ManualBackfillStart = nullableTimePointer(manualStart)
	state.ManualBackfillCursor = nullableTimePointer(manualCursor)
	state.LastRunAt = nullableTimePointer(lastRun)
	state.LastSuccessAt = nullableTimePointer(lastSuccess)
	state.LastErrorAt = nullableTimePointer(lastErrorAt)
	state.LiveWatermark = state.LiveWatermark.UTC()
	return state, nil
}

// SaveUsageAnalyticsAggregationState replaces all fields of the singleton progress row.
func (r *AggregationStore) SaveUsageAnalyticsAggregationState(ctx context.Context, state *usage.UsageAnalyticsAggregationState) error {
	if state == nil {
		return nil
	}
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO usage_analytics_aggregation_state (
			id, live_watermark, coverage_start, backfill_cursor, source_oldest_at,
			manual_backfill_start, manual_backfill_cursor, phase, last_run_at,
			last_success_at, last_error_at, last_error, last_duration_ms, updated_at
		) VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW())
		ON CONFLICT (id) DO UPDATE SET
			live_watermark = EXCLUDED.live_watermark,
			coverage_start = EXCLUDED.coverage_start,
			backfill_cursor = EXCLUDED.backfill_cursor,
			source_oldest_at = EXCLUDED.source_oldest_at,
			manual_backfill_start = EXCLUDED.manual_backfill_start,
			manual_backfill_cursor = EXCLUDED.manual_backfill_cursor,
			phase = EXCLUDED.phase,
			last_run_at = EXCLUDED.last_run_at,
			last_success_at = EXCLUDED.last_success_at,
			last_error_at = EXCLUDED.last_error_at,
			last_error = EXCLUDED.last_error,
			last_duration_ms = EXCLUDED.last_duration_ms,
			updated_at = NOW()
	`, state.LiveWatermark.UTC(), state.CoverageStart, state.BackfillCursor, state.SourceOldestAt,
		state.ManualBackfillStart, state.ManualBackfillCursor, state.Phase, state.LastRunAt,
		state.LastSuccessAt, state.LastErrorAt, state.LastError, state.LastDurationMS)
	return err
}

// GetOldestUsageLogTime reads the oldest raw timestamp using created_at ordering.
func (r *AggregationStore) GetOldestUsageLogTime(ctx context.Context) (*time.Time, error) {
	var oldest sql.NullTime
	if err := scanSingleRow(ctx, r.sql, `
		SELECT (
			SELECT created_at
			FROM usage_logs
			ORDER BY created_at ASC
			LIMIT 1
		)
	`, nil, &oldest); err != nil {
		return nil, err
	}
	return nullableTimePointer(oldest), nil
}

// CleanupUsageAnalytics deletes multidimensional buckets older than the supplied retention cutoffs.
func (r *AggregationStore) CleanupUsageAnalytics(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	if _, err := r.sql.ExecContext(ctx, `DELETE FROM usage_analytics_hourly WHERE bucket_start < $1`, hourlyCutoff.UTC()); err != nil {
		return err
	}
	_, err := r.sql.ExecContext(ctx, `DELETE FROM usage_analytics_daily WHERE bucket_date < $1::date`, dailyCutoff.UTC())
	return err
}

func nullableTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

// ApplyUsageAnalyticsState locks the progress row in a short transaction; aggregation queries execute separately.
func (r *AggregationStore) ApplyUsageAnalyticsState(ctx context.Context, change usage.AnalyticsStateChange) (*usage.UsageAnalyticsAggregationState, error) {
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		next, err := NewAggregationStoreWithSQL(tx, r.calendar).applyUsageAnalyticsStateInTx(ctx, change)
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return next, nil
	}
	return r.applyUsageAnalyticsStateInTx(ctx, change)
}

func (r *AggregationStore) applyUsageAnalyticsStateInTx(ctx context.Context, change usage.AnalyticsStateChange) (*usage.UsageAnalyticsAggregationState, error) {
	var id int64
	if err := scanSingleRow(ctx, r.sql, "SELECT id FROM usage_analytics_aggregation_state WHERE id=1 FOR UPDATE", nil, &id); err != nil {
		return nil, err
	}
	current, err := r.GetUsageAnalyticsAggregationState(ctx)
	if err != nil {
		return nil, err
	}
	next, err := usage.ApplyAnalyticsStateChange(*current, change)
	if err != nil {
		return nil, err
	}
	if err = r.SaveUsageAnalyticsAggregationState(ctx, &next); err != nil {
		return nil, err
	}
	return &next, nil
}
