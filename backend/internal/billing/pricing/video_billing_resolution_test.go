package pricing

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVideoBillingLabelsAndDurations covers aliases, custom dimensions, and long
// output observations. Invalid labels remain distinguishable from omitted values,
// and every positive duration reaches the per-second multiplication unchanged.
func TestVideoBillingLabelsAndDurations(t *testing.T) {
	for input, want := range map[string]string{"480": "480p", "SD": "480p", "hd": "720p", "full-hd": "1080p", " fhd ": "1080p", "768P": "768p", "2K": "2k", "1440p": "1440p", " custom-output ": "custom-output", strings.Repeat("界", 50): strings.Repeat("界", 50)} {
		label, valid := LookupVideoBillingResolution(input)
		require.True(t, valid, input)
		require.Equal(t, want, label)
		require.Equal(t, want, NormalizeVideoBillingResolutionOrDefault(input))
	}
	for _, input := range []string{strings.Repeat("a", 51), "a\x00b", "a\nb"} {
		_, valid := LookupVideoBillingResolution(input)
		require.False(t, valid)
		require.Error(t, ValidateVideoBillingResolution(input))
		require.NotEqual(t, "480p", NormalizeVideoBillingResolutionOrDefault(input))
	}
	require.Equal(t, "480p", NormalizeVideoBillingResolutionOrDefault(" "))
	for _, seconds := range []int{1, 15, 16, 30, 60, 999} {
		cost := CalculateVideoCost(0.2, 2, seconds, 1.5)
		require.InDelta(t, float64(seconds)*0.2*2*1.5, cost.ActualCost, 1e-12)
	}
	for _, seconds := range []int{0, -1} {
		require.Equal(t, 8, NormalizeVideoBillingDurationSecondsOrDefault(seconds))
	}
}

// TestVideoPriceTablesValidateCanonicalLabels checks custom catalog dimensions and
// collisions across aliases. Invalid amounts and labels reject the complete table,
// while missing resolutions stay unavailable even when 480p has a price.
func TestVideoPriceTablesValidateCanonicalLabels(t *testing.T) {
	prices, err := NormalizeVideoPrices(MediaPrices{" 768P ": 0.12, "2K": 0, "HD": 0.2})
	require.NoError(t, err)
	require.Equal(t, MediaPrices{"768p": 0.12, "2k": 0, "720p": 0.2}, prices)
	for _, invalid := range []MediaPrices{{"HD": 1, "720p": 1}, {" ": 1}, {strings.Repeat("a", 51): 1}, {"768p": -1}, {"2k": math.Inf(1)}, {"2k": math.NaN()}} {
		_, err := NormalizeVideoPrices(invalid)
		require.Error(t, err)
		require.Error(t, (CatalogRules{VideoPrices: invalid}).Validate())
	}
	raw := &CatalogModelPricing{CatalogRules: CatalogRules{VideoPrices: MediaPrices{"480p": 0.1}}}
	_, found := DefaultVideoPrice(raw, "2K")
	require.False(t, found)
	for _, tiers := range [][]PricingInterval{
		{{TierLabel: "HD"}, {TierLabel: "720p"}},
		{{TierLabel: strings.Repeat("a", 51)}},
	} {
		require.Error(t, ValidateIntervals(tiers, BillingModeVideo))
	}
	require.NoError(t, ValidateIntervals([]PricingInterval{{TierLabel: "768P"}, {TierLabel: "2K"}}, BillingModeVideo))
}
