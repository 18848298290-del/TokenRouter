package ops

import "time"

// dashboard_models.go defines dashboard filters, traffic summaries, latency
// measurements, and histogram response shapes.
// Dashboard totals distinguish observed usage rows, client-visible error rows,
// and the subset countable for SLA. Rates use fractional encoding for SLA and
// errors, while latency summaries use nullable integer milliseconds; optional
// system samples and job health are collected separately from the traffic
// range.

// OpsDashboardFilter carries the time and dimension selections for historical
// Ops queries.
type OpsDashboardFilter struct {
	// StartTime and EndTime select a half-open observation range. Services require
	// nonzero, ordered endpoints and allow equality; dashboard current rates
	// separately cover the minute ending at EndTime.
	StartTime time.Time // inclusive observation range start
	EndTime   time.Time // exclusive observation range end

	Platform string // recorded execution platform filter
	// GroupID selects a recorded group when positive. Repository queries omit nil
	// or nonpositive identities, while HTTP query parsing rejects supplied
	// nonpositive values.
	GroupID *int64

	// QueryMode supplies an internal data-source preference resolved by the
	// service. HTTP handlers leave source selection to backend settings, and
	// realtime callers can request raw rows.
	QueryMode OpsQueryMode

	// IgnoredStatusCodes supplies client-status exclusions for SLA categories. Nil
	// reads the runtime policy, while an allocated empty slice requests no status
	// exclusions; custom policies select raw overview queries.
	IgnoredStatusCodes []int
}

// OpsRateSummary carries current, peak, and selected-window mean values for one
// rate.
type OpsRateSummary struct {
	// Current normally averages the minute ending at EndTime. GetDashboardOverview
	// substitutes the selected-window average when a deadline degrades optional
	// queries and the current rate is nonpositive.
	Current float64 `json:"current"`
	// Peak normally uses the highest minute count converted to a rate.
	// GetDashboardOverview can substitute the larger current or average rate after
	// a deadline leaves no positive peak.
	Peak float64 `json:"peak"`
	Avg  float64 `json:"avg"` // selected-window average metric rate
}

// OpsPercentiles carries nullable integer-millisecond summaries for duration or
// first-token latency.
type OpsPercentiles struct {
	// P50, P90, P95, P99, Avg, and Max use nil for unavailable measurements and
	// integer milliseconds when measured. Combined hourly summaries approximate
	// P50, P90, and Avg by weighted means, using usage counts for duration and
	// measured-sample counts for TTFT. P95, P99, and Max take the largest segment
	// values.
	P50 *int `json:"p50_ms"` // milliseconds
	P90 *int `json:"p90_ms"` // milliseconds
	P95 *int `json:"p95_ms"` // milliseconds
	P99 *int `json:"p99_ms"` // milliseconds
	Avg *int `json:"avg_ms"` // milliseconds
	Max *int `json:"max_ms"` // milliseconds
}

// OpsDashboardOverview carries selected traffic metrics and separately
// collected system and job diagnostics.
type OpsDashboardOverview struct {
	StartTime time.Time `json:"start_time"` // inclusive selected range start
	EndTime   time.Time `json:"end_time"`   // exclusive selected range end
	Platform  string    `json:"platform"`   // selected recorded execution platform
	GroupID   *int64    `json:"group_id"`   // optional selected group identity

	// HealthScore is the rounded 0-100 diagnostic score from
	// computeDashboardHealthScore. Traffic health contributes seventy percent,
	// infrastructure contributes thirty percent, and an empty traffic range
	// receives one hundred.
	HealthScore int `json:"health_score"`

	// SystemMetrics contains the latest global one-minute sample attached by the
	// service. A failed optional read can leave this pointer nil while traffic
	// query results remain available.
	SystemMetrics *OpsSystemMetricsSnapshot `json:"system_metrics"`

	// JobHeartbeats contains separately read background-job diagnostics attached
	// by the service. A failed optional read leaves these values absent and does
	// not discard the traffic query result.
	JobHeartbeats []*OpsJobHeartbeat `json:"job_heartbeats"`

	// SuccessCount counts usage observations, ErrorCountTotal counts client
	// statuses at least 400, and BusinessLimitedCount identifies their excluded
	// subset. ErrorCountSLA counts the remaining SLA-category failures;
	// RequestCountTotal adds SuccessCount and ErrorCountTotal, while
	// RequestCountSLA adds SuccessCount and ErrorCountSLA.
	SuccessCount         int64 `json:"success_count"`          // observed usage row count
	ErrorCountTotal      int64 `json:"error_count_total"`      // client error observation count
	BusinessLimitedCount int64 `json:"business_limited_count"` // client errors excluded from SLA

	ErrorCountSLA     int64 `json:"error_count_sla"`     // client errors countable for SLA
	RequestCountTotal int64 `json:"request_count_total"` // usage plus client-error observations
	RequestCountSLA   int64 `json:"request_count_sla"`   // usage plus SLA-error observations

	TokenConsumed int64 `json:"token_consumed"` // input/output/cache token count

	// SLA divides SuccessCount by RequestCountSLA, ErrorRate divides ErrorCountSLA
	// by that same denominator, and UpstreamErrorRate divides other countable
	// provider failures by it. Provider 429 and 529 counts are exposed separately;
	// empty denominators yield zero rates.
	SLA               float64 `json:"sla"`                 // fraction
	ErrorRate         float64 `json:"error_rate"`          // fraction
	UpstreamErrorRate float64 `json:"upstream_error_rate"` // fraction
	// countable other provider failures
	UpstreamErrorCountExcl429529 int64 `json:"upstream_error_count_excl_429_529"`
	Upstream429Count             int64 `json:"upstream_429_count"` // countable provider 429 failures
	Upstream529Count             int64 `json:"upstream_529_count"` // countable provider 529 failures

	QPS OpsRateSummary `json:"qps"` // requests per second summaries
	TPS OpsRateSummary `json:"tps"` // tokens per second summaries

	Duration OpsPercentiles `json:"duration"` // request duration latency summaries
	TTFT     OpsPercentiles `json:"ttft"`     // first-token latency measurement summaries
}

// OpsLatencyHistogramBucket carries one displayed duration range and its usage
// observation count.
type OpsLatencyHistogramBucket struct {
	Range string `json:"range"` // formatted duration range label
	Count int64  `json:"count"` // usage latency observation count
}

// OpsLatencyHistogramResponse carries duration buckets for usage rows with
// recorded latency.
type OpsLatencyHistogramResponse struct {
	StartTime time.Time `json:"start_time"` // inclusive selected range start
	EndTime   time.Time `json:"end_time"`   // exclusive selected range end
	Platform  string    `json:"platform"`   // selected recorded execution platform
	GroupID   *int64    `json:"group_id"`   // optional selected group identity

	// BucketBoundariesMS contains five strictly increasing thresholds, and Buckets
	// contains their six displayed ranges in order, including zero counts.
	// TotalRequests sums the counts for rows whose duration_ms is nonnull.
	BucketBoundariesMS []int64 `json:"bucket_boundaries_ms"` // milliseconds
	TotalRequests      int64   `json:"total_requests"`       // usage rows with duration
	// ordered complete duration buckets
	Buckets []*OpsLatencyHistogramBucket `json:"buckets"`
}
