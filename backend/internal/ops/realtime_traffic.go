package ops

import (
	"context"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// realtime_traffic.go queries short-window traffic summaries and raw count
// windows for live monitoring consumers.
// Realtime traffic volume combines saved usage rows and client-error
// observations, including failures excluded from SLA. Cards use raw current,
// peak, and average rates within one hour; short sampler and WebSocket queries
// count observations using independently supplied, storage-validated endpoints.

// OpsRealtimeTrafficSummary carries raw traffic rate summaries for a short
// dashboard window.
type OpsRealtimeTrafficSummary struct {
	// Window is filled by the HTTP realtime handler from its normalized window
	// parameter, including disabled responses. PostgreSQL leaves this label empty,
	// and the service does not derive it.
	Window string `json:"window"`

	// StartTime and EndTime describe the selected half-open range, which
	// PostgreSQL requires to be positive and no wider than one hour. Current rates
	// separately cover the minute ending at EndTime, and can extend before
	// StartTime for a shorter range.
	StartTime time.Time `json:"start_time"` // inclusive traffic observation start
	EndTime   time.Time `json:"end_time"`   // exclusive traffic observation end

	Platform string `json:"platform"` // selected recorded execution platform
	// GroupID retains the supplied filter pointer in the PostgreSQL response. A
	// positive value filters recorded group observations; nil or nonpositive
	// values omit that storage condition.
	GroupID *int64 `json:"group_id"`

	QPS OpsRateSummary `json:"qps"` // requests per second summaries
	TPS OpsRateSummary `json:"tps"` // tokens per second summaries
}

// GetRealtimeTrafficSummary returns raw QPS/TPS summaries after monitoring,
// repository, and time validation. Ranges wider than one hour are rejected;
// equal endpoints pass service validation but fail the PostgreSQL
// positive-window check. The method sets QueryMode to raw and resolves
// IgnoredStatusCodes in the caller filter. Traffic totals still include
// SLA-excluded client failures.
func (s *OpsService) GetRealtimeTrafficSummary(ctx context.Context, filter *OpsDashboardFilter) (*OpsRealtimeTrafficSummary, error) {
	if err := s.validateDashboardQuery(ctx, filter); err != nil {
		return nil, err
	}
	// The traffic-card API caps its selected window at one hour.
	if filter.EndTime.Sub(filter.StartTime) > time.Hour {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_TOO_LARGE", "invalid time range: max window is 1 hour")
	}

	// The short traffic card uses raw observations for its current and peak rates.
	// Set raw directly so aggregation preferences do not alter this realtime
	// query.
	filter.QueryMode = OpsQueryModeRaw
	s.applyOpsIgnoredStatusCodes(ctx, filter)

	return s.opsRepo.GetRealtimeTrafficSummary(ctx, filter)
}

// GetWindowStats returns request and token observation counts used by realtime
// and WebSocket sampling after monitoring and repository checks. It builds a
// fresh filter carrying the runtime status policy; the returned error total
// includes SLA-excluded client failures. PostgreSQL requires ordered nonzero
// times, accepts equality, and rejects ranges wider than twenty-four hours.
func (s *OpsService) GetWindowStats(ctx context.Context, startTime, endTime time.Time) (*OpsWindowStats, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	filter := &OpsDashboardFilter{
		StartTime: startTime,
		EndTime:   endTime,
	}
	s.applyOpsIgnoredStatusCodes(ctx, filter)
	return s.opsRepo.GetWindowStats(ctx, filter)
}
