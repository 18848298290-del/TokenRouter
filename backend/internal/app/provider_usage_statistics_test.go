package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// windowPairSource supports paired-window and multi-provider batch queries for assembly tests.
type windowPairSource struct {
	usage.UsageLogRepository
	pairs int
}

// GetProviderWindowStatsPair uses distinct cost values to detect mapping errors.
func (s *windowPairSource) GetProviderWindowStatsPair(context.Context, int64, time.Time, time.Time) (*usage.ProviderStats, *usage.ProviderStats, error) {
	s.pairs++
	return &usage.ProviderStats{Requests: 1, Tokens: 2, Cost: 3, StandardCost: 4, UserCost: 5}, &usage.ProviderStats{Requests: 6}, nil
}

// GetProviderWindowStatsBatch exposes batch-query support to the adapter.
func (s *windowPairSource) GetProviderWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*usage.ProviderStats, error) {
	return map[int64]*usage.ProviderStats{}, nil
}

// TestProviderLocalStatsKeepsBothBatchCapabilities checks that the adapter exposes both batch interfaces.
func TestProviderLocalStatsKeepsBothBatchCapabilities(t *testing.T) {
	source := &windowPairSource{}
	reader := newProviderLocalUsageStats(source)
	_, ok := reader.(provider.LocalUsageStatsBatch)
	require.True(t, ok)
	pair, ok := reader.(provider.LocalUsageStatsPair)
	require.True(t, ok)
	a, b, err := pair.GetProviderWindowStatsPair(context.Background(), 1, time.Now(), time.Now())
	require.NoError(t, err)
	require.Equal(t, &provider.WindowStats{Requests: 1, Tokens: 2, Cost: 3, StandardCost: 4, UserCost: 5}, a)
	require.EqualValues(t, 6, b.Requests)
	require.Equal(t, 1, source.pairs)
}
