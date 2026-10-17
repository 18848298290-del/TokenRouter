package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ops_repo_preagg.go refreshes hourly and daily Ops aggregates from request
// observations and saved hourly summaries.
// Hourly Ops metrics retain separate global, platform, and platform-plus-group
// rows. Usage observations supply success, token, and latency measurements;
// saved error observations supply classification counts after count-token
// probes are excluded. Null dimensions identify broader summaries, and the
// migration's coalesced unique index makes those summaries addressable by the
// conflict target.
//
// Hourly refresh replaces values for dimensions present in the scanned range,
// then independently inserts missing global zero buckets so dashboard coverage
// can distinguish empty hours from unprocessed hours. An error in the second
// statement can leave the first committed, and conflict suppression on the zero
// insert keeps existing values. Daily refresh combines saved hourly summaries:
// low percentiles and averages use weights, while high percentiles use maxima.
// These estimates cannot reconstruct the original request-level percentile
// distribution.

// UpsertHourlyMetrics recomputes hourly summaries for a half-open
// raw-observation range using supplied ignored status codes. Zero or unordered
// endpoints return successfully without writes; the aggregation service
// supplies aligned UTC hours so the later coverage buckets match the scanned
// range. Input times are converted to UTC but are not rounded here.
//
// The first statement replaces metrics for observed global, platform, and
// platform-plus-group dimensions through the migration's coalesced unique
// index. Error observations exclude count-token probes and use the shared
// business/SLA classification rules; final-error totals require client failure,
// while eligible recovered provider attempts can contribute upstream counts.
// The second statement inserts missing global zero buckets independently. Its
// failure leaves the completed first write committed, and ON CONFLICT DO
// NOTHING retains earlier values for an already existing zero-insert target;
// dimensions absent from the new observations are not deleted.
func (r *Store) UpsertHourlyMetrics(ctx context.Context, startTime, endTime time.Time, ignoredStatusCodes []int) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil ops repository")
	}
	if startTime.IsZero() || endTime.IsZero() || !endTime.After(startTime) {
		return nil
	}

	start := startTime.UTC()
	end := endTime.UTC()
	businessLimitedSQL := opsBusinessLimitedSQL("client_status_code", "upstream_status_code", "upstream_errors", "error_owner", "is_business_limited", ignoredStatusCodes)
	slaCountableSQL := opsSLACountableSQL("client_status_code", "upstream_status_code", "upstream_errors", "error_owner", "is_business_limited", ignoredStatusCodes)

	// GROUPING SETS computes global, platform, and platform-plus-group rows from
	// the same usage and error inputs. The conflict expressions match
	// migration033's unique index, which maps null dimensions to empty platform
	// and zero group. Percentiles and maxima become integer milliseconds while
	// average columns retain fractional values.
	q := `
WITH usage_base AS (
  SELECT
    date_trunc('hour', ul.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket_start,
    ul.platform AS platform,
    ul.group_id AS group_id,
    ul.duration_ms AS duration_ms,
    ` + usageTTFTSQL("ul") + ` AS ttft_ms,
    (ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens) AS tokens
  FROM usage_logs ul
  WHERE ul.created_at >= $1 AND ul.created_at < $2
),
usage_agg AS (
  SELECT
    bucket_start,
    CASE WHEN GROUPING(platform) = 1 THEN NULL ELSE platform END AS platform,
    CASE WHEN GROUPING(group_id) = 1 THEN NULL ELSE group_id END AS group_id,
    COUNT(*) AS success_count,
    COUNT(*) FILTER (WHERE ttft_ms IS NOT NULL) AS ttft_sample_count,
    COALESCE(SUM(tokens), 0) AS token_consumed,

    percentile_cont(0.50) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms IS NOT NULL) AS duration_p50_ms,
    percentile_cont(0.90) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms IS NOT NULL) AS duration_p90_ms,
    percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms IS NOT NULL) AS duration_p95_ms,
    percentile_cont(0.99) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms IS NOT NULL) AS duration_p99_ms,
    AVG(duration_ms) FILTER (WHERE duration_ms IS NOT NULL) AS duration_avg_ms,
    MAX(duration_ms) AS duration_max_ms,

    percentile_cont(0.50) WITHIN GROUP (ORDER BY ttft_ms) FILTER (WHERE ttft_ms IS NOT NULL) AS ttft_p50_ms,
    percentile_cont(0.90) WITHIN GROUP (ORDER BY ttft_ms) FILTER (WHERE ttft_ms IS NOT NULL) AS ttft_p90_ms,
    percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms) FILTER (WHERE ttft_ms IS NOT NULL) AS ttft_p95_ms,
    percentile_cont(0.99) WITHIN GROUP (ORDER BY ttft_ms) FILTER (WHERE ttft_ms IS NOT NULL) AS ttft_p99_ms,
    AVG(ttft_ms) FILTER (WHERE ttft_ms IS NOT NULL) AS ttft_avg_ms,
    MAX(ttft_ms) AS ttft_max_ms
  FROM usage_base
  GROUP BY GROUPING SETS (
    (bucket_start),
    (bucket_start, platform),
    (bucket_start, platform, group_id)
  )
),
error_base AS (
  SELECT
    date_trunc('hour', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket_start,
    -- platform is NULL for some early-phase errors (e.g. before routing); map to a sentinel
    -- value so platform-level GROUPING SETS don't collide with the overall (platform=NULL) row.
    COALESCE(platform, 'unknown') AS platform,
    group_id AS group_id,
    is_business_limited AS is_business_limited,
    error_owner AS error_owner,
    status_code AS client_status_code,
    upstream_status_code AS upstream_status_code,
    upstream_errors AS upstream_errors,
    COALESCE(upstream_status_code, status_code, 0) AS effective_status_code
  FROM ops_error_logs
  -- Exclude count_tokens requests from error metrics as they are informational probes
  WHERE created_at >= $1 AND created_at < $2
    AND is_count_tokens = FALSE
),
error_agg AS (
  SELECT
    bucket_start,
    CASE WHEN GROUPING(platform) = 1 THEN NULL ELSE platform END AS platform,
    CASE WHEN GROUPING(group_id) = 1 THEN NULL ELSE group_id END AS group_id,
    COUNT(*) FILTER (WHERE COALESCE(client_status_code, 0) >= 400) AS error_count_total,
    COUNT(*) FILTER (WHERE COALESCE(client_status_code, 0) >= 400 AND ` + businessLimitedSQL + `) AS business_limited_count,
    COUNT(*) FILTER (WHERE COALESCE(client_status_code, 0) >= 400 AND ` + slaCountableSQL + `) AS error_count_sla,
    COUNT(*) FILTER (WHERE error_owner = 'provider' AND ` + slaCountableSQL + ` AND COALESCE(effective_status_code, 0) NOT IN (429, 529)) AS upstream_error_count_excl_429_529,
    COUNT(*) FILTER (WHERE error_owner = 'provider' AND ` + slaCountableSQL + ` AND COALESCE(effective_status_code, 0) = 429) AS upstream_429_count,
    COUNT(*) FILTER (WHERE error_owner = 'provider' AND ` + slaCountableSQL + ` AND COALESCE(effective_status_code, 0) = 529) AS upstream_529_count
  FROM error_base
  GROUP BY GROUPING SETS (
    (bucket_start),
    (bucket_start, platform),
    (bucket_start, platform, group_id)
  )
  HAVING GROUPING(group_id) = 1 OR group_id IS NOT NULL
),
combined AS (
  SELECT
    COALESCE(u.bucket_start, e.bucket_start) AS bucket_start,
    COALESCE(u.platform, e.platform) AS platform,
    COALESCE(u.group_id, e.group_id) AS group_id,

    COALESCE(u.success_count, 0) AS success_count,
    COALESCE(u.ttft_sample_count, 0) AS ttft_sample_count,
    COALESCE(e.error_count_total, 0) AS error_count_total,
    COALESCE(e.business_limited_count, 0) AS business_limited_count,
    COALESCE(e.error_count_sla, 0) AS error_count_sla,
    COALESCE(e.upstream_error_count_excl_429_529, 0) AS upstream_error_count_excl_429_529,
    COALESCE(e.upstream_429_count, 0) AS upstream_429_count,
    COALESCE(e.upstream_529_count, 0) AS upstream_529_count,

    COALESCE(u.token_consumed, 0) AS token_consumed,

    u.duration_p50_ms,
    u.duration_p90_ms,
    u.duration_p95_ms,
    u.duration_p99_ms,
    u.duration_avg_ms,
    u.duration_max_ms,

    u.ttft_p50_ms,
    u.ttft_p90_ms,
    u.ttft_p95_ms,
    u.ttft_p99_ms,
    u.ttft_avg_ms,
    u.ttft_max_ms
  FROM usage_agg u
  FULL OUTER JOIN error_agg e
    ON u.bucket_start = e.bucket_start
   AND COALESCE(u.platform, '') = COALESCE(e.platform, '')
   AND COALESCE(u.group_id, 0) = COALESCE(e.group_id, 0)
)
INSERT INTO ops_metrics_hourly (
  bucket_start,
  platform,
  group_id,
  success_count,
  ttft_sample_count,
  error_count_total,
  business_limited_count,
  error_count_sla,
  upstream_error_count_excl_429_529,
  upstream_429_count,
  upstream_529_count,
  token_consumed,
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
  computed_at
)
SELECT
  bucket_start,
  NULLIF(platform, '') AS platform,
  group_id,
  success_count,
  ttft_sample_count,
  error_count_total,
  business_limited_count,
  error_count_sla,
  upstream_error_count_excl_429_529,
  upstream_429_count,
  upstream_529_count,
  token_consumed,
  duration_p50_ms::int,
  duration_p90_ms::int,
  duration_p95_ms::int,
  duration_p99_ms::int,
  duration_avg_ms,
  duration_max_ms::int,
  ttft_p50_ms::int,
  ttft_p90_ms::int,
  ttft_p95_ms::int,
  ttft_p99_ms::int,
  ttft_avg_ms,
  ttft_max_ms::int,
  NOW()
FROM combined
WHERE bucket_start IS NOT NULL
  AND (platform IS NULL OR platform <> '')
ON CONFLICT (bucket_start, COALESCE(platform, ''), COALESCE(group_id, 0)) DO UPDATE SET
  success_count = EXCLUDED.success_count,
  ttft_sample_count = EXCLUDED.ttft_sample_count,
  error_count_total = EXCLUDED.error_count_total,
  business_limited_count = EXCLUDED.business_limited_count,
  error_count_sla = EXCLUDED.error_count_sla,
  upstream_error_count_excl_429_529 = EXCLUDED.upstream_error_count_excl_429_529,
  upstream_429_count = EXCLUDED.upstream_429_count,
  upstream_529_count = EXCLUDED.upstream_529_count,
  token_consumed = EXCLUDED.token_consumed,

  duration_p50_ms = EXCLUDED.duration_p50_ms,
  duration_p90_ms = EXCLUDED.duration_p90_ms,
  duration_p95_ms = EXCLUDED.duration_p95_ms,
  duration_p99_ms = EXCLUDED.duration_p99_ms,
  duration_avg_ms = EXCLUDED.duration_avg_ms,
  duration_max_ms = EXCLUDED.duration_max_ms,

  ttft_p50_ms = EXCLUDED.ttft_p50_ms,
  ttft_p90_ms = EXCLUDED.ttft_p90_ms,
  ttft_p95_ms = EXCLUDED.ttft_p95_ms,
  ttft_p99_ms = EXCLUDED.ttft_p99_ms,
  ttft_avg_ms = EXCLUDED.ttft_avg_ms,
  ttft_max_ms = EXCLUDED.ttft_max_ms,

  computed_at = NOW()
`

	// Finish the observed-dimension UPSERT before adding empty-hour markers. The
	// statements commit independently, so marker failure does not roll back
	// refreshed metrics.
	if _, err := r.db.ExecContext(ctx, q, start, end); err != nil {
		return err
	}
	// Dashboard coverage counts global rows for every full hour. Insert missing
	// markers after refresh; conflicting existing metrics remain unchanged even
	// when this scan found no observations.
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO ops_metrics_hourly (bucket_start, platform, group_id)
		SELECT bucket_start, NULL, NULL
		FROM generate_series($1::timestamptz, $2::timestamptz - INTERVAL '1 hour', INTERVAL '1 hour') AS bucket_start
		ON CONFLICT DO NOTHING
	`, start, end)
	return err
}

// UpsertDailyMetrics recomputes saved daily summaries from hourly rows in the
// supplied half-open range. Zero or unordered endpoints return successfully
// without writing. The aggregation service supplies whole UTC days; this method
// converts times to UTC but groups every included hour by its UTC date even
// when a direct caller supplies a partial day.
//
// Counts and tokens are summed, duration low percentiles and averages use
// success counts, and TTFT measurements use nonnull sample counts.
// Missing component statistics are excluded from their own denominators.
// Duration weights include every success in a contributing hour despite missing
// request durations, and maxima are selected independently of weights. High
// percentiles and maxima use the greatest available hourly value, so daily
// percentile values are approximations rather than exact request-level
// quantiles. One statement replaces each observed date/dimension through the
// coalesced unique index; previously stored dimensions with no included hourly
// rows remain untouched.
func (r *Store) UpsertDailyMetrics(ctx context.Context, startTime, endTime time.Time) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("nil ops repository")
	}
	if startTime.IsZero() || endTime.IsZero() || !endTime.After(startTime) {
		return nil
	}

	start := startTime.UTC()
	end := endTime.UTC()

	q := `
INSERT INTO ops_metrics_daily (
  bucket_date,
  platform,
  group_id,
  success_count,
  ttft_sample_count,
  error_count_total,
  business_limited_count,
  error_count_sla,
  upstream_error_count_excl_429_529,
  upstream_429_count,
  upstream_529_count,
  token_consumed,
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
  computed_at
)
SELECT
  (bucket_start AT TIME ZONE 'UTC')::date AS bucket_date,
  platform,
  group_id,

  COALESCE(SUM(success_count), 0) AS success_count,
  COALESCE(SUM(ttft_sample_count), 0) AS ttft_sample_count,
  COALESCE(SUM(error_count_total), 0) AS error_count_total,
  COALESCE(SUM(business_limited_count), 0) AS business_limited_count,
  COALESCE(SUM(error_count_sla), 0) AS error_count_sla,
  COALESCE(SUM(upstream_error_count_excl_429_529), 0) AS upstream_error_count_excl_429_529,
  COALESCE(SUM(upstream_429_count), 0) AS upstream_429_count,
  COALESCE(SUM(upstream_529_count), 0) AS upstream_529_count,
  COALESCE(SUM(token_consumed), 0) AS token_consumed,

  -- 近似合并：p50/p90/avg 使用加权平均，p95/p99/max 保守使用最大值。
  ROUND(SUM(duration_p50_ms::double precision * success_count) FILTER (WHERE duration_p50_ms IS NOT NULL)
    / NULLIF(SUM(success_count) FILTER (WHERE duration_p50_ms IS NOT NULL), 0))::int AS duration_p50_ms,
  ROUND(SUM(duration_p90_ms::double precision * success_count) FILTER (WHERE duration_p90_ms IS NOT NULL)
    / NULLIF(SUM(success_count) FILTER (WHERE duration_p90_ms IS NOT NULL), 0))::int AS duration_p90_ms,
  MAX(duration_p95_ms) AS duration_p95_ms,
  MAX(duration_p99_ms) AS duration_p99_ms,
  SUM(duration_avg_ms * success_count) FILTER (WHERE duration_avg_ms IS NOT NULL)
    / NULLIF(SUM(success_count) FILTER (WHERE duration_avg_ms IS NOT NULL), 0) AS duration_avg_ms,
  MAX(duration_max_ms) AS duration_max_ms,

  -- 首 token 延迟只在流式请求记录，需按 ttft_sample_count 加权，不能按全部成功请求数加权。
  ROUND(SUM(ttft_p50_ms::double precision * ttft_sample_count) FILTER (WHERE ttft_p50_ms IS NOT NULL)
    / NULLIF(SUM(ttft_sample_count) FILTER (WHERE ttft_p50_ms IS NOT NULL), 0))::int AS ttft_p50_ms,
  ROUND(SUM(ttft_p90_ms::double precision * ttft_sample_count) FILTER (WHERE ttft_p90_ms IS NOT NULL)
    / NULLIF(SUM(ttft_sample_count) FILTER (WHERE ttft_p90_ms IS NOT NULL), 0))::int AS ttft_p90_ms,
  MAX(ttft_p95_ms) AS ttft_p95_ms,
  MAX(ttft_p99_ms) AS ttft_p99_ms,
  SUM(ttft_avg_ms * ttft_sample_count) FILTER (WHERE ttft_avg_ms IS NOT NULL)
    / NULLIF(SUM(ttft_sample_count) FILTER (WHERE ttft_avg_ms IS NOT NULL), 0) AS ttft_avg_ms,
  MAX(ttft_max_ms) AS ttft_max_ms,

  NOW()
FROM ops_metrics_hourly
WHERE bucket_start >= $1 AND bucket_start < $2
GROUP BY 1, 2, 3
ON CONFLICT (bucket_date, COALESCE(platform, ''), COALESCE(group_id, 0)) DO UPDATE SET
  success_count = EXCLUDED.success_count,
  ttft_sample_count = EXCLUDED.ttft_sample_count,
  error_count_total = EXCLUDED.error_count_total,
  business_limited_count = EXCLUDED.business_limited_count,
  error_count_sla = EXCLUDED.error_count_sla,
  upstream_error_count_excl_429_529 = EXCLUDED.upstream_error_count_excl_429_529,
  upstream_429_count = EXCLUDED.upstream_429_count,
  upstream_529_count = EXCLUDED.upstream_529_count,
  token_consumed = EXCLUDED.token_consumed,

  duration_p50_ms = EXCLUDED.duration_p50_ms,
  duration_p90_ms = EXCLUDED.duration_p90_ms,
  duration_p95_ms = EXCLUDED.duration_p95_ms,
  duration_p99_ms = EXCLUDED.duration_p99_ms,
  duration_avg_ms = EXCLUDED.duration_avg_ms,
  duration_max_ms = EXCLUDED.duration_max_ms,

  ttft_p50_ms = EXCLUDED.ttft_p50_ms,
  ttft_p90_ms = EXCLUDED.ttft_p90_ms,
  ttft_p95_ms = EXCLUDED.ttft_p95_ms,
  ttft_p99_ms = EXCLUDED.ttft_p99_ms,
  ttft_avg_ms = EXCLUDED.ttft_avg_ms,
  ttft_max_ms = EXCLUDED.ttft_max_ms,

  computed_at = NOW()
`

	// Replace each observed daily dimension in one statement. Hourly metrics are
	// read as saved, so this calculation inherits their coverage and approximated
	// percentile values.
	_, err := r.db.ExecContext(ctx, q, start, end)
	return err
}

// GetLatestHourlyBucketStart returns the greatest stored hourly timestamp
// across all dimensions, converted to UTC. Empty storage returns false without
// error; the aggregation service uses this value as a resume hint, while
// dashboard coverage checks global buckets separately.
func (r *Store) GetLatestHourlyBucketStart(ctx context.Context) (time.Time, bool, error) {
	if r == nil || r.db == nil {
		return time.Time{}, false, fmt.Errorf("nil ops repository")
	}

	var value sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT MAX(bucket_start) FROM ops_metrics_hourly`).Scan(&value); err != nil {
		return time.Time{}, false, err
	}
	if !value.Valid {
		return time.Time{}, false, nil
	}
	return value.Time.UTC(), true, nil
}

// GetLatestDailyBucketDate returns the greatest saved daily date across all
// dimensions as UTC midnight. Empty storage returns false without error; the
// aggregation service uses this result as a resume hint rather than a
// continuous historical coverage marker.
func (r *Store) GetLatestDailyBucketDate(ctx context.Context) (time.Time, bool, error) {
	if r == nil || r.db == nil {
		return time.Time{}, false, fmt.Errorf("nil ops repository")
	}

	var value sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT MAX(bucket_date) FROM ops_metrics_daily`).Scan(&value); err != nil {
		return time.Time{}, false, err
	}
	if !value.Valid {
		return time.Time{}, false, nil
	}
	t := value.Time.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), true, nil
}
