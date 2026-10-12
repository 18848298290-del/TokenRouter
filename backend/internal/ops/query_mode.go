package ops

import (
	"context"
	"errors"
)

const (
	OpsQueryModeAuto OpsQueryMode = "auto"
	OpsQueryModeRaw  OpsQueryMode = "raw"
)

// ErrOpsPreaggregatedNotPopulated indicates incomplete aggregate coverage for the requested window.
var ErrOpsPreaggregatedNotPopulated = errors.New("ops pre-aggregated tables not populated")

type OpsQueryMode string

func (m OpsQueryMode) IsValid() bool {
	switch m {
	case OpsQueryModeAuto, OpsQueryModeRaw:
		return true
	default:
		return false
	}
}

func ShouldFallbackOpsPreagg(filter *OpsDashboardFilter, err error) bool {
	return filter != nil &&
		filter.QueryMode == OpsQueryModeAuto &&
		err != nil
}

func CloneOpsFilterWithMode(filter *OpsDashboardFilter, mode OpsQueryMode) *OpsDashboardFilter {
	if filter == nil {
		return nil
	}
	cloned := *filter
	cloned.QueryMode = mode
	return &cloned
}

func OpsIgnoredStatusCodesEqual(a, b []int) bool {
	a = NormalizeOpsIgnoredStatusCodes(a)
	b = NormalizeOpsIgnoredStatusCodes(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *OpsService) resolveOpsQueryModeWithIgnoredStatusCodes(ctx context.Context, filter *OpsDashboardFilter) {
	if filter == nil {
		return
	}
	filter.QueryMode = s.resolveOpsQueryMode(ctx, filter.QueryMode)
	s.applyOpsIgnoredStatusCodes(ctx, filter)
}

func (s *OpsService) applyOpsIgnoredStatusCodes(ctx context.Context, filter *OpsDashboardFilter) {
	if filter == nil {
		return
	}
	if filter.IgnoredStatusCodes == nil {
		filter.IgnoredStatusCodes = s.resolveOpsIgnoredStatusCodes(ctx)
	} else {
		filter.IgnoredStatusCodes = NormalizeOpsIgnoredStatusCodes(filter.IgnoredStatusCodes)
	}
	if !OpsIgnoredStatusCodesEqual(filter.IgnoredStatusCodes, DefaultOpsIgnoredStatusCodes()) {
		// Custom ignored statuses require raw queries because aggregates use a fixed status policy.
		filter.QueryMode = OpsQueryModeRaw
	}
}
