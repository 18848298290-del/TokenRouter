package ops

import (
	"context"
	"log"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// GetDashboardOverview selects a data source using pre-aggregation settings and retries raw data on aggregation failure.
// @project-doc docs/operations/pre_aggregation.md#query_routing_and_fallback
func (s *OpsService) GetDashboardOverview(ctx context.Context, filter *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	if err := s.validateDashboardQuery(ctx, filter); err != nil {
		return nil, err
	}

	// The unified pre-aggregation settings control the dashboard's data source.
	s.resolveOpsQueryModeWithIgnoredStatusCodes(ctx, filter)

	overview, err := s.opsRepo.GetDashboardOverview(ctx, filter)
	if err != nil && ShouldFallbackOpsPreagg(filter, err) {
		rawFilter := CloneOpsFilterWithMode(filter, OpsQueryModeRaw)
		overview, err = s.opsRepo.GetDashboardOverview(ctx, rawFilter)
	}
	if err != nil {
		return nil, err
	}

	// Best-effort system health + jobs; dashboard metrics should still render if these are missing.
	if metrics, err := s.opsRepo.GetLatestSystemMetrics(ctx, 1); err == nil {
		// Attach config-derived limits so the UI can show "current / max" for connection pools.
		// These are best-effort and should never block the dashboard rendering.
		if s != nil && s.cfg != nil {
			if s.cfg.Database.MaxOpenConns > 0 {
				metrics.DBMaxOpenConns = intPtr(s.cfg.Database.MaxOpenConns)
			}
			if s.cfg.Redis.PoolSize > 0 {
				metrics.RedisPoolSize = intPtr(s.cfg.Redis.PoolSize)
			}
		}
		overview.SystemMetrics = metrics
	} else if err != nil && !s.isNotFound(err) {
		log.Printf("[Ops] GetLatestSystemMetrics failed: %v", err)
	}

	if heartbeats, err := s.opsRepo.ListJobHeartbeats(ctx); err == nil {
		overview.JobHeartbeats = heartbeats
	} else {
		log.Printf("[Ops] ListJobHeartbeats failed: %v", err)
	}

	overview.HealthScore = computeDashboardHealthScore(time.Now().UTC(), overview)

	return overview, nil
}

func (s *OpsService) resolveOpsQueryMode(ctx context.Context, requested OpsQueryMode) OpsQueryMode {
	// Internal callers use raw for realtime monitoring, alerts, and custom ignored status codes.
	if requested == OpsQueryModeRaw {
		return OpsQueryModeRaw
	}
	if s != nil && s.preAggregationSettings != nil {
		if s.preAggregationSettings.OpsEnabled(ctx) {
			return OpsQueryModeAuto
		}
		return OpsQueryModeRaw
	}
	if s != nil && s.cfg != nil && (!s.cfg.Ops.Enabled || !s.cfg.Ops.Aggregation.Enabled) {
		return OpsQueryModeRaw
	}
	return OpsQueryModeAuto
}

// validateDashboardQuery checks monitoring, storage availability, and the time range in that order.
func (s *OpsService) validateDashboardQuery(ctx context.Context, filter *OpsDashboardFilter) error {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return err
	}
	if s.opsRepo == nil {
		return infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if filter == nil {
		return infraerrors.BadRequest("OPS_FILTER_REQUIRED", "filter is required")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return infraerrors.BadRequest("OPS_TIME_RANGE_REQUIRED", "start_time/end_time are required")
	}
	if filter.StartTime.After(filter.EndTime) {
		return infraerrors.BadRequest("OPS_TIME_RANGE_INVALID", "start_time must be <= end_time")
	}
	return nil
}
