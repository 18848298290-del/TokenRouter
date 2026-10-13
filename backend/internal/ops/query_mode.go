package ops

import (
	"context"
	"errors"
)

// Hourly overview totals carry the exclusion policy used when they were
// computed, with no status-code dimension for recalculation at query time.
// Query filters carry an internal source preference alongside the client-status
// exclusions. Services resolve the aggregation switch first and force
// original-row queries when the normalized exclusions differ from defaults.
//
// Automatic-mode errors permit a complete raw retry with a shallow filter copy.
// The retry decision accepts every query error, so it also applies to
// repository operations whose current implementation already reads raw rows.
// Copying the filter isolates its scalar mode while retaining referenced IDs
// and slices; callers treat those shared values as read-only.

// Query modes identify the repository data-source preference carried by internal
// dashboard callers; automatic overview queries may use covered hourly buckets.
const (
	OpsQueryModeAuto OpsQueryMode = "auto" // permit automatic overview selection
	OpsQueryModeRaw  OpsQueryMode = "raw"  // request original observation rows
)

// ErrOpsPreaggregatedNotPopulated marks missing aggregate coverage so overview
// callers can select the original observation rows for the complete requested
// range.
var ErrOpsPreaggregatedNotPopulated = errors.New("ops pre-aggregated tables not populated")

// OpsQueryMode names an internal dashboard data-source preference carried
// alongside its filters.
type OpsQueryMode string

// IsValid reports whether the mode names automatic selection or original-row
// queries.
func (m OpsQueryMode) IsValid() bool {
	switch m {
	case OpsQueryModeAuto, OpsQueryModeRaw:
		return true
	default:
		return false
	}
}

// ShouldFallbackOpsPreagg reports whether an automatic-mode query returned any
// error. Nil filters, successful queries, and other modes return false; the
// error need not match the missing-coverage sentinel.
func ShouldFallbackOpsPreagg(filter *OpsDashboardFilter, err error) bool {
	return filter != nil &&
		filter.QueryMode == OpsQueryModeAuto &&
		err != nil
}

// CloneOpsFilterWithMode returns a shallow filter copy with the supplied query
// mode. Nil input returns nil; referenced IDs and slices remain shared, so
// callers change scalar fields or replace collections on their copy.
func CloneOpsFilterWithMode(filter *OpsDashboardFilter, mode OpsQueryMode) *OpsDashboardFilter {
	if filter == nil {
		return nil
	}
	cloned := *filter
	cloned.QueryMode = mode
	return &cloned
}

// OpsIgnoredStatusCodesEqual compares exclusion policies after sorting and
// deduplication. Nil inputs select defaults, while empty slices represent no
// exclusions; neither supplied slice is reordered in place.
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

// resolveOpsQueryModeWithIgnoredStatusCodes updates a filter using deployment
// and runtime aggregation policy, then applies status exclusions. Nil filters
// are ignored, and a custom exclusion policy can override the selected mode to
// raw.
func (s *OpsService) resolveOpsQueryModeWithIgnoredStatusCodes(ctx context.Context, filter *OpsDashboardFilter) {
	if filter == nil {
		return
	}
	filter.QueryMode = s.resolveOpsQueryMode(ctx, filter.QueryMode)
	s.applyOpsIgnoredStatusCodes(ctx, filter)
}

// applyOpsIgnoredStatusCodes normalizes a supplied status policy or reads the
// current advanced setting for a nil policy. It mutates the filter's slice
// reference and can force raw queries when the normalized policy differs from
// defaults. Empty input remains an intentional policy with no exclusions.
//
// Hourly buckets record totals computed during aggregation and do not retain a
// status-code dimension for recalculating exclusions. The current routing check
// compares with default exclusions, so custom policies select original rows
// even when automatic aggregation would otherwise be enabled. A nil filter
// requires no work.
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
		// For a client 418 error with no upstream context, excluding 418 changes its
		// SLA category. Raw predicates can apply that policy to retained
		// observations.
		filter.QueryMode = OpsQueryModeRaw
	}
}
