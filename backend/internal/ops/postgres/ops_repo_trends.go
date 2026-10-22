package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// ops_repo_trends.go queries throughput and error trends and fills their
// requested timelines with continuous buckets.
// Throughput charts combine usage observations, final client failures, and
// saved switch events as separate quantities. Tokens come from usage rows,
// request totals add usage and failed client observations, and switches count
// selected attempt-kind prefixes even when a request recovered. Each rate
// divides by the complete bucket duration, so a partially selected bucket
// retains that denominator.
//
// Raw SQL produces sparse buckets and later filling supplies a continuous
// timeline. Five-minute SQL buckets use epoch arithmetic; minute and hour
// date_trunc follow the SQL session timezone, while filling uses epoch-aligned
// starts. Existing points survive only when their timestamp matches a generated
// slot, so callers need compatible bucket alignment. Breakdown reads run after
// the timeline query and may observe another database state. Every query uses
// raw tables regardless of QueryMode, and provider/SLA error categories retain
// different eligibility rules.

// GetThroughputTrend returns raw throughput buckets and a selection-dependent
// breakdown for nonzero endpoints. It accepts one-minute, five-minute, or
// hourly buckets and replaces other values with one minute. Endpoint ordering
// is left to OpsService; platform and positive group filtering use saved
// dimensions, while QueryMode is ignored.
//
// Request totals add usage and final-error observations, tokens sum usage only,
// and switch counts inspect saved attempt kinds independently of final status.
// Rates divide by the entire bucket and round to one decimal, then missing
// generated slots become zero points. An unrestricted selection loads
// platforms; a platform without a positive group loads ten leading groups; a
// positive group suppresses both. Breakdown errors reject the response after
// the successful timeline read, and separate statements can observe different
// writes.
func (r *Store) GetThroughputTrend(ctx context.Context, filter *ops.OpsDashboardFilter, bucketSeconds int) (*ops.OpsThroughputTrendResponse, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}

	if bucketSeconds <= 0 {
		bucketSeconds = 60
	}
	if bucketSeconds != 60 && bucketSeconds != 300 && bucketSeconds != 3600 {
		// Use one minute for unsupported sizes so SQL bucket selection, returned
		// labels, filling, and rate denominators keep one consistent normalized
		// duration.
		bucketSeconds = 60
	}

	start := filter.StartTime.UTC()
	end := filter.EndTime.UTC()

	usageJoin, usageWhere, usageArgs, next := buildUsageWhere(filter, start, end, 1)
	errorWhere, errorArgs, _ := buildErrorWhere(filter, start, end, next)

	usageBucketExpr := opsBucketExprForUsage(bucketSeconds)
	errorBucketExpr := opsBucketExprForError(bucketSeconds)

	// Count switch-event kind prefixes independently of request totals. Recovered
	// client outcomes can still contribute failover attempts, while tokens remain
	// supplied by usage rows.
	q := `
WITH usage_buckets AS (
  SELECT ` + usageBucketExpr + ` AS bucket,
         COUNT(*) AS success_count,
         COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS token_consumed
  FROM usage_logs ul
  ` + usageJoin + `
  ` + usageWhere + `
  GROUP BY 1
),
error_buckets AS (
  SELECT ` + errorBucketExpr + ` AS bucket,
         COUNT(*) AS error_count
  FROM ops_error_logs
  ` + errorWhere + `
    AND COALESCE(status_code, 0) >= 400
  GROUP BY 1
),
switch_buckets AS (
  SELECT ` + errorBucketExpr + ` AS bucket,
         COALESCE(SUM(CASE
           WHEN split_part(ev->>'kind', ':', 1) IN ('failover', 'retry_exhausted_failover', 'failover_on_400') THEN 1
           ELSE 0
         END), 0) AS switch_count
  FROM ops_error_logs
  CROSS JOIN LATERAL jsonb_array_elements(
    COALESCE(NULLIF(upstream_errors, 'null'::jsonb), '[]'::jsonb)
  ) AS ev
  ` + errorWhere + `
    AND upstream_errors IS NOT NULL
  GROUP BY 1
),
combined AS (
  SELECT
    bucket,
    SUM(success_count) AS success_count,
    SUM(error_count) AS error_count,
    SUM(token_consumed) AS token_consumed,
    SUM(switch_count) AS switch_count
  FROM (
    SELECT bucket, success_count, 0 AS error_count, token_consumed, 0 AS switch_count
    FROM usage_buckets
    UNION ALL
    SELECT bucket, 0, error_count, 0, 0
    FROM error_buckets
    UNION ALL
    SELECT bucket, 0, 0, 0, switch_count
    FROM switch_buckets
  ) t
  GROUP BY bucket
)
SELECT
  bucket,
  (success_count + error_count) AS request_count,
  token_consumed,
  switch_count
FROM combined
ORDER BY bucket ASC`

	args := append(usageArgs, errorArgs...)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	// Close the cursor after success or an early scan failure. Cleanup errors are
	// suppressed, while iteration errors still reject this query's result.
	defer func() { _ = rows.Close() }()

	points := make([]*ops.OpsThroughputTrendPoint, 0, 256)
	for rows.Next() {
		var bucket time.Time
		var requests int64
		var tokens sql.NullInt64
		var switches sql.NullInt64
		if err := rows.Scan(&bucket, &requests, &tokens, &switches); err != nil {
			return nil, err
		}
		tokenConsumed := int64(0)
		if tokens.Valid {
			tokenConsumed = tokens.Int64
		}
		switchCount := int64(0)
		if switches.Valid {
			switchCount = switches.Int64
		}

		// Divide by the full normalized bucket seconds even at partially selected
		// endpoints, then round QPS and TPS to one decimal.
		denom := float64(bucketSeconds)
		if denom <= 0 {
			denom = 60
		}
		qps := roundTo1DP(float64(requests) / denom)
		tps := roundTo1DP(float64(tokenConsumed) / denom)

		points = append(points, &ops.OpsThroughputTrendPoint{
			BucketStart:   bucket.UTC(),
			RequestCount:  requests,
			TokenConsumed: tokenConsumed,
			SwitchCount:   switchCount,
			QPS:           qps,
			TPS:           tps,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fill epoch-aligned slots after the sparse raw query. This keeps idle
	// intervals visible, reuses matching point pointers, and omits points with
	// other alignment.
	points = fillOpsThroughputBuckets(start, end, bucketSeconds, points)

	var byPlatform []*ops.OpsThroughputPlatformBreakdownItem
	var topGroups []*ops.OpsThroughputGroupBreakdownItem

	platform := ""
	if filter != nil {
		platform = strings.TrimSpace(strings.ToLower(filter.Platform))
	}
	groupID := (*int64)(nil)
	if filter != nil {
		groupID = filter.GroupID
	}

	// Read drilldown after the timeline: unrestricted filters select platforms,
	// platform-only filters select leading groups, and a positive group leaves
	// both absent. Each read can observe newer writes.
	if platform == "" && (groupID == nil || *groupID <= 0) {
		items, err := r.getThroughputBreakdownByPlatform(ctx, start, end)
		if err != nil {
			return nil, err
		}
		byPlatform = items
	} else if platform != "" && (groupID == nil || *groupID <= 0) {
		items, err := r.getThroughputTopGroupsByPlatform(ctx, start, end, platform, 10)
		if err != nil {
			return nil, err
		}
		topGroups = items
	}

	return &ops.OpsThroughputTrendResponse{
		Bucket: opsBucketLabel(bucketSeconds),
		Points: points,

		ByPlatform: byPlatform,
		TopGroups:  topGroups,
	}, nil
}

// getThroughputBreakdownByPlatform returns raw usage-plus-client-failure counts
// and usage token totals for each saved nonempty platform in the half-open
// range. It expects a usable Store and supplied endpoints; count-token error
// probes are excluded, and rows are ordered by request count without a tie
// breaker.
//
// A full outer join retains a platform observed by either source, while empty
// or null platforms disappear from the result. Usage totals read the recorded
// platform directly. This independent query can observe data newer than the
// preceding timeline, and its scan or iteration errors reject the combined
// response.
func (r *Store) getThroughputBreakdownByPlatform(ctx context.Context, start, end time.Time) ([]*ops.OpsThroughputPlatformBreakdownItem, error) {
	q := `
WITH usage_totals AS (
  SELECT ul.platform AS platform,
         COUNT(*) AS success_count,
         COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS token_consumed
  FROM usage_logs ul
  WHERE ul.created_at >= $1 AND ul.created_at < $2
  GROUP BY 1
),
error_totals AS (
  SELECT platform,
         COUNT(*) AS error_count
  FROM ops_error_logs
  WHERE created_at >= $1 AND created_at < $2
    AND COALESCE(status_code, 0) >= 400
    AND is_count_tokens = FALSE  -- 排除 count_tokens 请求的错误
  GROUP BY 1
),
combined AS (
  SELECT COALESCE(u.platform, e.platform) AS platform,
         COALESCE(u.success_count, 0) AS success_count,
         COALESCE(e.error_count, 0) AS error_count,
         COALESCE(u.token_consumed, 0) AS token_consumed
  FROM usage_totals u
  FULL OUTER JOIN error_totals e ON u.platform = e.platform
)
SELECT platform, (success_count + error_count) AS request_count, token_consumed
FROM combined
WHERE platform IS NOT NULL AND platform <> ''
ORDER BY request_count DESC`

	rows, err := r.db.QueryContext(ctx, q, start, end)
	if err != nil {
		return nil, err
	}
	// Close the cursor after success or an early scan failure. Cleanup errors are
	// suppressed, while iteration errors still reject this query's result.
	defer func() { _ = rows.Close() }()

	items := make([]*ops.OpsThroughputPlatformBreakdownItem, 0, 8)
	for rows.Next() {
		var platform string
		var requests int64
		var tokens sql.NullInt64
		if err := rows.Scan(&platform, &requests, &tokens); err != nil {
			return nil, err
		}
		tokenConsumed := int64(0)
		if tokens.Valid {
			tokenConsumed = tokens.Int64
		}
		items = append(items, &ops.OpsThroughputPlatformBreakdownItem{
			Platform:      platform,
			RequestCount:  requests,
			TokenConsumed: tokenConsumed,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// getThroughputTopGroupsByPlatform returns leading raw group totals for a
// supplied recorded platform and half-open range. Blank platform returns nil,
// while nonpositive or over-one-hundred limits select ten. Other platform text
// is bound as supplied, and request-count ordering has no group tie breaker.
//
// Usage rows require a current groups join, so usage whose group is gone is
// excluded; error totals require a nonnull saved group and can still appear
// with an empty name. The sources combine by group identity, tokens come from
// usage, and count-token errors are excluded. Callers keep Store usable for
// this query; scan and iteration errors reject its result.
func (r *Store) getThroughputTopGroupsByPlatform(ctx context.Context, start, end time.Time, platform string, limit int) ([]*ops.OpsThroughputGroupBreakdownItem, error) {
	if strings.TrimSpace(platform) == "" {
		return nil, nil
	}
	// Keep drilldown within the supported hundred-group bound; unusable limits
	// select the standard top-ten view requested by the timeline caller.
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	q := `
WITH usage_totals AS (
  SELECT ul.group_id AS group_id,
         g.name AS group_name,
         COUNT(*) AS success_count,
         COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS token_consumed
  FROM usage_logs ul
  JOIN groups g ON g.id = ul.group_id
  WHERE ul.created_at >= $1 AND ul.created_at < $2
    AND ul.platform = $3
  GROUP BY 1, 2
),
error_totals AS (
  SELECT group_id,
         COUNT(*) AS error_count
  FROM ops_error_logs
  WHERE created_at >= $1 AND created_at < $2
    AND platform = $3
    AND group_id IS NOT NULL
    AND COALESCE(status_code, 0) >= 400
    AND is_count_tokens = FALSE  -- 排除 count_tokens 请求的错误
  GROUP BY 1
),
combined AS (
  SELECT COALESCE(u.group_id, e.group_id) AS group_id,
         COALESCE(u.group_name, g2.name, '') AS group_name,
         COALESCE(u.success_count, 0) AS success_count,
         COALESCE(e.error_count, 0) AS error_count,
         COALESCE(u.token_consumed, 0) AS token_consumed
  FROM usage_totals u
  FULL OUTER JOIN error_totals e ON u.group_id = e.group_id
  LEFT JOIN groups g2 ON g2.id = COALESCE(u.group_id, e.group_id)
)
SELECT group_id, group_name, (success_count + error_count) AS request_count, token_consumed
FROM combined
WHERE group_id IS NOT NULL
ORDER BY request_count DESC
LIMIT $4`

	rows, err := r.db.QueryContext(ctx, q, start, end, platform, limit)
	if err != nil {
		return nil, err
	}
	// Close the cursor after success or an early scan failure. Cleanup errors are
	// suppressed, while iteration errors still reject this query's result.
	defer func() { _ = rows.Close() }()

	items := make([]*ops.OpsThroughputGroupBreakdownItem, 0, limit)
	for rows.Next() {
		var groupID int64
		var groupName sql.NullString
		var requests int64
		var tokens sql.NullInt64
		if err := rows.Scan(&groupID, &groupName, &requests, &tokens); err != nil {
			return nil, err
		}
		tokenConsumed := int64(0)
		if tokens.Valid {
			tokenConsumed = tokens.Int64
		}
		name := ""
		if groupName.Valid {
			name = groupName.String
		}
		items = append(items, &ops.OpsThroughputGroupBreakdownItem{
			GroupID:       groupID,
			GroupName:     name,
			RequestCount:  requests,
			TokenConsumed: tokenConsumed,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// opsBucketExprForUsage returns a trusted usage timestamp grouping expression.
// Five-minute buckets align to Unix epoch multiples; minute and hour truncation
// follows the SQL session timezone, and unsupported values choose minutes.
func opsBucketExprForUsage(bucketSeconds int) string {
	switch bucketSeconds {
	case 3600:
		return "date_trunc('hour', ul.created_at)"
	case 300:
		// Epoch division creates five-minute absolute slots; formatting the returned
		// timestamp in UTC does not depend on session hour boundaries.
		return "to_timestamp(floor(extract(epoch from ul.created_at) / 300) * 300)"
	default:
		return "date_trunc('minute', ul.created_at)"
	}
}

// opsBucketExprForError returns a trusted error timestamp grouping expression.
// Five-minute buckets align to Unix epoch multiples; minute and hour date_trunc
// uses the session timezone, and unsupported values choose minutes.
func opsBucketExprForError(bucketSeconds int) string {
	switch bucketSeconds {
	case 3600:
		return "date_trunc('hour', created_at)"
	case 300:
		// Epoch slots share the usage timeline's absolute five-minute alignment.
		return "to_timestamp(floor(extract(epoch from created_at) / 300) * 300)"
	default:
		return "date_trunc('minute', created_at)"
	}
}

// opsBucketLabel formats a display duration for supplied seconds. Nonpositive
// input selects 1m, positive whole hours use h, and other values truncate to
// minutes with a minimum label of 1m; no allowed-bucket validation occurs here.
func opsBucketLabel(bucketSeconds int) string {
	if bucketSeconds <= 0 {
		return "1m"
	}
	if bucketSeconds%3600 == 0 {
		// Convert whole hours into the compact chart label.
		h := bucketSeconds / 3600
		if h <= 0 {
			h = 1
		}
		return fmt.Sprintf("%dh", h)
	}
	// Truncate partial minutes for the compact chart duration label.
	m := bucketSeconds / 60
	if m <= 0 {
		m = 1
	}
	return fmt.Sprintf("%dm", m)
}

// opsFloorToBucketStart aligns nonnegative Unix seconds to the preceding
// epoch-based slot and returns UTC. Nonpositive bucket size selects one minute;
// Go remainder arithmetic rounds pre-epoch negative seconds toward zero instead
// of mathematical floor.
func opsFloorToBucketStart(t time.Time, bucketSeconds int) time.Time {
	t = t.UTC()
	if bucketSeconds <= 0 {
		bucketSeconds = 60
	}
	secs := t.Unix()
	// Use epoch-second remainder alignment for modern nonnegative observation
	// times.
	floored := secs - (secs % int64(bucketSeconds))
	return time.Unix(floored, 0).UTC()
}

// fillOpsThroughputBuckets returns throughput slots enclosing a positive
// half-open range, inserting zero points for missing timestamps. Nonpositive
// bucket size selects one minute. An unordered or empty range returns the
// supplied slice itself; otherwise existing points are indexed by UTC Unix
// seconds, with the last duplicate winning and nil entries ignored.
//
// Existing point pointers are reused in the new slice, so callers keep their
// contents immutable while sharing either result. Points outside the generated
// slots or with incompatible alignment are omitted. The first slot can precede
// start, and subtracting one nanosecond from end excludes an exactly aligned
// exclusive-end slot. Whole-bucket rates are left as supplied for reused
// observations.
func fillOpsThroughputBuckets(start, end time.Time, bucketSeconds int, points []*ops.OpsThroughputTrendPoint) []*ops.OpsThroughputTrendPoint {
	if bucketSeconds <= 0 {
		bucketSeconds = 60
	}
	if !start.Before(end) {
		return points
	}

	// Move the exclusive end back one nanosecond before selecting its last slot,
	// so a range ending on a bucket start excludes that bucket.
	endMinus := end.Add(-time.Nanosecond)
	if endMinus.Before(start) {
		return points
	}

	first := opsFloorToBucketStart(start, bucketSeconds)
	last := opsFloorToBucketStart(endMinus, bucketSeconds)
	step := time.Duration(bucketSeconds) * time.Second

	// Index points by exact Unix-second bucket identity before generating slots.
	// Later duplicates win and matched pointers stay shared with the caller's
	// input.
	existing := make(map[int64]*ops.OpsThroughputTrendPoint, len(points))
	for _, p := range points {
		if p == nil {
			continue
		}
		existing[p.BucketStart.UTC().Unix()] = p
	}

	out := make([]*ops.OpsThroughputTrendPoint, 0, int(last.Sub(first)/step)+1)
	for cursor := first; !cursor.After(last); cursor = cursor.Add(step) {
		if p, ok := existing[cursor.Unix()]; ok && p != nil {
			out = append(out, p)
			continue
		}
		out = append(out, &ops.OpsThroughputTrendPoint{
			BucketStart:   cursor,
			RequestCount:  0,
			TokenConsumed: 0,
			SwitchCount:   0,
			QPS:           0,
			TPS:           0,
		})
	}
	return out
}

// GetErrorTrend returns raw classified error buckets for nonzero endpoints and
// a normalized supported bucket size. OpsService owns endpoint-order
// validation; this method ignores QueryMode and excludes count-token probes
// through buildErrorWhere. Totals, business-limited, and SLA counts require
// final client failure.
//
// Provider-owned SLA-countable observations are classified by effective
// upstream-or-client status independently of final-error totals, including
// eligible recovered observations. SQL reads sparse bucket counts, then filling
// inserts zero slots with shared point ownership and epoch alignment
// requirements. Error buckets and returned labels follow the same one-minute,
// five-minute, or hourly normalization; query, scan, and iteration failures
// reject the result.
func (r *Store) GetErrorTrend(ctx context.Context, filter *ops.OpsDashboardFilter, bucketSeconds int) (*ops.OpsErrorTrendResponse, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}

	if bucketSeconds <= 0 {
		bucketSeconds = 60
	}
	// Normalize unsupported chart buckets before building expressions and filling
	// slots, so both phases use the same rate-independent timeline duration.
	if bucketSeconds != 60 && bucketSeconds != 300 && bucketSeconds != 3600 {
		bucketSeconds = 60
	}

	start := filter.StartTime.UTC()
	end := filter.EndTime.UTC()
	where, args, _ := buildErrorWhere(filter, start, end, 1)
	bucketExpr := opsBucketExprForError(bucketSeconds)
	businessLimitedSQL := opsBusinessLimitedSQL("status_code", "upstream_status_code", "upstream_errors", "error_owner", "is_business_limited", filter.IgnoredStatusCodes)
	slaCountableSQL := opsSLACountableSQL("status_code", "upstream_status_code", "upstream_errors", "error_owner", "is_business_limited", filter.IgnoredStatusCodes)

	q := `
SELECT
  ` + bucketExpr + ` AS bucket,
  COUNT(*) FILTER (WHERE COALESCE(status_code, 0) >= 400) AS error_total,
  COUNT(*) FILTER (WHERE COALESCE(status_code, 0) >= 400 AND ` + businessLimitedSQL + `) AS business_limited,
  COUNT(*) FILTER (WHERE COALESCE(status_code, 0) >= 400 AND ` + slaCountableSQL + `) AS error_sla,
  COUNT(*) FILTER (WHERE error_owner = 'provider' AND ` + slaCountableSQL + ` AND COALESCE(upstream_status_code, status_code, 0) NOT IN (429, 529)) AS upstream_excl,
  COUNT(*) FILTER (WHERE error_owner = 'provider' AND ` + slaCountableSQL + ` AND COALESCE(upstream_status_code, status_code, 0) = 429) AS upstream_429,
  COUNT(*) FILTER (WHERE error_owner = 'provider' AND ` + slaCountableSQL + ` AND COALESCE(upstream_status_code, status_code, 0) = 529) AS upstream_529
FROM ops_error_logs
` + where + `
GROUP BY 1
ORDER BY 1 ASC`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	// Close the cursor after success or an early scan failure. Cleanup errors are
	// suppressed, while iteration errors still reject this query's result.
	defer func() { _ = rows.Close() }()

	points := make([]*ops.OpsErrorTrendPoint, 0, 256)
	for rows.Next() {
		var bucket time.Time
		var total, businessLimited, sla, upstreamExcl, upstream429, upstream529 int64
		if err := rows.Scan(&bucket, &total, &businessLimited, &sla, &upstreamExcl, &upstream429, &upstream529); err != nil {
			return nil, err
		}
		points = append(points, &ops.OpsErrorTrendPoint{
			BucketStart: bucket.UTC(),

			ErrorCountTotal:      total,
			BusinessLimitedCount: businessLimited,
			ErrorCountSLA:        sla,

			UpstreamErrorCountExcl429529: upstreamExcl,
			Upstream429Count:             upstream429,
			Upstream529Count:             upstream529,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	points = fillOpsErrorTrendBuckets(start, end, bucketSeconds, points)

	return &ops.OpsErrorTrendResponse{
		Bucket: opsBucketLabel(bucketSeconds),
		Points: points,
	}, nil
}

// fillOpsErrorTrendBuckets returns error-count slots enclosing a positive
// half-open range, adding zero points where matching timestamps are missing.
// Nonpositive bucket size selects one minute, and unordered or empty endpoints
// return the supplied slice itself. Nil observations are skipped and the last
// point for duplicate UTC Unix seconds wins.
//
// A new slice reuses matched point pointers, which callers treat as immutable
// while shared. Out-of-range or differently aligned points are omitted; the
// first enclosing slot can precede start. Subtracting a nanosecond from end
// keeps an aligned exclusive end out of the timeline. Counts already present in
// reused points remain supplied rather than being recomputed.
func fillOpsErrorTrendBuckets(start, end time.Time, bucketSeconds int, points []*ops.OpsErrorTrendPoint) []*ops.OpsErrorTrendPoint {
	if bucketSeconds <= 0 {
		bucketSeconds = 60
	}
	if !start.Before(end) {
		return points
	}

	// Move the exclusive end back one nanosecond before selecting its last slot,
	// so a range ending on a bucket start excludes that bucket.
	endMinus := end.Add(-time.Nanosecond)
	if endMinus.Before(start) {
		return points
	}

	first := opsFloorToBucketStart(start, bucketSeconds)
	last := opsFloorToBucketStart(endMinus, bucketSeconds)
	step := time.Duration(bucketSeconds) * time.Second

	// Index points by exact Unix-second bucket identity before generating slots.
	// Later duplicates win and matched pointers stay shared with the caller's
	// input.
	existing := make(map[int64]*ops.OpsErrorTrendPoint, len(points))
	for _, p := range points {
		if p == nil {
			continue
		}
		existing[p.BucketStart.UTC().Unix()] = p
	}

	out := make([]*ops.OpsErrorTrendPoint, 0, int(last.Sub(first)/step)+1)
	for cursor := first; !cursor.After(last); cursor = cursor.Add(step) {
		if p, ok := existing[cursor.Unix()]; ok && p != nil {
			out = append(out, p)
			continue
		}
		out = append(out, &ops.OpsErrorTrendPoint{
			BucketStart: cursor,

			ErrorCountTotal:      0,
			BusinessLimitedCount: 0,
			ErrorCountSLA:        0,

			UpstreamErrorCountExcl429529: 0,
			Upstream429Count:             0,
			Upstream529Count:             0,
		})
	}
	return out
}

// GetErrorDistribution groups matching raw final-client failures by effective
// saved status, preferring nonnull upstream status even when zero. Nonzero
// endpoints are required, while ordering is validated by OpsService. QueryMode
// is ignored and count-token errors are excluded by the shared base predicate.
//
// The shared SLA/business policy divides each displayed status group into
// countable and excluded observations. SQL selects at most twenty groups
// ordered by total without a status tie breaker. Response Total sums those
// returned groups, so matching failures in additional statuses are absent from
// that total. Items are newly allocated; query, scan, and iteration errors
// reject the response.
func (r *Store) GetErrorDistribution(ctx context.Context, filter *ops.OpsDashboardFilter) (*ops.OpsErrorDistributionResponse, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}

	start := filter.StartTime.UTC()
	end := filter.EndTime.UTC()
	where, args, _ := buildErrorWhere(filter, start, end, 1)
	businessLimitedSQL := opsBusinessLimitedSQL("status_code", "upstream_status_code", "upstream_errors", "error_owner", "is_business_limited", filter.IgnoredStatusCodes)
	slaCountableSQL := opsSLACountableSQL("status_code", "upstream_status_code", "upstream_errors", "error_owner", "is_business_limited", filter.IgnoredStatusCodes)

	// Limit displayed status groups to the twenty most frequent.
	q := `
SELECT
  COALESCE(upstream_status_code, status_code, 0) AS status_code,
  COUNT(*) AS total,
  COUNT(*) FILTER (WHERE ` + slaCountableSQL + `) AS sla,
  COUNT(*) FILTER (WHERE ` + businessLimitedSQL + `) AS business_limited
FROM ops_error_logs
` + where + `
  AND COALESCE(status_code, 0) >= 400
GROUP BY 1
ORDER BY total DESC
LIMIT 20`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	// Close the cursor after success or an early scan failure. Cleanup errors are
	// suppressed, while iteration errors still reject this query's result.
	defer func() { _ = rows.Close() }()

	items := make([]*ops.OpsErrorDistributionItem, 0, 16)
	var total int64
	for rows.Next() {
		var statusCode int
		var cntTotal, cntSLA, cntBiz int64
		if err := rows.Scan(&statusCode, &cntTotal, &cntSLA, &cntBiz); err != nil {
			return nil, err
		}
		total += cntTotal
		items = append(items, &ops.OpsErrorDistributionItem{
			StatusCode:      statusCode,
			Total:           cntTotal,
			SLA:             cntSLA,
			BusinessLimited: cntBiz,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &ops.OpsErrorDistributionResponse{
		Total: total,
		Items: items,
	}, nil
}
