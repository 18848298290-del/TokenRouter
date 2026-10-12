package ops

import (
	"context"
	"strings"
)

func (s *OpsService) ListRequestDetails(ctx context.Context, filter *OpsRequestDetailFilter) (*OpsRequestDetailList, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
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

// LookupRequestTimings returns http.access timing metadata for the requested IDs.
// It trims clientRequestIDs in place and returns an empty result when Ops is disabled or the repository is nil.
func (s *OpsService) LookupRequestTimings(ctx context.Context, clientRequestIDs []string) (map[string]*OpsRequestTiming, error) {
	result := make(map[string]*OpsRequestTiming)
	if s == nil || s.opsRepo == nil || len(clientRequestIDs) == 0 || !s.IsMonitoringEnabled(ctx) {
		return result, nil
	}
	for i := range clientRequestIDs {
		clientRequestIDs[i] = strings.TrimSpace(clientRequestIDs[i])
	}
	return s.opsRepo.ListRequestTimings(ctx, clientRequestIDs)
}
