package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	usagecore "github.com/TokenFlux/TokenRouter/internal/usage"
)

// Gemini local quota display tracks provider cost independently of user
// charges. Two model rows deliberately supply different actual and provider
// amounts, allowing request, token, and model-family totals to expose an
// incorrect cost-field choice in the adapter.

// TestGeminiAggregateUsageUsesProviderCost supplies Pro and Flash usage with
// user charges different from provider cost. It checks family request/token
// totals and provider-cost amounts within the numeric comparison tolerance.
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
	// Compare floating-point aggregated costs within a one-millionth numeric tolerance.
	require.InDelta(t, 10, totals.ProCost, 0.000001)
	require.InDelta(t, 2, totals.FlashCost, 0.000001)
}
