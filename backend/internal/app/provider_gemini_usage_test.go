package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	usagecore "github.com/TokenFlux/TokenRouter/internal/usage"
)

func TestGeminiAggregateUsageUsesProviderCost(t *testing.T) {
	// Gemini local usage uses provider cost even when the user charge differs.
	stats := []usagecore.ModelStat{
		{
			Model:        "gemini-2.5-pro",
			Requests:     2,
			TotalTokens:  300,
			ActualCost:   500,
			ProviderCost: 10,
		},
		{
			Model:        "gemini-2.5-flash",
			Requests:     3,
			TotalTokens:  400,
			ActualCost:   100,
			ProviderCost: 2,
		},
	}

	totals := provider.AggregateGeminiUsage(projectGeminiModelUsage(stats))

	require.Equal(t, int64(2), totals.ProRequests)
	require.Equal(t, int64(3), totals.FlashRequests)
	require.Equal(t, int64(300), totals.ProTokens)
	require.Equal(t, int64(400), totals.FlashTokens)
	require.InDelta(t, 10, totals.ProCost, 0.000001)
	require.InDelta(t, 2, totals.FlashCost, 0.000001)
}
