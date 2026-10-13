package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// The local provider statistics adapter can expose combined windows and
// multi-provider batches from one usage source. A counting paired read supplies
// distinct totals for each window, making preserved capability and cost-field
// conversion visible to the assembly scenario.

// windowPairSource supplies combined window and batch capabilities for usage
// adapter tests.
type windowPairSource struct {
	// UsageLogRepository supplies unused methods; this fixture implements pair
	// and batch reads so construction can expose both provider statistics capabilities.
	usage.UsageLogRepository
	pairs int // completed paired queries
}

// GetProviderWindowStatsPair records one combined query and returns distinct
// window totals.
func (s *windowPairSource) GetProviderWindowStatsPair(context.Context, int64, time.Time, time.Time) (*usage.ProviderStats, *usage.ProviderStats, error) {
	s.pairs++
	return &usage.ProviderStats{Requests: 1, Tokens: 2, Cost: 3, StandardCost: 4, UserCost: 5}, &usage.ProviderStats{Requests: 6}, nil
}

// GetProviderWindowStatsBatch returns a new empty map for this batch-capable
// fixture.
func (s *windowPairSource) GetProviderWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*usage.ProviderStats, error) {
	return map[int64]*usage.ProviderStats{}, nil
}

// TestProviderLocalStatsKeepsBothBatchCapabilities adapts one source
// implementing pair and batch queries. It checks both capabilities remain
// discoverable, the paired query executes once, and each window retains its
// supplied request, token, and cost fields.
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
