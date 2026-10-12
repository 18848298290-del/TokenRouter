package pricing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRequestPriceSeparatesLabelsAndContext combines a free size label with
// paid context tiers. Known context selects its tier, the free label wins
// independently, and an unknown unmatched label remains unavailable.
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

// TestRequestMissingDefaultIsNotFree compares an absent default, a present zero
// default, and a zero-scaled paid default. Only the latter two must return a
// present free price.
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

// TestConstantRequestIntervalsKeepExplicitFreeQuote scales different default
// and context amounts by zero. The resulting equal free prices must permit a
// fixed quote when context is unknown.
func TestConstantRequestIntervalsKeepExplicitFreeQuote(t *testing.T) {
	base, tier, multiplier := 0.2, 0.4, 0.0
	resolved := ResolvePriceCards(&ModelPricingEntry{BillingMode: BillingModePerRequest, PerRequestPrice: &base, PriceMultiplier: &multiplier, Intervals: []PricingInterval{{MinTokens: 0, PerRequestPrice: &tier}}}, nil, PricingSourceUnpriced, true)
	price, ok := ResolveRequestUnitPrice(resolved, "2K", nil)
	require.True(t, ok)
	require.Zero(t, price)
}
