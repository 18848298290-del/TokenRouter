package pricingcontract

import (
	"testing"

	"github.com/stretchr/testify/require"

	billingpricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
)

// Explicit priority token prices and a configured Fast multiplier are separate
// ways to price accelerated requests. The fixtures check precedence using
// local catalog amounts and keep full model names from supplying commercial
// rules that their catalog entries did not declare.

// TestOpenAIFastBillingUsesExplicitCatalogRates supplies three local models
// whose priority prices are twice their standard prices. It checks
// fast/priority alias equivalence and standard amounts for empty
// or default tiers.
func TestOpenAIFastBillingUsesExplicitCatalogRates(t *testing.T) {
	t.Parallel()

	// Model names must leave the catalog's explicit priority prices intact.
	catalog := map[string]*billingpricing.CatalogModelPricing{
		"gpt-5.4": {
			InputCostPerToken:               2.5e-6,
			InputCostPerTokenPriority:       5e-6,
			OutputCostPerToken:              15e-6,
			OutputCostPerTokenPriority:      30e-6,
			CacheReadInputTokenCost:         0.25e-6,
			CacheReadInputTokenCostPriority: 0.5e-6,
		},
		"gpt-5.5": {
			InputCostPerToken:               5e-6,
			InputCostPerTokenPriority:       10e-6,
			OutputCostPerToken:              30e-6,
			OutputCostPerTokenPriority:      60e-6,
			CacheReadInputTokenCost:         0.5e-6,
			CacheReadInputTokenCostPriority: 1e-6,
		},
		"gpt-5.6-sol": {
			InputCostPerToken:               5e-6,
			InputCostPerTokenPriority:       10e-6,
			OutputCostPerToken:              30e-6,
			OutputCostPerTokenPriority:      60e-6,
			CacheReadInputTokenCost:         0.5e-6,
			CacheReadInputTokenCostPriority: 1e-6,
		},
	}
	billing := billingtestkit.Calculator(newCatalogFixture(catalogFixture{pricingData: catalog}), nil)
	// One million tokens in each populated bucket turns the dollar-per-token
	// fixture rates into readily distinguishable dollar totals for Standard
	// and accelerated requests.
	tokens := billingpricing.UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000}

	standard := func(model string) *billingpricing.CostBreakdown {
		cost, err := billing.CalculateCost(model, tokens, 1)
		require.NoError(t, err)
		return cost
	}
	fast := func(model, tier string) *billingpricing.CostBreakdown {
		cost, err := billing.CalculateCostWithServiceTier(model, tokens, 1, tier)
		require.NoError(t, err)
		return cost
	}

	tests := []struct {
		model string  // full catalog model name
		ratio float64 // expected accelerated cost factor
	}{
		{model: "gpt-5.4", ratio: 2.0},
		{model: "gpt-5.5", ratio: 2.0},
		{model: "gpt-5.6-sol", ratio: 2.0},
	}
	for _, tt := range tests {
		t.Run(tt.model+"/fast", func(t *testing.T) {
			base := standard(tt.model)
			fastCost := fast(tt.model, "fast")
			// The USD bucket and total comparisons allow
			// token-rate multiplication rounding.
			require.InDelta(t, base.TotalCost*tt.ratio, fastCost.TotalCost, 1e-9,
				"fast total must be %.1fx standard", tt.ratio)
		})
		t.Run(tt.model+"/priority_alias", func(t *testing.T) {
			fastCost := fast(tt.model, "fast")
			priorityCost := fast(tt.model, "priority")
			// The USD bucket and total comparisons allow
			// token-rate multiplication rounding.
			require.InDelta(t, fastCost.TotalCost, priorityCost.TotalCost, 1e-12,
				"client alias fast must bill identically to priority")
			require.InDelta(t, standard(tt.model).TotalCost*tt.ratio, priorityCost.TotalCost, 1e-9)
		})
		t.Run(tt.model+"/no_tier_unchanged", func(t *testing.T) {
			base := standard(tt.model)
			noTier, err := billing.CalculateCostWithServiceTier(tt.model, tokens, 1, "")
			require.NoError(t, err)
			// The USD bucket and total comparisons allow
			// token-rate multiplication rounding.
			require.InDelta(t, base.TotalCost, noTier.TotalCost, 1e-12)
		})
		t.Run(tt.model+"/default_equals_standard", func(t *testing.T) {
			base := standard(tt.model)
			defaultCost, err := billing.CalculateCostWithServiceTier(tt.model, tokens, 1, "default")
			require.NoError(t, err)
			// The USD bucket and total comparisons allow
			// token-rate multiplication rounding.
			require.InDelta(t, base.TotalCost, defaultCost.TotalCost, 1e-12)
			require.InDelta(t, base.InputCost, defaultCost.InputCost, 1e-12)
			require.InDelta(t, base.OutputCost, defaultCost.OutputCost, 1e-12)
			require.InDelta(t, base.CacheReadCost, defaultCost.CacheReadCost, 1e-12)
		})
	}
}

// TestOpenAIFastBilling_FastMultiplierOverridesCatalogRates adds a 1.7 factor
// to a price record with explicit twofold priority rates. It checks that the
// factor wins, then removing it restores the explicit rates.
func TestOpenAIFastBilling_FastMultiplierOverridesCatalogRates(t *testing.T) {
	svc := billingtestkit.Calculator(nil, map[string]*billingpricing.ModelPricing{})
	t.Parallel()

	catalog := &billingpricing.ModelPricing{
		InputPricePerToken:             5e-6,
		InputPricePerTokenPriority:     10e-6,
		OutputPricePerToken:            30e-6,
		OutputPricePerTokenPriority:    60e-6,
		CacheReadPricePerToken:         0.5e-6,
		CacheReadPricePerTokenPriority: 1e-6,
	}
	pricing := catalog
	// These catalog rates use an absolute USD-per-token tolerance
	// for floating-point rounding.
	require.InDelta(t, 10e-6, pricing.InputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 60e-6, pricing.OutputPricePerTokenPriority, 1e-12)

	multiplier := 1.7
	pricing.FastMultiplier = &multiplier

	tokens := billingpricing.UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000}
	standard := svc.ComputeTokenBreakdown(pricing, tokens, 1, "", false)
	fast := svc.ComputeTokenBreakdown(pricing, tokens, 1, "fast", false)
	priority := svc.ComputeTokenBreakdown(pricing, tokens, 1, "priority", false)

	// The USD bucket and total comparisons allow
	// token-rate multiplication rounding.
	require.InDelta(t, standard.TotalCost*1.7, fast.TotalCost, 1e-9)
	require.InDelta(t, fast.TotalCost, priority.TotalCost, 1e-12)

	// The price record is copied by value. Clearing FastMultiplier on the copy
	// leaves the source pointer intact, so the next calculation checks
	// explicit priority pricing.
	withoutOverride := *pricing
	withoutOverride.FastMultiplier = nil
	enforced := svc.ComputeTokenBreakdown(&withoutOverride, tokens, 1, "fast", false)
	// The USD bucket and total comparisons allow
	// token-rate multiplication rounding.
	require.InDelta(t, standard.TotalCost*2, enforced.TotalCost, 1e-9,
		"without FastMultiplier use the explicit catalog prices")
}

// TestModelNameDoesNotCreateCommercialRules resolves four named catalog
// entries containing ordinary input and output prices. It checks absent
// commercial multipliers, absent cache-write pricing, and unchanged input cost
// for priority and flex tiers.
func TestModelNameDoesNotCreateCommercialRules(t *testing.T) {
	for _, model := range []string{"gpt-5.5", "gpt-5.6-sol", "claude-fable-5-1", "deepseek-v4-pro"} {
		raw := &billingpricing.CatalogModelPricing{InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6}
		price, err := billingpricing.ResolveModelPricing(model, raw)
		require.NoError(t, err)
		require.Nil(t, price.FastMultiplier)
		require.Nil(t, price.FlexMultiplier)
		require.Nil(t, price.MaxReasoningEffortMultiplier)
		require.Zero(t, price.CacheCreationPricePerToken)
		for _, tier := range []string{"priority", "flex"} {
			cost := billingpricing.ComputeTokenBreakdown(price, billingpricing.UsageTokens{InputTokens: 100}, 1, tier, false)
			// The USD bucket and total comparisons allow
			// token-rate multiplication rounding.
			require.InDelta(t, 100e-6, cost.TotalCost, 1e-12)
		}
	}
}
