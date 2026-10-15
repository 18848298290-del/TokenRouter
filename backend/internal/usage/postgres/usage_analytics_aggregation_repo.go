package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// usage_analytics_aggregation_repo.go rebuilds multidimensional usage rollups
// and publishes field-specific aggregation progress.
// Usage analytics combines normalized dimensions with UTC hour and day buckets.
// Live rebuilding clears every affected hourly row but reads the last hour only
// through the supplied watermark. Cleanup uses an inclusive end and rescans
// that complete final hour, retaining surviving requests outside the deletion
// range. Daily rows are rebuilt from hourly sums with matching dimension
// identities and nonnull-duration counts.
//
// Rebuilding replaces rows before inserting current data so empty or changed
// dimension groups disappear. A *sql.DB creates a transaction around those
// phases; supplied executors retain caller-owned transaction behavior.
// ApplyUsageAnalyticsState uses a separate, short row-lock transaction and
// merges fields by change kind, keeping a manual backfill request from
// overwriting concurrent live progress.

const (
	// usageAnalyticsRawDimensionsSQL supplies the ordered dimensions shared by
	// hourly insertion and combined raw queries; callers alias usage_logs as ul.
	// Missing payer IDs use the actor, absent team/group IDs become zero, and the
	// model prefers trimmed model then requested_model.
	//
	// Nonzero request types win over legacy websocket and stream flags. An
	// existing nonempty billing mode wins over inferred video, image, then token
	// mode. Platform remains the recorded snapshot, so provider metadata changes
	// cannot relabel existing usage.
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

// AggregateUsageAnalyticsRange refreshes UTC hourly rows and the affected daily
// rows from their sums. It scans raw data through the exclusive end, and nil
// storage or a nonincreasing range performs no work.
func (r *AggregationStore) AggregateUsageAnalyticsRange(ctx context.Context, start, end time.Time) error {
	return r.aggregateUsageAnalyticsRange(ctx, start, end, false, true)
}

// AggregateUsageAnalyticsHourlyRange refreshes affected UTC hourly rows through
// the exclusive end. Daily rows remain for a separate rebuild phase, and nil
// storage or nonincreasing ranges return successfully.
func (r *AggregationStore) AggregateUsageAnalyticsHourlyRange(ctx context.Context, start, end time.Time) error {
	return r.aggregateUsageAnalyticsRange(ctx, start, end, false, false)
}

// RecomputeUsageAnalyticsRange refreshes UTC hourly and daily rows after an
// inclusive cleanup range. It includes the hour beginning at an exact end and
// scans that complete hour to retain surviving requests.
func (r *AggregationStore) RecomputeUsageAnalyticsRange(ctx context.Context, start, end time.Time) error {
	return r.aggregateUsageAnalyticsRange(ctx, start, end, true, true)
}

// aggregateUsageAnalyticsRange selects affected UTC hours and the raw scan
// endpoint for live work or cleanup repair. Nil storage and nonincreasing
// ranges return successfully. Live work excludes an exact end hour and scans
// only through end, while includeEndBucket adds that hour and scans its
// complete contents.
//
// Deleting entire affected hourly groups allows vanished dimensions to
// disappear. A database handle wraps deletion, insertion, and optional daily
// rebuilding in one transaction; supplied executors keep their own transaction
// ownership. The daily flag lets backfill defer day rebuilding until its hour
// blocks are ready.
func (r *AggregationStore) aggregateUsageAnalyticsRange(ctx context.Context, start, end time.Time, includeEndBucket, rebuildDaily bool) error {
	if r == nil || r.sql == nil || !end.After(start) {
		return nil
	}
	// Select whole UTC bucket starts for replacement even when raw endpoints
	// fall inside hours; the scan endpoint below distinguishes live partial work.
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
		// Cleanup removes records at end as well as before it. Rescan the complete
		// last hour so rebuilding keeps its surviving later records.
		scanEnd = hourEnd
	}
	// Keep the raw scan inside the hourly replacement range.
	if scanEnd.After(hourEnd) {
		scanEnd = hourEnd
	}
	// Use one database transaction for each replacement sequence; caller-supplied
	// executors must provide the transaction holding their own delete and insert
	// together.
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := NewAggregationStoreWithSQL(tx, r.calendar)
		if err := txRepo.aggregateUsageAnalyticsRangeInTx(ctx, hourStart, hourEnd, scanEnd, rebuildDaily); err != nil {
			// Roll back the earlier replacement phases after a write fails; return that
			// write error as the operation result even if rollback fails.
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.aggregateUsageAnalyticsRangeInTx(ctx, hourStart, hourEnd, scanEnd, rebuildDaily)
}

// aggregateUsageAnalyticsRangeInTx replaces hourly dimensions from raw
// [hourStart, scanEnd) records and optionally rebuilds their UTC dates. The
// caller supplies rounded hour bounds and a transaction when replacement must
// commit together. Deleting the affected range first removes dimension groups
// whose raw requests disappeared.
//
// The shared dimension expression gives raw readers and stored rows the same
// legacy normalization. duration_count counts nonnull values separately from
// the duration sum. Daily rebuilding includes dates containing replaced hours,
// using the instant before the exclusive hourEnd to avoid adding an untouched
// next date.
func (r *AggregationStore) aggregateUsageAnalyticsRangeInTx(ctx context.Context, hourStart, hourEnd, scanEnd time.Time, rebuildDaily bool) error {
	// Delete the affected hourly groups before insertion so requests removed by
	// cleanup cannot leave stale totals or obsolete dimension rows behind.
	if _, err := r.sql.ExecContext(ctx, `
		DELETE FROM usage_analytics_hourly
		WHERE bucket_start >= $1 AND bucket_start < $2
	`, hourStart, hourEnd); err != nil {
		return err
	}

	// Raw and rollup query rows share this dimension expression; normalization at
	// insertion keeps the same request from acquiring different identities across
	// data sources.
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
	// hourEnd is exclusive; subtract one nanosecond to select the last affected
	// date without extending daily rebuilding into the next untouched day.
	lastHour := hourEnd.Add(-time.Nanosecond)
	dayEnd := truncateUsageAnalyticsDay(lastHour).AddDate(0, 0, 1)
	return r.rebuildUsageAnalyticsDailyRangeInTx(ctx, dayStart, dayEnd)
}

// RebuildUsageAnalyticsDailyRange replaces complete UTC days touched by [start,
// end) using hourly data. A partial final day expands to the following
// midnight, while an exact midnight remains exclusive. Nil storage and
// nonincreasing ranges return successfully.
//
// The database-backed path starts a transaction so deletion and reinsertion
// commit together. A failed phase requests rollback and returns its error; a
// supplied executor retains caller-owned completion. This method reads existing
// hourly rows, so callers finish the relevant hourly work before rebuilding
// their daily summaries.
func (r *AggregationStore) RebuildUsageAnalyticsDailyRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil || !end.After(start) {
		return nil
	}
	// Daily rows contain whole UTC dates; round touched dates outward so a
	// partial endpoint cannot leave its earlier daily total in place.
	dayStart := truncateUsageAnalyticsDay(start)
	dayEnd := truncateUsageAnalyticsDay(end)
	if end.UTC().After(dayEnd) {
		dayEnd = dayEnd.AddDate(0, 0, 1)
	}
	if !dayEnd.After(dayStart) {
		return nil
	}
	// Use one database transaction for each replacement sequence; caller-supplied
	// executors must provide the transaction holding their own delete and insert
	// together.
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := NewAggregationStoreWithSQL(tx, r.calendar)
		if err := txRepo.rebuildUsageAnalyticsDailyRangeInTx(ctx, dayStart, dayEnd); err != nil {
			// Roll back the earlier replacement phases after a write fails; return that
			// write error as the operation result even if rollback fails.
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.rebuildUsageAnalyticsDailyRangeInTx(ctx, dayStart, dayEnd)
}

// rebuildUsageAnalyticsDailyRangeInTx replaces daily dimensions from hourly
// rows in [dayStart, dayEnd). Its caller supplies UTC day bounds and any
// transaction containing the delete and insert. Removing old daily rows first
// clears groups that no longer exist in the hourly source.
//
// The insert keeps every stored dimension and adds requests, tokens, costs,
// duration sums, and duration counts across hours. Empty days remain empty
// after deletion. Query errors stop replacement before later work, leaving
// rollback or partial-commit handling to the supplied executor's owner.
func (r *AggregationStore) rebuildUsageAnalyticsDailyRangeInTx(ctx context.Context, dayStart, dayEnd time.Time) error {
	// Replace whole daily groups from the hourly source; deletion also removes a
	// previous group when no surviving hourly row can recreate it.
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

// truncateUsageAnalyticsDay returns midnight for the UTC date containing
// value's instant. Its result supplies multidimensional daily bounds
// independently of the Calendar timezone used by dashboard aggregation.
func truncateUsageAnalyticsDay(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

// GetUsageAnalyticsAggregationState reads the singleton progress row with
// optional timestamps represented by pointers. Timestamp values are converted
// to UTC, SQL NULL becomes nil, and a missing singleton remains a query error.
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

// SaveUsageAnalyticsAggregationState replaces every persisted field of the
// singleton progress row; nil state performs no work. Callers coordinate full
// replacement themselves or use ApplyUsageAnalyticsState to merge a field-owned
// change under the row lock.
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

// GetOldestUsageLogTime returns the earliest retained raw timestamp in UTC. An
// empty table produces nil through the scalar subquery's SQL NULL; scan and
// result cleanup failures propagate.
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

// CleanupUsageAnalytics deletes hourly rows before hourlyCutoff, then daily
// rows before dailyCutoff's SQL date. It starts no transaction; when the daily
// deletion fails, the executor's transaction scope determines whether the
// earlier hourly deletion remains.
func (r *AggregationStore) CleanupUsageAnalytics(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	// Hourly and daily rollups have different configured retention lifetimes.
	// Apply their independent cutoffs on the supplied executor, stopping the
	// sequence if hourly cleanup fails.
	if _, err := r.sql.ExecContext(ctx, `DELETE FROM usage_analytics_hourly WHERE bucket_start < $1`, hourlyCutoff.UTC()); err != nil {
		return err
	}
	_, err := r.sql.ExecContext(ctx, `DELETE FROM usage_analytics_daily WHERE bucket_date < $1::date`, dailyCutoff.UTC())
	return err
}

// nullableTimePointer converts a valid SQL timestamp into a new UTC time
// pointer. SQL NULL returns nil, letting progress readers distinguish an absent
// cursor from a stored zero timestamp.
func nullableTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

// ApplyUsageAnalyticsState applies a change kind to the latest locked singleton
// progress row and returns the merged state after successful persistence. A
// *sql.DB supplies a short transaction containing the row lock, read, merge,
// and save. Aggregation scans run separately from this state transaction.
//
// Callers supplying another executor provide its transaction so the lock
// remains held through saving. Superseded manual progress returns its
// comparison error before saving. The merged result can retain timestamp
// pointers from change.State, and an unsuccessful commit returns an error
// instead of the candidate state.
func (r *AggregationStore) ApplyUsageAnalyticsState(ctx context.Context, change usage.AnalyticsStateChange) (*usage.UsageAnalyticsAggregationState, error) {
	// Hold the singleton lock through read, merge, and save in this short
	// transaction so field-owned writers operate on the latest committed request.
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		// An early lock, merge, or save error leaves the transaction open. Deferred
		// rollback releases it while the primary operation error remains returned.
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

// applyUsageAnalyticsStateInTx locks and reads the singleton before merging
// fields owned by change.Kind. The caller supplies a transaction keeping FOR
// UPDATE effective through the save. A missing singleton or rejected manual
// target/cursor comparison ends the operation before replacement.
//
// Reading after the lock gives the merge current live and manual fields.
// SaveUsageAnalyticsAggregationState then replaces the whole row using that
// merged value, retaining fields belonging to other flows. The returned state
// can share timestamp pointers with change.State; transaction completion
// belongs to the caller.
func (r *AggregationStore) applyUsageAnalyticsStateInTx(ctx context.Context, change usage.AnalyticsStateChange) (*usage.UsageAnalyticsAggregationState, error) {
	var id int64
	// Acquire the singleton lock before reading field ownership inputs so another
	// state writer cannot replace the observed manual target during this
	// transaction.
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
