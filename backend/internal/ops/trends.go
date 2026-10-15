package ops

import (
	"context"
	"time"
)

// trends.go defines throughput and error chart responses and routes throughput
// queries through the selected storage mode.
// Throughput buckets count usage and client-error observations together, while
// token totals come from usage rows and switches come from saved attempt
// events. Error charts keep SLA exclusions separate from provider status
// counts, and drilldown breakdowns depend on the selected platform and group.

// OpsThroughputTrendPoint carries traffic observations and rates for one
// displayed time bucket.
type OpsThroughputTrendPoint struct {
	BucketStart   time.Time `json:"bucket_start"`   // UTC observation bucket start
	RequestCount  int64     `json:"request_count"`  // usage plus client-error observations
	TokenConsumed int64     `json:"token_consumed"` // input/output/cache token count
	// SwitchCount counts saved attempt kinds whose prefix is failover,
	// retry_exhausted_failover, or failover_on_400. The query counts those attempt
	// events independently of the request observation total.
	SwitchCount int64 `json:"switch_count"` // recorded failover attempt count
	// QPS and TPS divide RequestCount and TokenConsumed by the full normalized
	// bucket duration in seconds. Repository formatting rounds each rate to one
	// decimal, including partially selected buckets.
	QPS float64 `json:"qps"` // requests per second
	TPS float64 `json:"tps"` // tokens per second
}

// OpsThroughputPlatformBreakdownItem carries traffic and usage-token totals for
// one saved platform.
type OpsThroughputPlatformBreakdownItem struct {
	Platform      string `json:"platform"`       // saved execution platform label
	RequestCount  int64  `json:"request_count"`  // usage plus client-error observations
	TokenConsumed int64  `json:"token_consumed"` // input/output/cache token count
}

// OpsThroughputGroupBreakdownItem carries traffic and usage-token totals for
// one recorded group.
type OpsThroughputGroupBreakdownItem struct {
	GroupID       int64  `json:"group_id"`       // recorded group identity dimension
	GroupName     string `json:"group_name"`     // current joined group name
	RequestCount  int64  `json:"request_count"`  // usage plus client-error observations
	TokenConsumed int64  `json:"token_consumed"` // input/output/cache token count
}

// OpsThroughputTrendResponse carries time-bucketed throughput and optional
// selection-dependent drilldown totals.
type OpsThroughputTrendResponse struct {
	Bucket string `json:"bucket"` // 1m/5m/1h

	// Points is ordered by BucketStart and includes zero-valued buckets inserted
	// for missing observations. Its first and last buckets can extend beyond the
	// selected interval.
	Points []*OpsThroughputTrendPoint `json:"points"` // ordered complete throughput buckets

	// ByPlatform is returned when platform is empty and no positive group is
	// selected. TopGroups is returned for a selected platform with no positive
	// group, limited to ten groups by request count; a positive group suppresses
	// both breakdowns.
	// optional platform drilldown totals
	ByPlatform []*OpsThroughputPlatformBreakdownItem `json:"by_platform,omitempty"`
	// optional leading group totals
	TopGroups []*OpsThroughputGroupBreakdownItem `json:"top_groups,omitempty"`
}

// OpsErrorTrendPoint carries client-failure categories and provider-status
// observations for one time bucket.
type OpsErrorTrendPoint struct {
	BucketStart time.Time `json:"bucket_start"` // UTC observation bucket start

	// ErrorCountTotal counts client statuses at least 400. BusinessLimitedCount
	// identifies their SLA-excluded subset, and ErrorCountSLA counts the remaining
	// eligible failures under the resolved status policy.
	ErrorCountTotal      int64 `json:"error_count_total"`      // client error observation count
	BusinessLimitedCount int64 `json:"business_limited_count"` // client errors excluded from SLA
	ErrorCountSLA        int64 `json:"error_count_sla"`        // client failures countable for SLA

	// UpstreamErrorCountExcl429529, Upstream429Count, and Upstream529Count group
	// SLA-countable provider-owned observations by effective status. The query
	// prefers a saved upstream status to the client status, independently of the
	// client-error total.
	// countable other provider failures
	UpstreamErrorCountExcl429529 int64 `json:"upstream_error_count_excl_429_529"`
	Upstream429Count             int64 `json:"upstream_429_count"` // countable provider 429 failures
	Upstream529Count             int64 `json:"upstream_529_count"` // countable provider 529 failures
}

// OpsErrorTrendResponse carries a complete displayed timeline of error-count
// buckets.
type OpsErrorTrendResponse struct {
	Bucket string `json:"bucket"` // 1m/5m/1h
	// Points contains UTC buckets in ascending order with zero counts for missing
	// observations. Partial selected intervals still use their full enclosing
	// buckets.
	Points []*OpsErrorTrendPoint `json:"points"` // ordered complete error buckets
}

// OpsErrorDistributionItem carries client-failure counts grouped by one
// effective saved status.
type OpsErrorDistributionItem struct {
	// StatusCode uses the saved upstream status when nonnull, otherwise the client
	// status, and finally zero. A supplied upstream zero remains its own grouping
	// value.
	StatusCode int `json:"status_code"` // effective recorded status value
	// Total counts matching client failures in this status group. SLA and
	// BusinessLimited divide those observations into countable and excluded
	// categories using the resolved ignored-status policy.
	Total           int64 `json:"total"`            // matching client-error observation count
	SLA             int64 `json:"sla"`              // client failures countable for SLA
	BusinessLimited int64 `json:"business_limited"` // client errors excluded from SLA
}

// OpsErrorDistributionResponse carries the returned status groups and their
// combined error count.
type OpsErrorDistributionResponse struct {
	// Total sums the counts in Items, and the PostgreSQL query returns at most
	// twenty status groups in descending frequency. It therefore describes the
	// displayed groups when additional matching statuses exist.
	Total int64                       `json:"total"` // returned status-group error total
	Items []*OpsErrorDistributionItem `json:"items"` // descending frequency status groups
}

// GetThroughputTrend returns traffic buckets after dashboard validation and
// resolves QueryMode in the supplied filter. An automatic-mode error is retried
// with a shallow raw copy, while the repository normalizes bucketSeconds and
// supplies any selection-dependent breakdowns.
func (s *OpsService) GetThroughputTrend(ctx context.Context, filter *OpsDashboardFilter, bucketSeconds int) (*OpsThroughputTrendResponse, error) {
	if err := s.validateDashboardQuery(ctx, filter); err != nil {
		return nil, err
	}

	filter.QueryMode = s.resolveOpsQueryMode(ctx, filter.QueryMode)

	result, err := s.opsRepo.GetThroughputTrend(ctx, filter, bucketSeconds)
	// Retry the full range after an automatic-mode failure; the raw copy keeps
	// selection fields and does not combine the first attempt with the new
	// timeline.
	if err != nil && ShouldFallbackOpsPreagg(filter, err) {
		rawFilter := CloneOpsFilterWithMode(filter, OpsQueryModeRaw)
		return s.opsRepo.GetThroughputTrend(ctx, rawFilter, bucketSeconds)
	}
	return result, err
}
