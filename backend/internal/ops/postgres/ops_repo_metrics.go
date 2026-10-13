package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// System metrics distinguish an unavailable observation from an observed zero
// through nullable scalar encoding. Most integer metrics, floats, and booleans
// retain zero or false, while disk-size fields use the general integer encoder
// that omits zero. Metric insertion is an ordinary INSERT, and the repository
// schema's minute/window indexes do not make repeated rows unique.
//
// Heartbeat updates combine optional observations under a job-name conflict.
// Missing run, success, and duration fields retain prior values; a supplied
// nonzero success time clears previous error metadata. Conflict updates
// preserve the last successful result during failures, although a first insert
// can store a result with no success timestamp. These records are written
// separately from metric rows, and neither method couples their outcomes in a
// transaction. Latest system reads select a global window row by time without
// an age limit or row-ID tie breaker.

// InsertSystemMetrics writes one supplied metrics row, defaulting nonpositive
// window to one minute and a zero CreatedAt to current UTC time. Store, pool,
// and input are required; a supplied timestamp is forwarded unchanged.
//
// Nullable metric encoders preserve explicit zero, including pool and queue
// counts, and false health values. Disk-size inputs instead use opsNullInt,
// which maps zero to SQL NULL; group identity also omits zero. The plain insert
// has no conflict handler or per-minute deduplication, and job-heartbeat
// persistence is a separate caller operation. Values receive no range or
// finite-number validation in this method.
func (r *Store) InsertSystemMetrics(ctx context.Context, input *ops.OpsInsertSystemMetricsInput) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil ops repository")
	}
	if input == nil {
		return fmt.Errorf("nil input")
	}

	window := input.WindowMinutes
	if window <= 0 {
		window = 1
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	q := `
INSERT INTO ops_system_metrics (
  created_at,
  window_minutes,
  platform,
  group_id,

  success_count,
  error_count_total,
  business_limited_count,
  error_count_sla,

  upstream_error_count_excl_429_529,
  upstream_429_count,
  upstream_529_count,

  token_consumed,
  provider_switch_count,
  qps,
  tps,

  duration_p50_ms,
  duration_p90_ms,
  duration_p95_ms,
  duration_p99_ms,
  duration_avg_ms,
  duration_max_ms,

  ttft_p50_ms,
  ttft_p90_ms,
  ttft_p95_ms,
  ttft_p99_ms,
  ttft_avg_ms,
  ttft_max_ms,

  cpu_usage_percent,
  memory_used_mb,
  memory_total_mb,
  memory_usage_percent,
  disk_used_mb,
  disk_total_mb,
  disk_usage_percent,

  db_ok,
  redis_ok,

  redis_conn_total,
  redis_conn_idle,

  db_conn_active,
  db_conn_idle,
  db_conn_waiting,

  goroutine_count,
  concurrency_queue_depth
) VALUES (
  $1,$2,$3,$4,
  $5,$6,$7,$8,
  $9,$10,$11,
  $12,$13,$14,$15,
  $16,$17,$18,$19,$20,$21,
  $22,$23,$24,$25,$26,$27,
  $28,$29,$30,$31,$32,$33,$34,
  $35,$36,
  $37,$38,
  $39,$40,$41,
  $42,$43
)`

	// Insert each accepted input as its own row; the SQL has no conflict clause,
	// and OpsMetricsCollector records its heartbeat outcome independently
	// afterward.
	_, err := r.db.ExecContext(
		ctx,
		q,
		createdAt,
		window,
		opsNullString(input.Platform),
		opsNullInt64(input.GroupID),

		input.SuccessCount,
		input.ErrorCountTotal,
		input.BusinessLimitedCount,
		input.ErrorCountSLA,

		input.UpstreamErrorCountExcl429529,
		input.Upstream429Count,
		input.Upstream529Count,

		input.TokenConsumed,
		input.ProviderSwitchCount,
		opsNullFloat64(input.QPS),
		opsNullFloat64(input.TPS),

		opsNullMetricInt(input.DurationP50Ms),
		opsNullMetricInt(input.DurationP90Ms),
		opsNullMetricInt(input.DurationP95Ms),
		opsNullMetricInt(input.DurationP99Ms),
		opsNullFloat64(input.DurationAvgMs),
		opsNullMetricInt(input.DurationMaxMs),

		opsNullMetricInt(input.TTFTP50Ms),
		opsNullMetricInt(input.TTFTP90Ms),
		opsNullMetricInt(input.TTFTP95Ms),
		opsNullMetricInt(input.TTFTP99Ms),
		opsNullFloat64(input.TTFTAvgMs),
		opsNullMetricInt(input.TTFTMaxMs),

		opsNullFloat64(input.CPUUsagePercent),
		opsNullMetricInt64(input.MemoryUsedMB),
		opsNullMetricInt64(input.MemoryTotalMB),
		opsNullFloat64(input.MemoryUsagePercent),
		// Disk size uses the general integer encoder, so a supplied zero becomes SQL
		// NULL despite other metric encoders retaining their observed zeros.
		opsNullInt(input.DiskUsedMB),
		opsNullInt(input.DiskTotalMB),
		opsNullFloat64(input.DiskUsagePercent),

		opsNullBool(input.DBOK),
		opsNullBool(input.RedisOK),

		opsNullMetricInt(input.RedisConnTotal),
		opsNullMetricInt(input.RedisConnIdle),

		// Keep valid pool zeros present through the dedicated pointer encoder; the
		// general integer helper would turn those observations into unavailable
		// values.
		opsNullableIntPointer(input.DBConnActive),
		opsNullableIntPointer(input.DBConnIdle),
		opsNullableIntPointer(input.DBConnWaiting),

		opsNullMetricInt(input.GoroutineCount),
		opsNullMetricInt(input.ConcurrencyQueueDepth),
	)
	return err
}

// GetLatestSystemMetrics reads the newest global row for a supplied window
// size, defaulting nonpositive size to one. Only SQL-null platform and group
// dimensions qualify; there is no freshness cutoff or deterministic row-ID tie
// breaker for equal created_at values.
//
// No matching row returns the database error. SQL-null observations remain nil,
// while valid zero and false values become newly allocated scalar pointers.
// Integer connection, goroutine, and queue values narrow to native int without
// range validation; memory/disk sizes and switch counts retain int64. The
// response uses its stored timestamp and does not merge fields from older rows.
func (r *Store) GetLatestSystemMetrics(ctx context.Context, windowMinutes int) (*ops.OpsSystemMetricsSnapshot, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if windowMinutes <= 0 {
		windowMinutes = 1
	}

	q := `
SELECT
  id,
  created_at,
  window_minutes,

  cpu_usage_percent,
  memory_used_mb,
  memory_total_mb,
  memory_usage_percent,
  disk_used_mb,
  disk_total_mb,
  disk_usage_percent,

  db_ok,
  redis_ok,

  redis_conn_total,
  redis_conn_idle,

  db_conn_active,
  db_conn_idle,
  db_conn_waiting,

  goroutine_count,
  concurrency_queue_depth,
  provider_switch_count
FROM ops_system_metrics
WHERE window_minutes = $1
  AND platform IS NULL
  AND group_id IS NULL
ORDER BY created_at DESC
LIMIT 1`

	var out ops.OpsSystemMetricsSnapshot
	var cpu sql.NullFloat64
	var memUsed sql.NullInt64
	var memTotal sql.NullInt64
	var memPct sql.NullFloat64
	var diskUsed sql.NullInt64
	var diskTotal sql.NullInt64
	var diskPct sql.NullFloat64
	var dbOK sql.NullBool
	var redisOK sql.NullBool
	var redisTotal sql.NullInt64
	var redisIdle sql.NullInt64
	var dbActive sql.NullInt64
	var dbIdle sql.NullInt64
	var dbWaiting sql.NullInt64
	var goroutines sql.NullInt64
	var queueDepth sql.NullInt64
	var providerSwitchCount sql.NullInt64

	if err := r.db.QueryRowContext(ctx, q, windowMinutes).Scan(
		&out.ID,
		&out.CreatedAt,
		&out.WindowMinutes,
		&cpu,
		&memUsed,
		&memTotal,
		&memPct,
		&diskUsed,
		&diskTotal,
		&diskPct,
		&dbOK,
		&redisOK,
		&redisTotal,
		&redisIdle,
		&dbActive,
		&dbIdle,
		&dbWaiting,
		&goroutines,
		&queueDepth,
		&providerSwitchCount,
	); err != nil {
		return nil, err
	}

	// Restore valid nullable observations with new scalar pointers so a measured
	// zero or false survives readback while SQL absence remains nil.
	if cpu.Valid {
		v := cpu.Float64
		out.CPUUsagePercent = &v
	}
	if memUsed.Valid {
		v := memUsed.Int64
		out.MemoryUsedMB = &v
	}
	if memTotal.Valid {
		v := memTotal.Int64
		out.MemoryTotalMB = &v
	}
	if memPct.Valid {
		v := memPct.Float64
		out.MemoryUsagePercent = &v
	}
	if diskUsed.Valid {
		v := diskUsed.Int64
		out.DiskUsedMB = &v
	}
	if diskTotal.Valid {
		v := diskTotal.Int64
		out.DiskTotalMB = &v
	}
	if diskPct.Valid {
		v := diskPct.Float64
		out.DiskUsagePercent = &v
	}
	if dbOK.Valid {
		v := dbOK.Bool
		out.DBOK = &v
	}
	if redisOK.Valid {
		v := redisOK.Bool
		out.RedisOK = &v
	}
	// Narrow available connection/runtime counts to native int without a bounds
	// check; other int64 size fields above retain their database width.
	if redisTotal.Valid {
		v := int(redisTotal.Int64)
		out.RedisConnTotal = &v
	}
	if redisIdle.Valid {
		v := int(redisIdle.Int64)
		out.RedisConnIdle = &v
	}
	if dbActive.Valid {
		v := int(dbActive.Int64)
		out.DBConnActive = &v
	}
	if dbIdle.Valid {
		v := int(dbIdle.Int64)
		out.DBConnIdle = &v
	}
	if dbWaiting.Valid {
		v := int(dbWaiting.Int64)
		out.DBConnWaiting = &v
	}
	if goroutines.Valid {
		v := int(goroutines.Int64)
		out.GoroutineCount = &v
	}
	if queueDepth.Valid {
		v := int(queueDepth.Int64)
		out.ConcurrencyQueueDepth = &v
	}
	if providerSwitchCount.Valid {
		v := providerSwitchCount.Int64
		out.ProviderSwitchCount = &v
	}

	return &out, nil
}

// UpsertJobHeartbeat records optional job observations using exact nonempty
// JobName as its conflict identity. Whitespace-only names are accepted here.
// Nil or zero times and blank strings become SQL NULL; a zero duration also
// becomes NULL through the general integer encoder.
//
// On conflict, absent run/success/duration values retain prior observations. A
// nonnull success timestamp clears both error fields and can replace LastResult
// when supplied; a failure retains the stored successful result. A first insert
// stores provided result/error fields directly, so it can differ from
// conflict-update behavior. Timestamps are not compared for monotonic
// advancement, and this write is independent of the job's data changes.
func (r *Store) UpsertJobHeartbeat(ctx context.Context, input *ops.OpsUpsertJobHeartbeatInput) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil ops repository")
	}
	if input == nil {
		return fmt.Errorf("nil input")
	}
	if input.JobName == "" {
		return fmt.Errorf("job_name required")
	}

	// Apply optional fields through job-name conflict rules: success clears
	// errors, missing values retain prior observations, and failure preserves the
	// stored successful result.
	q := `
INSERT INTO ops_job_heartbeats (
  job_name,
  last_run_at,
  last_success_at,
  last_error_at,
  last_error,
  last_duration_ms,
  last_result,
  updated_at
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,NOW()
)
ON CONFLICT (job_name) DO UPDATE SET
  last_run_at = COALESCE(EXCLUDED.last_run_at, ops_job_heartbeats.last_run_at),
  last_success_at = COALESCE(EXCLUDED.last_success_at, ops_job_heartbeats.last_success_at),
  last_error_at = CASE
    WHEN EXCLUDED.last_success_at IS NOT NULL THEN NULL
    ELSE COALESCE(EXCLUDED.last_error_at, ops_job_heartbeats.last_error_at)
  END,
  last_error = CASE
    WHEN EXCLUDED.last_success_at IS NOT NULL THEN NULL
    ELSE COALESCE(EXCLUDED.last_error, ops_job_heartbeats.last_error)
  END,
  last_duration_ms = COALESCE(EXCLUDED.last_duration_ms, ops_job_heartbeats.last_duration_ms),
  last_result = CASE
    WHEN EXCLUDED.last_success_at IS NOT NULL THEN COALESCE(EXCLUDED.last_result, ops_job_heartbeats.last_result)
    ELSE ops_job_heartbeats.last_result
  END,
  updated_at = NOW()`

	_, err := r.db.ExecContext(
		ctx,
		q,
		input.JobName,
		opsNullTime(input.LastRunAt),
		opsNullTime(input.LastSuccessAt),
		opsNullTime(input.LastErrorAt),
		opsNullString(input.LastError),
		opsNullInt(input.LastDurationMs),
		opsNullString(input.LastResult),
	)
	return err
}

// ListJobHeartbeats returns job-name-ordered stored observations with SQL
// absence kept as nil pointers. Query, scan, and cursor errors reject the
// result; newly allocated rows remain usable after the cursor closes.
func (r *Store) ListJobHeartbeats(ctx context.Context) ([]*ops.OpsJobHeartbeat, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}

	q := `
SELECT
  job_name,
  last_run_at,
  last_success_at,
  last_error_at,
  last_error,
  last_duration_ms,
  last_result,
  updated_at
FROM ops_job_heartbeats
ORDER BY job_name ASC`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	// Close the heartbeat cursor after scanning or early error; close failures are
	// suppressed while scan and iteration errors still reject the returned list.
	defer func() { _ = rows.Close() }()

	out := make([]*ops.OpsJobHeartbeat, 0, 8)
	for rows.Next() {
		var item ops.OpsJobHeartbeat
		var lastRun sql.NullTime
		var lastSuccess sql.NullTime
		var lastErrorAt sql.NullTime
		var lastError sql.NullString
		var lastDuration sql.NullInt64

		var lastResult sql.NullString

		if err := rows.Scan(
			&item.JobName,
			&lastRun,
			&lastSuccess,
			&lastErrorAt,
			&lastError,
			&lastDuration,
			&lastResult,
			&item.UpdatedAt,
		); err != nil {
			return nil, err
		}

		if lastRun.Valid {
			v := lastRun.Time
			item.LastRunAt = &v
		}
		if lastSuccess.Valid {
			v := lastSuccess.Time
			item.LastSuccessAt = &v
		}
		if lastErrorAt.Valid {
			v := lastErrorAt.Time
			item.LastErrorAt = &v
		}
		if lastError.Valid {
			v := lastError.String
			item.LastError = &v
		}
		if lastDuration.Valid {
			v := lastDuration.Int64
			item.LastDurationMs = &v
		}
		if lastResult.Valid {
			v := lastResult.String
			item.LastResult = &v
		}

		out = append(out, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// opsNullBool returns SQL NULL for nil and a valid nullable boolean otherwise.
// Explicit false stays present, letting health writes distinguish a failed
// check from an unavailable check.
func opsNullBool(v *bool) any {
	if v == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *v, Valid: true}
}

// opsNullFloat64 returns SQL NULL for nil and copies a supplied float into a
// valid nullable value. Zero remains present, and no finite or range validation
// is performed here.
func opsNullFloat64(v *float64) any {
	if v == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *v, Valid: true}
}

// opsNullMetricInt returns SQL NULL for nil and converts a present native int
// to valid int64 storage. Zero and negative values remain present; this encoder
// performs no metric-value validation.
func opsNullMetricInt(v *int) any {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

// opsNullMetricInt64 returns SQL NULL for nil and a valid copied int64
// otherwise. Explicit zero is retained, allowing empty memory/count
// observations to remain distinct from missing measurements.
func opsNullMetricInt64(v *int64) any {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

// opsNullTime returns SQL NULL for nil or a zero Time and otherwise copies the
// supplied timestamp into a valid nullable value. Its location and instant are
// retained without UTC normalization.
func opsNullTime(v *time.Time) any {
	if v == nil || v.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *v, Valid: true}
}
