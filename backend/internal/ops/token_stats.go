package ops

import (
	"context"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Model statistics can return either leading models or a paginated ranking, and
// positive requests for both modes are rejected. Monitoring and time validation
// precede those limits; pagination defaults are written into the supplied
// filter before the repository groups the saved usage observations.

// GetTokenStats returns model-grouped usage measurements after monitoring,
// repository, and ordered nonzero-time validation. A supplied group identity
// must be positive. Positive TopN accepts up to one hundred models and
// conflicts with positive pagination values; otherwise nonpositive Page and
// PageSize become one and twenty in the caller filter, with page size limited
// to one hundred.
func (s *OpsService) GetTokenStats(ctx context.Context, filter *OpsTokenStatsFilter) (*OpsTokenStatsResponse, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if filter == nil {
		return nil, infraerrors.BadRequest("OPS_FILTER_REQUIRED", "filter is required")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_REQUIRED", "start_time/end_time are required")
	}
	if filter.StartTime.After(filter.EndTime) {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_INVALID", "start_time must be <= end_time")
	}

	if filter.GroupID != nil && *filter.GroupID <= 0 {
		return nil, infraerrors.BadRequest("OPS_GROUP_ID_INVALID", "group_id must be > 0")
	}

	// Storage chooses limit-only SQL for TopN and limit/offset SQL for pagination,
	// so one request supplies a single result-limiting mode.
	if filter.TopN > 0 && (filter.Page > 0 || filter.PageSize > 0) {
		return nil, infraerrors.BadRequest("OPS_PAGINATION_CONFLICT", "top_n cannot be used with page/page_size")
	}

	if filter.TopN > 0 {
		// The leading-model result is bounded to one hundred grouped rows.
		if filter.TopN < 1 || filter.TopN > 100 {
			return nil, infraerrors.BadRequest("OPS_TOPN_INVALID", "top_n must be between 1 and 100")
		}
	} else {
		if filter.Page <= 0 {
			filter.Page = 1
		}
		if filter.PageSize <= 0 {
			filter.PageSize = 20
		}
		if filter.Page < 1 {
			return nil, infraerrors.BadRequest("OPS_PAGE_INVALID", "page must be >= 1")
		}
		// Paginated SQL also returns at most one hundred grouped model rows.
		if filter.PageSize < 1 || filter.PageSize > 100 {
			return nil, infraerrors.BadRequest("OPS_PAGE_SIZE_INVALID", "page_size must be between 1 and 100")
		}
	}

	return s.opsRepo.GetTokenStats(ctx, filter)
}
