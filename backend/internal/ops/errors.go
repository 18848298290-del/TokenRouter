package ops

import (
	"context"
)

// Error charts separate client-failure totals from SLA-countable failures and
// recorded provider statuses. Each service query resolves the current
// exclusions and source preference first, then can repeat an automatic-mode
// failure using a raw-mode copy of the same time and dimension filters.

// GetErrorTrend returns time-bucketed client and provider error counts after
// dashboard validation. It resolves QueryMode and IgnoredStatusCodes in the
// caller filter and retries an automatic-mode error in raw mode; bucketSeconds
// is normalized by the repository.
func (s *OpsService) GetErrorTrend(ctx context.Context, filter *OpsDashboardFilter, bucketSeconds int) (*OpsErrorTrendResponse, error) {
	if err := s.validateDashboardQuery(ctx, filter); err != nil {
		return nil, err
	}
	s.resolveOpsQueryModeWithIgnoredStatusCodes(ctx, filter)

	result, err := s.opsRepo.GetErrorTrend(ctx, filter, bucketSeconds)
	// Automatic-mode errors trigger a new complete query with the same exclusions
	// and dimensions; the shallow copy changes source selection and discards the
	// failed attempt.
	if err != nil && ShouldFallbackOpsPreagg(filter, err) {
		rawFilter := CloneOpsFilterWithMode(filter, OpsQueryModeRaw)
		return s.opsRepo.GetErrorTrend(ctx, rawFilter, bucketSeconds)
	}
	return result, err
}

// GetErrorDistribution returns status-grouped error counts after dashboard
// validation and runtime SLA-policy resolution. It updates the supplied filter
// source preference and exclusions, and retries an automatic-mode error with a
// shallow raw filter copy before returning that result.
func (s *OpsService) GetErrorDistribution(ctx context.Context, filter *OpsDashboardFilter) (*OpsErrorDistributionResponse, error) {
	if err := s.validateDashboardQuery(ctx, filter); err != nil {
		return nil, err
	}
	s.resolveOpsQueryModeWithIgnoredStatusCodes(ctx, filter)

	result, err := s.opsRepo.GetErrorDistribution(ctx, filter)
	// Automatic-mode errors trigger a new complete query with the same exclusions
	// and dimensions; the shallow copy changes source selection and discards the
	// failed attempt.
	if err != nil && ShouldFallbackOpsPreagg(filter, err) {
		rawFilter := CloneOpsFilterWithMode(filter, OpsQueryModeRaw)
		return s.opsRepo.GetErrorDistribution(ctx, rawFilter)
	}
	return result, err
}
