package pricing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRequestPriceSeparatesLabelsAndContext checks that labels and context ranges select separate request tiers.
func TestRequestPriceSeparatesLabelsAndContext(t *testing.T) {
	zero, low, high := 0.0, 0.03, 0.1
	maxTokens := 128000
	resolved := &ResolvedPricing{Mode: BillingModePerRequest, RequestTiers: []PricingInterval{
		{TierLabel: "1K", PerRequestPrice: &zero},
		{MinTokens: 0, MaxTokens: &maxTokens, PerRequestPrice: &low},
		{MinTokens: maxTokens, PerRequestPrice: &high},
	}}
	_, ok := GetRequestTierPriceValue(resolved, "")
	require.False(t, ok)
	count := 200000
	value, ok := ResolveRequestUnitPrice(resolved, "2K", &count)
	require.True(t, ok)
	require.Equal(t, high, value)
	value, ok = ResolveRequestUnitPrice(resolved, "1K", nil)
	require.True(t, ok)
	require.Zero(t, value)
	_, ok = ResolveRequestUnitPrice(resolved, "2K", nil)
	require.False(t, ok)
}

// TestRequestMissingDefaultIsNotFree distinguishes zero prices and multipliers from an absent default price.
func TestRequestMissingDefaultIsNotFree(t *testing.T) {
	count := 0
	_, ok := ResolveRequestUnitPrice(&ResolvedPricing{Mode: BillingModePerRequest}, "", &count)
	require.False(t, ok)
	price, ok := ResolveRequestUnitPrice(&ResolvedPricing{Mode: BillingModePerRequest, DefaultPerRequestPricePresent: true}, "", &count)
	require.True(t, ok)
	require.Zero(t, price)
	unit, multiplier := 0.2, 0.0
	resolved := ResolvePriceCards(&ModelPricingEntry{BillingMode: BillingModePerRequest, PerRequestPrice: &unit, PriceMultiplier: &multiplier}, nil, PricingSourceUnpriced, true)
	price, ok = ResolveRequestUnitPrice(resolved, "", &count)
	require.True(t, ok)
	require.Zero(t, price)
}

// TestConstantRequestIntervalsKeepExplicitFreeQuote checks that equal context prices permit a fixed quote, including zero.
func TestConstantRequestIntervalsKeepExplicitFreeQuote(t *testing.T) {
	base, tier, multiplier := 0.2, 0.4, 0.0
	resolved := ResolvePriceCards(&ModelPricingEntry{BillingMode: BillingModePerRequest, PerRequestPrice: &base, PriceMultiplier: &multiplier, Intervals: []PricingInterval{{MinTokens: 0, PerRequestPrice: &tier}}}, nil, PricingSourceUnpriced, true)
	price, ok := ResolveRequestUnitPrice(resolved, "2K", nil)
	require.True(t, ok)
	require.Zero(t, price)
}
