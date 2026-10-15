package ops

import (
	"context"
	"fmt"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// histograms.go validates latency bucket thresholds and requests histogram
// data for dashboard filters.
// Latency histograms use five ordered millisecond thresholds to form six
// displayed ranges. Empty input selects the shared defaults, invalid thresholds
// fail before storage runs, and returned boundary metadata has its own slice so
// clients can retain their chosen range settings.

// Histogram validation requires five positive, increasing millisecond cutoffs
// and limits each cutoff to one day of recorded latency.
const (
	opsLatencyBucketBoundaryCount = 5                 // required interior cutoff count
	opsLatencyBucketBoundaryMaxMS = int64(86_400_000) // milliseconds
)

// defaultOpsLatencyBucketBoundariesMS holds the five default millisecond
// cutoffs. Consumers obtain a copy through DefaultOpsLatencyBucketBoundariesMS
// before changing them.
var defaultOpsLatencyBucketBoundariesMS = []int64{100, 200, 500, 1000, 2000}

// DefaultOpsLatencyBucketBoundariesMS returns an independent copy of the
// default millisecond cutoffs. Callers can change the returned slice without
// altering the defaults later used for empty histogram settings.
func DefaultOpsLatencyBucketBoundariesMS() []int64 {
	return append([]int64(nil), defaultOpsLatencyBucketBoundariesMS...)
}

// NormalizeOpsLatencyBucketBoundariesMS returns copied, validated millisecond
// cutoffs, using defaults for nil or empty input. Supplied values must contain
// exactly five positive, strictly increasing thresholds no larger than one day;
// the helper rejects invalid input without sorting it.
func NormalizeOpsLatencyBucketBoundariesMS(boundaries []int64) ([]int64, error) {
	if len(boundaries) == 0 {
		return DefaultOpsLatencyBucketBoundariesMS(), nil
	}
	// Five interior cutoffs produce the six displayed duration ranges.
	if len(boundaries) != opsLatencyBucketBoundaryCount {
		return nil, fmt.Errorf("exactly %d bucket boundaries are required", opsLatencyBucketBoundaryCount)
	}

	normalized := append([]int64(nil), boundaries...)
	for i, boundary := range normalized {
		// The maximum cutoff represents one day of measured latency.
		if boundary <= 0 || boundary > opsLatencyBucketBoundaryMaxMS {
			return nil, fmt.Errorf("bucket boundary must be between 1 and %d milliseconds", opsLatencyBucketBoundaryMaxMS)
		}
		// Increasing cutoffs give the repository six nonoverlapping duration ranges.
		if i > 0 && boundary <= normalized[i-1] {
			return nil, fmt.Errorf("bucket boundaries must be strictly increasing")
		}
	}
	return normalized, nil
}

// GetLatencyHistogram validates the dashboard range and cutoffs before querying
// duration buckets. It resolves QueryMode in the supplied filter and retries an
// automatic-mode error with a raw copy. Any returned response receives copied
// normalized boundary metadata, including when the repository also returns an
// error.
func (s *OpsService) GetLatencyHistogram(ctx context.Context, filter *OpsDashboardFilter, bucketBoundariesMS []int64) (*OpsLatencyHistogramResponse, error) {
	if err := s.validateDashboardQuery(ctx, filter); err != nil {
		return nil, err
	}
	boundaries, err := NormalizeOpsLatencyBucketBoundariesMS(bucketBoundariesMS)
	if err != nil {
		return nil, infraerrors.BadRequest("OPS_LATENCY_BUCKET_BOUNDARIES_INVALID", err.Error())
	}
	filter.QueryMode = s.resolveOpsQueryMode(ctx, filter.QueryMode)

	result, err := s.opsRepo.GetLatencyHistogram(ctx, filter, boundaries)
	// A failed automatic-mode call is retried as a complete raw query with the
	// same validated cutoffs, so the response does not mix two attempts.
	if err != nil && ShouldFallbackOpsPreagg(filter, err) {
		rawFilter := CloneOpsFilterWithMode(filter, OpsQueryModeRaw)
		result, err = s.opsRepo.GetLatencyHistogram(ctx, rawFilter, boundaries)
	}
	if result != nil {
		// Give the response its own cutoff slice; later edits to response settings
		// cannot alter the normalized thresholds supplied to storage.
		result.BucketBoundariesMS = append([]int64(nil), boundaries...)
	}
	return result, err
}
