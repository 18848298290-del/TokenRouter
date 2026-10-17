package ops

import (
	"context"
)

// request_details.go normalizes request-detail filters and delegates drilldown
// and timing queries to storage.
// Request drilldown combines usage observations with client-error observations,
// while access-log timing lookup reads a separate diagnostic source. Drilldown
// normalizes a copied filter and SLA policy; timing lookup trims supplied
// identifiers in place and can return an empty map when monitoring is
// unavailable.

// ListRequestDetails returns combined observation rows after monitoring
// validation, using normalized times and pagination in a shallow filter copy.
// Nil status exclusions select runtime policy, while supplied exclusions are
// normalized separately. Missing storage returns an empty page with fixed
// default metadata regardless of the requested filter; nil repository items
// become an allocated empty slice.
func (s *OpsService) ListRequestDetails(ctx context.Context, filter *OpsRequestDetailFilter) (*OpsRequestDetailList, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		// Absent storage is represented as a successful empty drilldown page. The
		// fixed metadata is returned before normalization can inspect the caller
		// filter.
		return &OpsRequestDetailList{
			Items:    []*OpsRequestDetail{},
			Total:    0,
			Page:     1,
			PageSize: 50,
		}, nil
	}

	page, pageSize, startTime, endTime := filter.Normalize()
	filterCopy := &OpsRequestDetailFilter{}
	if filter != nil {
		*filterCopy = *filter
	}
	filterCopy.Page = page
	filterCopy.PageSize = pageSize
	filterCopy.StartTime = &startTime
	filterCopy.EndTime = &endTime
	if filterCopy.IgnoredStatusCodes == nil {
		filterCopy.IgnoredStatusCodes = s.resolveOpsIgnoredStatusCodes(ctx)
	} else {
		filterCopy.IgnoredStatusCodes = NormalizeOpsIgnoredStatusCodes(filterCopy.IgnoredStatusCodes)
	}

	items, total, err := s.opsRepo.ListRequestDetails(ctx, filterCopy)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []*OpsRequestDetail{}
	}

	return &OpsRequestDetailList{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}
