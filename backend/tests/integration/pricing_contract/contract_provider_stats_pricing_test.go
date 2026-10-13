package pricingcontract

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Provider cost rules select an amount independently of the user's billable
// charge. These fixtures connect routing configuration with catalog token
// calculation, checking custom-rule priority, Qoder model identities, and the
// difference between an explicit free override and an unavailable cost
// that returns nil.

// TestTryModelFilePricing_Success supplies separate input and output prices
// for measured token usage. It checks that catalog provider-cost calculation
// returns the sum of both dollar buckets.
func TestTryModelFilePricing_Success(t *testing.T) {
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"claude-sonnet-4": {
			InputPricePerToken:  0.001,
			OutputPricePerToken: 0.002,
		},
	})
	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}
	result := bs.ModelFileStatsCost("claude-sonnet-4", tokens, "", "")
	require.NotNil(t, result)
	// The input and output buckets each contribute $0.10. Compare their dollar
	// sum with a small absolute tolerance for floating-point arithmetic.
	require.InDelta(t, 0.2, *result, 1e-12)
}

// TestTryModelFilePricing_AppliesLongContextPricing supplies usage above a
// catalog threshold with separate input and output factors. It checks the
// combined provider cost after input/cache scaling and output scaling.
func TestTryModelFilePricing_AppliesLongContextPricing(t *testing.T) {
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"gpt-5.6-sol": {
			InputPricePerToken:          0.001,
			OutputPricePerToken:         0.002,
			CacheReadPricePerToken:      0.0001,
			LongContextInputThreshold:   100,
			LongContextInputMultiplier:  2,
			LongContextOutputMultiplier: 1.5,
		},
	})
	tokens := pricing.UsageTokens{InputTokens: 101, OutputTokens: 10, CacheReadTokens: 5}

	result := bs.ModelFileStatsCost("gpt-5.6-sol", tokens, "", "")

	require.NotNil(t, result)
	// Input and cache reads use the 2x long-context rate; output uses 1.5x.
	// Compare provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 0.233, *result, 1e-12)
}

// TestTryModelFilePricing_AppliesServiceTierPricing gives the catalog explicit
// priority rates and a Flex factor. It checks standard, priority, and Flex
// totals across input, output, cache-write, and cache-read buckets.
func TestTryModelFilePricing_AppliesServiceTierPricing(t *testing.T) {
	flex := 0.5
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"gpt-5.6-sol": {
			FlexMultiplier:                     &flex,
			InputPricePerToken:                 0.001,
			InputPricePerTokenPriority:         0.002,
			OutputPricePerToken:                0.002,
			OutputPricePerTokenPriority:        0.004,
			CacheCreationPricePerToken:         0.003,
			CacheCreationPricePerTokenPriority: 0.006,
			CacheReadPricePerToken:             0.0005,
			CacheReadPricePerTokenPriority:     0.001,
		},
	})
	tokens := pricing.UsageTokens{
		InputTokens:         100,
		OutputTokens:        50,
		CacheCreationTokens: 20,
		CacheReadTokens:     10,
	}

	tests := []struct {
		name        string  // subtest pricing label
		serviceTier string  // forwarded pricing tier
		want        float64 // expected provider cost USD
	}{
		{name: "standard", serviceTier: "", want: 0.265},
		{name: "priority", serviceTier: "priority", want: 0.53},
		{name: "flex", serviceTier: "flex", want: 0.1325},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bs.ModelFileStatsCost("gpt-5.6-sol", tokens, tt.serviceTier, "")
			require.NotNil(t, result)
			// Compare provider USD amounts with an
			// absolute rounding tolerance.
			require.InDelta(t, tt.want, *result, 1e-12)
		})
	}
}

// TestTryModelFilePricing_CombinesPriorityAndLongContextPricing combines
// explicit priority rates with an exceeded input threshold. It checks the
// provider-cost total after applying the input and
// output long-context factors.
func TestTryModelFilePricing_CombinesPriorityAndLongContextPricing(t *testing.T) {
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"gpt-5.6-sol": {
			InputPricePerToken:                 0.001,
			InputPricePerTokenPriority:         0.002,
			OutputPricePerToken:                0.002,
			OutputPricePerTokenPriority:        0.004,
			CacheCreationPricePerToken:         0.003,
			CacheCreationPricePerTokenPriority: 0.006,
			CacheReadPricePerToken:             0.0005,
			CacheReadPricePerTokenPriority:     0.001,
			LongContextInputThreshold:          100,
			LongContextInputMultiplier:         2,
			LongContextOutputMultiplier:        1.5,
		},
	})
	tokens := pricing.UsageTokens{
		InputTokens:         101,
		OutputTokens:        10,
		CacheCreationTokens: 5,
		CacheReadTokens:     5,
	}

	result := bs.ModelFileStatsCost("gpt-5.6-sol", tokens, "priority", "")

	require.NotNil(t, result)
	// Apply priority prices before the 2x input and 1.5x output long-context
	// rates. Compare provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 0.534, *result, 1e-12)
}

// TestTryModelFilePricing_PricingNotFound uses an empty catalog with positive
// token usage for an unknown model. It checks that unavailable provider
// pricing returns nil to its consumer.
func TestTryModelFilePricing_PricingNotFound(t *testing.T) {
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{})
	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}
	result := bs.ModelFileStatsCost("nonexistent-model", tokens, "", "")
	require.Nil(t, result)
}

// TestTryModelFilePricing_NilFallback supplies a nil normalized price entry
// for the requested model. It checks that the calculator fixture produces no
// usable catalog provider cost for that entry.
func TestTryModelFilePricing_NilFallback(t *testing.T) {
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"claude-sonnet-4": nil,
	})
	tokens := pricing.UsageTokens{InputTokens: 100}
	result := bs.ModelFileStatsCost("claude-sonnet-4", tokens, "", "")
	require.Nil(t, result)
}

// TestTryModelFilePricing_ZeroCost supplies a priced model with every usage
// bucket empty. It checks that catalog provider-cost calculation represents
// the nonpositive result with nil.
func TestTryModelFilePricing_ZeroCost(t *testing.T) {
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"claude-sonnet-4": {
			InputPricePerToken:  0.001,
			OutputPricePerToken: 0.002,
		},
	})
	tokens := pricing.UsageTokens{}
	result := bs.ModelFileStatsCost("claude-sonnet-4", tokens, "", "")
	require.Nil(t, result)
}

// TestTryModelFilePricing_WithImageOutput includes image-output tokens inside
// the reported output count. It checks that image and remaining text output
// receive their own prices in the provider-cost total.
func TestTryModelFilePricing_WithImageOutput(t *testing.T) {
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"claude-sonnet-4": {
			InputPricePerToken:       0.001,
			OutputPricePerToken:      0.002,
			ImageOutputPricePerToken: 0.01,
		},
	})
	tokens := pricing.UsageTokens{
		InputTokens:       100,
		OutputTokens:      50,
		ImageOutputTokens: 10,
	}
	result := bs.ModelFileStatsCost("claude-sonnet-4", tokens, "", "")
	require.NotNil(t, result)
	// Reported ImageOutputTokens is included in OutputTokens. Charging only
	// the remaining output tokens as text makes the image portion contribute
	// once to provider cost. With 50 output tokens and 10 image-output tokens,
	// the provider prices 40 text tokens and 10 image tokens. Compare provider
	// USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 0.28, *result, 1e-12)
}

// TestTryModelFilePricing_WithCacheTokens supplies input, output, cache-write,
// and cache-read usage with separate prices. It checks that the returned
// provider amount includes all four dollar buckets.
func TestTryModelFilePricing_WithCacheTokens(t *testing.T) {
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"claude-sonnet-4": {
			InputPricePerToken:         0.001,
			OutputPricePerToken:        0.002,
			CacheCreationPricePerToken: 0.003,
			CacheReadPricePerToken:     0.0005,
		},
	})
	tokens := pricing.UsageTokens{
		InputTokens:         100,
		OutputTokens:        50,
		CacheCreationTokens: 200,
		CacheReadTokens:     300,
	}
	result := bs.ModelFileStatsCost("claude-sonnet-4", tokens, "", "")
	require.NotNil(t, result)
	// Cache creation and reads are separate priced observations here. Their
	// $0.60 and $0.15 amounts add to the $0.20 input/output total. Compare
	// provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 0.95, *result, 1e-12)
}

// TestResolveProviderStatsCost_NilPricingConfigService calls the statistics
// fixture with an absent configuration source and empty catalog. It checks
// that provider cost remains unavailable as a nil result.
func TestResolveProviderStatsCost_NilPricingConfigService(t *testing.T) {
	result := contractProviderStatsCost(
		context.Background(),
		nil, // No pricing configuration service.
		newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{}),
		"", 1, 1, "claude-sonnet-4", "",
		pricing.UsageTokens{InputTokens: 100}, 1, 0.5, "",
	)
	require.Nil(t, result)
}

// TestResolveProviderStatsCost_EmptyUpstreamModel supplies a loaded group
// configuration but no upstream identity. It checks that the statistics
// resolver returns nil for that incomplete lookup input.
func TestResolveProviderStatsCost_EmptyUpstreamModel(t *testing.T) {
	cs := newTestPricingConfigServiceForStats(t, &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}, 1, "")

	result := contractProviderStatsCost(
		context.Background(),
		cs,
		newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{}),
		"", 1, 1, "", "", // empty upstream model
		pricing.UsageTokens{InputTokens: 100}, 1, 0.5, "",
	)
	require.Nil(t, result)
}

// TestResolveProviderStatsCost_GetPricingConfigForGroupReturnsNil configures
// one group and queries a different group's provider cost. It checks that the
// missing group configuration produces a nil result.
func TestResolveProviderStatsCost_GetPricingConfigForGroupReturnsNil(t *testing.T) {
	cs := newTestPricingConfigServiceForStats(t, &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}, 1, "")

	result := contractProviderStatsCost(
		context.Background(),
		cs,
		newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{}),
		"", 1, 99, "claude-sonnet-4", "", // Group 99 has no pricing configuration.
		pricing.UsageTokens{InputTokens: 100}, 1, 0.5, "",
	)
	require.Nil(t, result)
}

// TestResolveProviderStatsCost_HitsCustomRule supplies a matching
// provider-cost card with no catalog calculator. It checks the manual token
// amount despite a priority tier and an unrelated supplied totalCost.
func TestResolveProviderStatsCost_HitsCustomRule(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
		ProviderStatsPricingRules: []routing.ProviderStatsPricingRule{
			{
				GroupIDs: []int64{10},
				Pricing: []routing.ModelPricingEntry{
					{
						ID:          100,
						Models:      []string{"claude-sonnet-4"},
						InputPrice:  testPtrFloat64(0.01),
						OutputPrice: testPtrFloat64(0.02),
					},
				},
			},
		},
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "anthropic")

	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}

	// A configured provider-cost card supplies its own final prices. Keeping
	// priority in the input checks that this branch does not layer catalog
	// tier prices onto them.
	result := contractProviderStatsCost(
		context.Background(),
		cs, nil,
		"", 1, 10, "claude-sonnet-4", "",
		tokens, 1, 999.0, "priority",
	)
	require.NotNil(t, result)
	// At these manual dollar-per-token rates, each observed bucket contributes
	// $1.00, independently of the supplied fixture totalCost. Compare provider
	// USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 2.0, *result, 1e-12)
}

// TestResolveProviderStatsCost_DoesNotUseUserPrice supplies loaded empty rules
// and a catalog-priced upstream model. It checks the catalog token amount
// while the fixture's supplied totalCost differs.
func TestResolveProviderStatsCost_DoesNotUseUserPrice(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "anthropic")

	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}

	result := contractProviderStatsCost(
		context.Background(),
		cs, newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
			"claude-sonnet-4": {InputPricePerToken: 0.001, OutputPricePerToken: 0.002},
		}),
		"", 1, 10, "claude-sonnet-4", "",
		tokens, 1, 0.75, "",
	)
	require.NotNil(t, result)
	// Compare provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 0.2, *result, 1e-12)
}

// TestResolveProviderStatsCost_NoCalculatorReturnsNil supplies a loaded
// configuration with no custom rules or calculator. It checks that the
// resolver returns nil when neither configured nor catalog calculation can
// supply a cost.
func TestResolveProviderStatsCost_NoCalculatorReturnsNil(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "anthropic")

	result := contractProviderStatsCost(
		context.Background(),
		cs, nil,
		"", 1, 10, "claude-sonnet-4", "",
		pricing.UsageTokens{}, 1, 0.0, "", // totalCost = 0
	)
	require.Nil(t, result)
}

// TestResolveProviderStatsCost_FallsBackTo模型目录 supplies a loaded group
// configuration with empty provider rules and a priced catalog model. It
// checks that the catalog token amount becomes the provider cost.
func TestResolveProviderStatsCost_FallsBackTo模型目录(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "anthropic")

	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"claude-sonnet-4": {
			InputPricePerToken:  0.001,
			OutputPricePerToken: 0.002,
		},
	})

	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}

	result := contractProviderStatsCost(
		context.Background(),
		cs, bs,
		"", 1, 10, "claude-sonnet-4", "",
		tokens, 1, 999.0, "",
	)
	require.NotNil(t, result)
	// The upstream catalog's two token buckets contribute $0.10 each; the
	// unrelated supplied totalCost leaves that provider amount unchanged.
	// Compare provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 0.2, *result, 1e-12)
}

// TestResolveProviderStatsCost_QoderRouteKeyWithoutManualPricingReturnsNil
// queries qmodel while the fixture prices a different Claude identity. It
// checks that the unpriced route key produces a nil provider-cost result.
func TestResolveProviderStatsCost_QoderRouteKeyWithoutManualPricingReturnsNil(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformQoder)

	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"claude-opus-4.8": {
			InputPricePerToken:  0.005,
			OutputPricePerToken: 0.025,
		},
	})

	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}

	result := contractProviderStatsCost(
		context.Background(),
		cs, bs,
		capability.PlatformQoder, 1, 10, "qmodel", "qwen3.7-plus",
		tokens, 1, 999.0, "",
	)
	require.Nil(t, result)
}

// TestResolveProviderStatsCost_QoderAliasUsesStandardUpstreamPricing pairs a
// Qoder request alias with a catalog-priced upstream identity. It checks that
// empty custom rules lead to the upstream model's token amount.
func TestResolveProviderStatsCost_QoderAliasUsesStandardUpstreamPricing(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformQoder)
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"gpt-5.4-mini": {
			InputPricePerToken:  0.001,
			OutputPricePerToken: 0.002,
		},
	})

	result := contractProviderStatsCost(
		context.Background(),
		cs, bs,
		capability.PlatformQoder, 1, 10, "gpt-5.4-mini", "qwen3.7-plus",
		pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1, 999.0, "",
	)
	require.NotNil(t, result)
	// Compare provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 0.2, *result, 1e-12)
}

// TestResolveProviderStatsCost_QoderCustomRuleCanMatchRequestedAliasAfterRouteKeyMiss
// gives a Qoder route a manual request-alias price and no calculator. It
// checks that alias matching supplies the configured token amount.
func TestResolveProviderStatsCost_QoderCustomRuleCanMatchRequestedAliasAfterRouteKeyMiss(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
		ProviderStatsPricingRules: []routing.ProviderStatsPricingRule{
			{
				GroupIDs: []int64{10},
				Pricing: []routing.ModelPricingEntry{
					{
						ID:          100,
						Models:      []string{"qwen3.7-plus"},
						InputPrice:  testPtrFloat64(0.01),
						OutputPrice: testPtrFloat64(0.02),
					},
				},
			},
		},
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformQoder)

	result := contractProviderStatsCost(
		context.Background(),
		cs, nil,
		capability.PlatformQoder, 1, 10, "qmodel", "qwen3.7-plus",
		pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1, 999.0, "",
	)

	require.NotNil(t, result)
	// Compare provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 2.0, *result, 1e-12)
}

// TestResolveProviderStatsCost_QoderGroupMappedRuleMatchesBeforeDifferentUpstream
// supplies distinct requested, group-mapped, and upstream names with one
// mapped-name card. It checks that the custom mapped-name
// price resolves successfully.
func TestResolveProviderStatsCost_QoderGroupMappedRuleMatchesBeforeDifferentUpstream(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
		ProviderStatsPricingRules: []routing.ProviderStatsPricingRule{
			{
				GroupIDs: []int64{10},
				Pricing: []routing.ModelPricingEntry{
					{
						ID:          100,
						Models:      []string{"qmodel"},
						InputPrice:  testPtrFloat64(0.01),
						OutputPrice: testPtrFloat64(0.02),
					},
				},
			},
		},
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformQoder)

	result := contractProviderStatsWithMapping(
		context.Background(),
		cs, nil,
		capability.PlatformQoder, 1, 10, "ultimate", "qwen3.7-plus", "qmodel",
		pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1, 999.0, "",
	)

	require.NotNil(t, result)
	// Compare provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 2.0, *result, 1e-12)
}

// TestResolveProviderStatsCost_QoderGroupMappedRuleMatchesWhenUpstreamFallsBackToRequested
// makes requested and upstream names equal while retaining a different group
// mapping. It checks that the mapped-name custom price remains reachable.
func TestResolveProviderStatsCost_QoderGroupMappedRuleMatchesWhenUpstreamFallsBackToRequested(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
		ProviderStatsPricingRules: []routing.ProviderStatsPricingRule{
			{
				GroupIDs: []int64{10},
				Pricing: []routing.ModelPricingEntry{
					{
						ID:          100,
						Models:      []string{"qmodel"},
						InputPrice:  testPtrFloat64(0.01),
						OutputPrice: testPtrFloat64(0.02),
					},
				},
			},
		},
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformQoder)

	result := contractProviderStatsWithMapping(
		context.Background(),
		cs, nil,
		capability.PlatformQoder, 1, 10, "qwen3.7-plus", "qwen3.7-plus", "qmodel",
		pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1, 999.0, "",
	)

	require.NotNil(t, result)
	// Compare provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 2.0, *result, 1e-12)
}

// TestResolveProviderStatsCost_QoderRequestedAliasRuleOverridesRouteKeyRule
// places a costly route-key card before a cheaper request-alias card. It
// checks that Qoder request-name preference selects the alias amount.
func TestResolveProviderStatsCost_QoderRequestedAliasRuleOverridesRouteKeyRule(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
		ProviderStatsPricingRules: []routing.ProviderStatsPricingRule{
			{
				GroupIDs: []int64{10},
				Pricing: []routing.ModelPricingEntry{
					{
						ID:          100,
						Models:      []string{"qmodel"},
						InputPrice:  testPtrFloat64(0.50),
						OutputPrice: testPtrFloat64(0.75),
					},
					{
						ID:          101,
						Models:      []string{"qwen3.7-plus"},
						InputPrice:  testPtrFloat64(0.01),
						OutputPrice: testPtrFloat64(0.02),
					},
				},
			},
		},
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformQoder)

	result := contractProviderStatsCost(
		context.Background(),
		cs, nil,
		capability.PlatformQoder, 1, 10, "qmodel", "qwen3.7-plus",
		pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1, 999.0, "",
	)

	require.NotNil(t, result)
	// Compare provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 2.0, *result, 1e-12)
}

// TestResolveProviderStatsCost_QoderBlankRuleDoesNotMaskLaterAliasRule places
// an unpriced alias card before a priced card in separate rules. It checks
// that lookup continues to the later effective amount.
func TestResolveProviderStatsCost_QoderBlankRuleDoesNotMaskLaterAliasRule(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
		ProviderStatsPricingRules: []routing.ProviderStatsPricingRule{
			{
				GroupIDs: []int64{10},
				Pricing: []routing.ModelPricingEntry{
					{
						ID:     100,
						Models: []string{"qwen3.7-plus"},
					},
				},
			},
			{
				GroupIDs: []int64{10},
				Pricing: []routing.ModelPricingEntry{
					{
						ID:          101,
						Models:      []string{"qwen3.7-plus"},
						InputPrice:  testPtrFloat64(0.01),
						OutputPrice: testPtrFloat64(0.02),
					},
				},
			},
		},
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformQoder)

	result := contractProviderStatsCost(
		context.Background(),
		cs, nil,
		capability.PlatformQoder, 1, 10, "qmodel", "qwen3.7-plus",
		pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1, 999.0, "",
	)

	require.NotNil(t, result)
	// Compare provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 2.0, *result, 1e-12)
}

// TestResolveProviderStatsCost_CustomRuleExplicitZeroOverridesTotalCost
// supplies an explicit zero input price for the Qoder alias. It checks a
// nonnil zero cost despite a positive supplied fixture totalCost.
func TestResolveProviderStatsCost_CustomRuleExplicitZeroOverridesTotalCost(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
		ProviderStatsPricingRules: []routing.ProviderStatsPricingRule{
			{
				GroupIDs: []int64{10},
				Pricing: []routing.ModelPricingEntry{
					{
						ID:         100,
						Models:     []string{"qwen3.7-plus"},
						InputPrice: testPtrFloat64(0),
					},
				},
			},
		},
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformQoder)

	result := contractProviderStatsCost(
		context.Background(),
		cs, nil,
		capability.PlatformQoder, 1, 10, "qmodel", "qwen3.7-plus",
		pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1, 999.0, "",
	)

	require.NotNil(t, result)
	require.Zero(t, *result)
}

// TestResolveProviderStatsCost_QoderUnknownUpstreamDoesNotUseRequestedPrice
// pairs an unpriced upstream route with a catalog-priced request name. It
// checks that the catalog path returns nil for the selected upstream identity.
func TestResolveProviderStatsCost_QoderUnknownUpstreamDoesNotUseRequestedPrice(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformQoder)
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"gpt-5.4": {
			InputPricePerToken:  0.001,
			OutputPricePerToken: 0.002,
		},
	})

	result := contractProviderStatsCost(
		context.Background(),
		cs, bs,
		capability.PlatformQoder, 1, 10, "ultimate", "gpt-5.4",
		pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 1, 999.0, "",
	)

	require.Nil(t, result)
}

// TestResolveProviderStatsCost_Gemini36FlashTierUsesFallbackPricing queries
// the full Gemini low-suffixed model in the historical fixture. It checks a
// nil statistics result when that complete model has no catalog price.
func TestResolveProviderStatsCost_Gemini36FlashTierUsesFallbackPricing(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "antigravity")
	bs := newCalculator(nil)

	result := contractProviderStatsCost(
		context.Background(),
		cs, bs,
		"", 1, 10, "gemini-3.6-flash-low", "",
		pricing.UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000}, 1, 0, "",
	)
	require.Nil(t, result)
}

// TestResolveProviderStatsCost_AllMiss_ReturnsNil combines empty configured
// rules with an empty catalog and positive token usage. It checks that the
// unresolved provider-cost result remains nil.
func TestResolveProviderStatsCost_AllMiss_ReturnsNil(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "anthropic")

	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{})

	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}

	result := contractProviderStatsCost(
		context.Background(),
		cs, bs,
		"", 1, 10, "totally-unknown-model", "",
		tokens, 1, 0.0, "",
	)
	require.Nil(t, result)
}

// TestResolveProviderStatsCost_NilBillingService_Skips模型目录 supplies a loaded
// group configuration with empty rules and no billing calculator. It checks
// that catalog pricing is unavailable through that dependency selection.
func TestResolveProviderStatsCost_NilBillingService_Skips模型目录(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "anthropic")

	result := contractProviderStatsCost(
		context.Background(),
		cs, nil,
		"", 1, 10, "claude-sonnet-4", "",
		pricing.UsageTokens{InputTokens: 100}, 1, 0.0, "",
	)
	require.Nil(t, result)
}

// TestResolveProviderStatsCost_CustomRuleDoesNotUseUserPrice supplies a custom
// input price alongside a different positive fixture totalCost. It checks that
// the provider amount follows the manual token price.
func TestResolveProviderStatsCost_CustomRuleDoesNotUseUserPrice(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
		ProviderStatsPricingRules: []routing.ProviderStatsPricingRule{
			{
				GroupIDs: []int64{10},
				Pricing: []routing.ModelPricingEntry{
					{
						ID:         100,
						Models:     []string{"claude-sonnet-4"},
						InputPrice: testPtrFloat64(0.05),
					},
				},
			},
		},
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "anthropic")

	tokens := pricing.UsageTokens{InputTokens: 100}

	result := contractProviderStatsCost(
		context.Background(),
		cs, nil,
		"", 1, 10, "claude-sonnet-4", "",
		tokens, 1, 99.0, "",
	)
	require.NotNil(t, result)
	// The custom rule charges 100 * 0.05 = 5.0. Compare provider USD amounts
	// with an absolute rounding tolerance.
	require.InDelta(t, 5.0, *result, 1e-12)
}

// TestApplyProviderStatsCost_UsesUsageLogServiceTier stores priority on the
// usage row while supplying catalog standard and priority prices. It checks
// that the helper writes the priority-priced provider amount back to the row.
func TestApplyProviderStatsCost_UsesUsageLogServiceTier(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "openai")
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"gpt-5.6-sol": {
			InputPricePerToken:          0.001,
			InputPricePerTokenPriority:  0.002,
			OutputPricePerToken:         0.002,
			OutputPricePerTokenPriority: 0.004,
		},
	})
	serviceTier := "priority"
	usageLog := &usage.UsageLog{ServiceTier: &serviceTier}

	applyContractProviderStatsCost(
		context.Background(), usageLog, cs, bs,
		1, 10, "gpt-5.6-sol", "gpt-5.6-sol", "",
		pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}, 999,
	)

	require.NotNil(t, usageLog.ProviderStatsCost)
	// Compare provider USD amounts with an absolute rounding tolerance.
	require.InDelta(t, 0.4, *usageLog.ProviderStatsCost, 1e-12)
}

// applyContractProviderStatsCost writes the resolved provider-cost pointer
// onto a nonnil usage log. An empty upstream name uses requestedModel;
// positive image count, final tier, and final effort come from the row.
// Operation count defaults to one when no positive image count is present. A
// nonnil first optional resolver takes precedence over the constructed fixture
// resolver. Later optional resolvers are ignored, and totalCost contributes no
// amount on the fixture path.
func applyContractProviderStatsCost(
	ctx context.Context,
	usageLog *usage.UsageLog,
	cs *routing.PricingConfigService, bs *billing.Calculator,
	providerID int64, groupID int64,
	upstreamModel, requestedModel, groupMappedModel string,
	tokens pricing.UsageTokens,
	totalCost float64,
	resolvers ...*billing.PriceResolver,
) {
	model := upstreamModel
	// An absent upstream identity uses the request name before invoking
	// statistics resolution. Preserve that selected identity when forwarding
	// the row's tier and effort.
	if model == "" {
		model = requestedModel
	}
	requestCount := 1
	if usageLog != nil && usageLog.ImageCount > 0 {
		requestCount = usageLog.ImageCount
	}
	serviceTier := ""
	reasoningEffort := ""
	if usageLog != nil && usageLog.ServiceTier != nil {
		serviceTier = *usageLog.ServiceTier
	}
	if usageLog != nil && usageLog.ReasoningEffort != nil {
		reasoningEffort = *usageLog.ReasoningEffort
	}
	// An explicitly supplied resolver owns the statistics lookup. Only its
	// first optional argument is considered, so later entries cannot replace
	// this dependency selection.
	if len(resolvers) > 0 && resolvers[0] != nil {
		usageLog.ProviderStatsCost = resolvers[0].ResolveProviderStats(ctx, billing.ProviderStatsCostInput{PreferRequestedModel: usageLog.Platform == "qoder", ProviderID: providerID, GroupID: groupID, UpstreamModel: model, RequestedModel: requestedModel, MappedModel: groupMappedModel, Tokens: tokens, RequestCount: requestCount, ServiceTier: serviceTier, ReasoningEffort: reasoningEffort})
		return
	}
	usageLog.ProviderStatsCost = contractProviderStatsWithMapping(
		ctx, cs, bs, usageLog.Platform, providerID, groupID, model, requestedModel, groupMappedModel, tokens, requestCount, totalCost, serviceTier,
		reasoningEffort,
	)
}

// newTestBillingServiceWithPrices constructs a calculator using the supplied
// full-name token-price map. An empty map creates an empty catalog; nil
// selects the shared historical/media
// defaults through newCalculatorWithPrices.
func newTestBillingServiceWithPrices(prices map[string]*pricing.ModelPricing) *billing.Calculator {
	return newCalculatorWithPrices(nil, prices)
}
