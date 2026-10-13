package ops

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// latencyHistogramRepoStub captures histogram cutoffs while returning a
// successful empty response.
type latencyHistogramRepoStub struct {
	// OpsRepository supplies unused methods through its nil embedded interface.
	// These tests call the histogram override; an unexpected other method call
	// needs a configured implementation.
	OpsRepository // unused embedded repository methods
	// capturedBoundaries is replaced with a copy during each histogram call. Test
	// assertions can inspect the captured values independently of the caller
	// slice.
	capturedBoundaries []int64 // copied requested histogram cutoffs
}

// GetLatencyHistogram copies supplied cutoffs into capturedBoundaries and
// returns an empty successful histogram response. The test stub ignores context
// and filter selections so assertions isolate the service cutoff validation and
// response metadata.
func (s *latencyHistogramRepoStub) GetLatencyHistogram(_ context.Context, _ *OpsDashboardFilter, boundaries []int64) (*OpsLatencyHistogramResponse, error) {
	s.capturedBoundaries = append([]int64(nil), boundaries...)
	return &OpsLatencyHistogramResponse{}, nil
}

// TestNormalizeOpsLatencyBucketBoundariesMS supplies defaults, ordered custom
// cutoffs, and malformed cutoff lists. It checks accepted values and rejection
// of wrong counts, zero thresholds, duplicates, and values above one day.
func TestNormalizeOpsLatencyBucketBoundariesMS(t *testing.T) {
	require.Equal(t, []int64{100, 200, 500, 1000, 2000}, DefaultOpsLatencyBucketBoundariesMS())

	valid := []int64{1000, 2000, 5000, 10000, 20000}
	normalized, err := NormalizeOpsLatencyBucketBoundariesMS(valid)
	require.NoError(t, err)
	require.Equal(t, valid, normalized)

	invalid := [][]int64{
		{100, 200},
		{0, 200, 500, 1000, 2000},
		{100, 200, 200, 1000, 2000},
		{100, 200, 500, 1000, 86_400_001},
	}
	for _, boundaries := range invalid {
		_, err := NormalizeOpsLatencyBucketBoundariesMS(boundaries)
		require.Error(t, err, "boundaries=%v", boundaries)
	}
}

// TestOpsServiceGetLatencyHistogram_UsesDefaultBoundaries requests a valid time
// range with no cutoff list. It checks that storage receives the defaults and
// the same values appear in returned boundary metadata.
func TestOpsServiceGetLatencyHistogram_UsesDefaultBoundaries(t *testing.T) {
	now := time.Now().UTC()
	repo := &latencyHistogramRepoStub{}
	service := &OpsService{opsRepo: repo}

	response, err := service.GetLatencyHistogram(context.Background(), &OpsDashboardFilter{
		StartTime: now.Add(-time.Hour),
		EndTime:   now,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, DefaultOpsLatencyBucketBoundariesMS(), repo.capturedBoundaries)
	require.Equal(t, DefaultOpsLatencyBucketBoundariesMS(), response.BucketBoundariesMS)
}

// TestOpsServiceGetLatencyHistogram_RejectsInvalidBoundaries submits a valid
// range with duplicate cutoff values. It protects the service HTTP-error
// classification and the dedicated invalid-boundaries reason.
func TestOpsServiceGetLatencyHistogram_RejectsInvalidBoundaries(t *testing.T) {
	now := time.Now().UTC()
	service := &OpsService{opsRepo: &latencyHistogramRepoStub{}}

	_, err := service.GetLatencyHistogram(context.Background(), &OpsDashboardFilter{
		StartTime: now.Add(-time.Hour),
		EndTime:   now,
	}, []int64{100, 200, 200, 1000, 2000})
	require.Error(t, err)
	require.Equal(t, 400, httpx.ErrorCode(err))
	require.Equal(t, "OPS_LATENCY_BUCKET_BOUNDARIES_INVALID", apperror.Reason(err))
}
