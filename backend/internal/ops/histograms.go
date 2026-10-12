package ops

import (
	"context"
	"fmt"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	opsLatencyBucketBoundaryCount = 5
	opsLatencyBucketBoundaryMaxMS = int64(86_400_000)
)

var defaultOpsLatencyBucketBoundariesMS = []int64{100, 200, 500, 1000, 2000}

// DefaultOpsLatencyBucketBoundariesMS returns a copy of the default duration thresholds in milliseconds.
func DefaultOpsLatencyBucketBoundariesMS() []int64 {
	return append([]int64(nil), defaultOpsLatencyBucketBoundariesMS...)
}

// NormalizeOpsLatencyBucketBoundariesMS validates and copies duration thresholds, using defaults for empty input.
func NormalizeOpsLatencyBucketBoundariesMS(boundaries []int64) ([]int64, error) {
	if len(boundaries) == 0 {
		return DefaultOpsLatencyBucketBoundariesMS(), nil
	}
	if len(boundaries) != opsLatencyBucketBoundaryCount {
		return nil, fmt.Errorf("exactly %d bucket boundaries are required", opsLatencyBucketBoundaryCount)
	}

	normalized := append([]int64(nil), boundaries...)
	for i, boundary := range normalized {
		if boundary <= 0 || boundary > opsLatencyBucketBoundaryMaxMS {
			return nil, fmt.Errorf("bucket boundary must be between 1 and %d milliseconds", opsLatencyBucketBoundaryMaxMS)
		}
		if i > 0 && boundary <= normalized[i-1] {
			return nil, fmt.Errorf("bucket boundaries must be strictly increasing")
		}
	}
	return normalized, nil
}

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
	if err != nil && ShouldFallbackOpsPreagg(filter, err) {
		rawFilter := CloneOpsFilterWithMode(filter, OpsQueryModeRaw)
		result, err = s.opsRepo.GetLatencyHistogram(ctx, rawFilter, boundaries)
	}
	if result != nil {
		result.BucketBoundariesMS = append([]int64(nil), boundaries...)
	}
	return result, err
}
