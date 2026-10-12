package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// MetricsQueries reads business tables for monitoring samples.
type MetricsQueries struct {
	*Advisory
	db *sql.DB
}

func (c *MetricsQueries) QueryProviderSwitchCount(ctx context.Context, start, end time.Time) (int64, error) {
	q := `
SELECT
  COALESCE(SUM(CASE
    WHEN split_part(ev->>'kind', ':', 1) IN ('failover', 'retry_exhausted_failover', 'failover_on_400') THEN 1
    ELSE 0
  END), 0) AS switch_count
FROM ops_error_logs o
CROSS JOIN LATERAL jsonb_array_elements(
  COALESCE(NULLIF(o.upstream_errors, 'null'::jsonb), '[]'::jsonb)
) AS ev
WHERE o.created_at >= $1 AND o.created_at < $2
  AND o.is_count_tokens = FALSE`

	var count int64
	if err := c.db.QueryRowContext(ctx, q, start, end).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (c *MetricsQueries) QueryErrorCounts(ctx context.Context, start, end time.Time, ignoredStatusCodes []int) (
	errorTotal int64,
	businessLimited int64,
	errorSLA int64,
	upstreamExcl429529 int64,
	upstream429 int64,
	upstream529 int64,
	err error,
) {
	const where = "WHERE created_at >= $1 AND created_at < $2 AND is_count_tokens = FALSE"
	return queryOpsErrorCounts(ctx, c.db, where, []any{start, end}, ignoredStatusCodes)
}

func queryOpsErrorCounts(ctx context.Context, db *sql.DB, where string, args []any, ignoredStatusCodes []int) (
	errorTotal int64,
	businessLimited int64,
	errorCountSLA int64,
	upstreamExcl429529 int64,
	upstream429 int64,
	upstream529 int64,
	err error,
) {
	businessLimitedSQL := opsBusinessLimitedSQL("status_code", "upstream_status_code", "upstream_errors", "error_owner", "is_business_limited", ignoredStatusCodes)
	slaCountableSQL := opsSLACountableSQL("status_code", "upstream_status_code", "upstream_errors", "error_owner", "is_business_limited", ignoredStatusCodes)

	q := `
SELECT
  COALESCE(COUNT(*) FILTER (WHERE COALESCE(status_code, 0) >= 400), 0) AS error_total,
  COALESCE(COUNT(*) FILTER (WHERE COALESCE(status_code, 0) >= 400 AND ` + businessLimitedSQL + `), 0) AS business_limited,
  COALESCE(COUNT(*) FILTER (WHERE COALESCE(status_code, 0) >= 400 AND ` + slaCountableSQL + `), 0) AS error_sla,
  COALESCE(COUNT(*) FILTER (WHERE error_owner = 'provider' AND ` + slaCountableSQL + ` AND COALESCE(upstream_status_code, status_code, 0) NOT IN (429, 529)), 0) AS upstream_excl,
  COALESCE(COUNT(*) FILTER (WHERE error_owner = 'provider' AND ` + slaCountableSQL + ` AND COALESCE(upstream_status_code, status_code, 0) = 429), 0) AS upstream_429,
  COALESCE(COUNT(*) FILTER (WHERE error_owner = 'provider' AND ` + slaCountableSQL + ` AND COALESCE(upstream_status_code, status_code, 0) = 529), 0) AS upstream_529
FROM ops_error_logs
` + where

	if err := db.QueryRowContext(ctx, q, args...).Scan(
		&errorTotal,
		&businessLimited,
		&errorCountSLA,
		&upstreamExcl429529,
		&upstream429,
		&upstream529,
	); err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	return errorTotal, businessLimited, errorCountSLA, upstreamExcl429529, upstream429, upstream529, nil
}

func (c *MetricsQueries) QueryUsageLatency(ctx context.Context, start, end time.Time) (duration ops.CollectedPercentiles, ttft ops.CollectedPercentiles, err error) {
	{
		q := `
SELECT
  percentile_cont(0.50) WITHIN GROUP (ORDER BY duration_ms) AS p50,
  percentile_cont(0.90) WITHIN GROUP (ORDER BY duration_ms) AS p90,
  percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) AS p95,
  percentile_cont(0.99) WITHIN GROUP (ORDER BY duration_ms) AS p99,
  AVG(duration_ms) AS avg_ms,
  MAX(duration_ms) AS max_ms
FROM usage_logs
WHERE created_at >= $1 AND created_at < $2
  AND duration_ms IS NOT NULL`

		var p50, p90, p95, p99 sql.NullFloat64
		var avg sql.NullFloat64
		var max sql.NullInt64
		if err := c.db.QueryRowContext(ctx, q, start, end).Scan(&p50, &p90, &p95, &p99, &avg, &max); err != nil {
			return ops.CollectedPercentiles{}, ops.CollectedPercentiles{}, err
		}
		duration.P50 = floatToIntPtr(p50)
		duration.P90 = floatToIntPtr(p90)
		duration.P95 = floatToIntPtr(p95)
		duration.P99 = floatToIntPtr(p99)
		if avg.Valid {
			v := ops.RoundTo1DP(avg.Float64)
			duration.Avg = &v
		}
		if max.Valid {
			v := int(max.Int64)
			duration.Max = &v
		}
	}

	{
		q := `
SELECT
  percentile_cont(0.50) WITHIN GROUP (ORDER BY first_token_ms) AS p50,
  percentile_cont(0.90) WITHIN GROUP (ORDER BY first_token_ms) AS p90,
  percentile_cont(0.95) WITHIN GROUP (ORDER BY first_token_ms) AS p95,
  percentile_cont(0.99) WITHIN GROUP (ORDER BY first_token_ms) AS p99,
  AVG(first_token_ms) AS avg_ms,
  MAX(first_token_ms) AS max_ms
FROM usage_logs
WHERE created_at >= $1 AND created_at < $2
  AND first_token_ms IS NOT NULL`

		var p50, p90, p95, p99 sql.NullFloat64
		var avg sql.NullFloat64
		var max sql.NullInt64
		if err := c.db.QueryRowContext(ctx, q, start, end).Scan(&p50, &p90, &p95, &p99, &avg, &max); err != nil {
			return ops.CollectedPercentiles{}, ops.CollectedPercentiles{}, err
		}
		ttft.P50 = floatToIntPtr(p50)
		ttft.P90 = floatToIntPtr(p90)
		ttft.P95 = floatToIntPtr(p95)
		ttft.P99 = floatToIntPtr(p99)
		if avg.Valid {
			v := ops.RoundTo1DP(avg.Float64)
			ttft.Avg = &v
		}
		if max.Valid {
			v := int(max.Int64)
			ttft.Max = &v
		}
	}

	return duration, ttft, nil
}

func (c *MetricsQueries) QueryUsageCounts(ctx context.Context, start, end time.Time) (successCount int64, tokenConsumed int64, err error) {
	q := `
SELECT
  COALESCE(COUNT(*), 0) AS success_count,
  COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS token_consumed
FROM usage_logs
WHERE created_at >= $1 AND created_at < $2`

	var tokens sql.NullInt64
	if err := c.db.QueryRowContext(ctx, q, start, end).Scan(&successCount, &tokens); err != nil {
		return 0, 0, err
	}
	if tokens.Valid {
		tokenConsumed = tokens.Int64
	}
	return successCount, tokenConsumed, nil
}

func NewMetricsQueries(db *sql.DB) ops.MetricsSource {
	if db == nil {
		return nil
	}
	return &MetricsQueries{&Advisory{db}, db}
}
