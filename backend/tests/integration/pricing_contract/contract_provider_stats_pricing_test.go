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
	// 100*0.001 + 50*0.002 = 0.1 + 0.1 = 0.2
	require.InDelta(t, 0.2, *result, 1e-12)
}

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
	require.InDelta(t, 0.233, *result, 1e-12)
}

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
		name        string
		serviceTier string
		want        float64
	}{
		{name: "standard", serviceTier: "", want: 0.265},
		{name: "priority", serviceTier: "priority", want: 0.53},
		{name: "flex", serviceTier: "flex", want: 0.1325},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bs.ModelFileStatsCost("gpt-5.6-sol", tokens, tt.serviceTier, "")
			require.NotNil(t, result)
			require.InDelta(t, tt.want, *result, 1e-12)
		})
	}
}

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
	// Apply priority prices before the 2x input and 1.5x output long-context rates.
	require.InDelta(t, 0.534, *result, 1e-12)
}

func TestTryModelFilePricing_PricingNotFound(t *testing.T) {
	// The fixture has no catalog entry for nonexistent-model.
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{})
	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}
	result := bs.ModelFileStatsCost("nonexistent-model", tokens, "", "")
	require.Nil(t, result)
}

func TestTryModelFilePricing_NilFallback(t *testing.T) {
	// A nil price entry supplies no catalog price.
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"claude-sonnet-4": nil,
	})
	tokens := pricing.UsageTokens{InputTokens: 100}
	result := bs.ModelFileStatsCost("claude-sonnet-4", tokens, "", "")
	require.Nil(t, result)
}

func TestTryModelFilePricing_ZeroCost(t *testing.T) {
	bs := newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
		"claude-sonnet-4": {
			InputPricePerToken:  0.001,
			OutputPricePerToken: 0.002,
		},
	})
	tokens := pricing.UsageTokens{} // Zero usage returns no provider cost.
	result := bs.ModelFileStatsCost("claude-sonnet-4", tokens, "", "")
	require.Nil(t, result)
}

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
	// ImageOutputTokens is part of OutputTokens; subtract it before pricing text output.
	// 100*0.001 + (50-10)*0.002 + 10*0.01 = 0.1 + 0.08 + 0.1 = 0.28
	require.InDelta(t, 0.28, *result, 1e-12)
}

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
	// 100*0.001 + 50*0.002 + 200*0.003 + 300*0.0005
	// = 0.1 + 0.1 + 0.6 + 0.15 = 0.95
	require.InDelta(t, 0.95, *result, 1e-12)
}

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

func TestResolveProviderStatsCost_GetPricingConfigForGroupReturnsNil(t *testing.T) {
	// Group 99 has no cached pricing configuration.
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

	result := contractProviderStatsCost(
		context.Background(),
		cs, nil, // billingService not needed when custom rule hits
		"", 1, 10, "claude-sonnet-4", "",
		tokens, 1, 999.0, "priority", // Custom provider prices do not apply service-tier multipliers.
	)
	require.NotNil(t, result)
	// 100*0.01 + 50*0.02 = 1.0 + 1.0 = 2.0
	require.InDelta(t, 2.0, *result, 1e-12)
}

func TestResolveProviderStatsCost_DoesNotUseUserPrice(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
		// No custom rules
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "anthropic")

	tokens := pricing.UsageTokens{InputTokens: 100, OutputTokens: 50}

	result := contractProviderStatsCost(
		context.Background(),
		cs, newTestBillingServiceWithPrices(map[string]*pricing.ModelPricing{
			"claude-sonnet-4": {InputPricePerToken: 0.001, OutputPricePerToken: 0.002},
		}),
		"", 1, 10, "claude-sonnet-4", "",
		tokens, 1, 0.75, "", // The user charge is excluded from provider costs.
	)
	require.NotNil(t, result)
	require.InDelta(t, 0.2, *result, 1e-12)
}

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

func TestResolveProviderStatsCost_FallsBackTo模型目录(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
		// No custom rules
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
		tokens, 1, 999.0, "", // totalCost ignored
	)
	require.NotNil(t, result)
	// 100*0.001 + 50*0.002 = 0.1 + 0.1 = 0.2
	require.InDelta(t, 0.2, *result, 1e-12)
}

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
	require.InDelta(t, 0.2, *result, 1e-12)
}

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
	require.InDelta(t, 2.0, *result, 1e-12)
}

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
	require.InDelta(t, 2.0, *result, 1e-12)
}

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
	require.InDelta(t, 2.0, *result, 1e-12)
}

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
	require.InDelta(t, 2.0, *result, 1e-12)
}

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
	require.InDelta(t, 2.0, *result, 1e-12)
}

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

func TestResolveProviderStatsCost_AllMiss_ReturnsNil(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
		// No custom rules
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "anthropic")

	// The fixture catalog has no price for this model.
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

func TestResolveProviderStatsCost_NilBillingService_Skips模型目录(t *testing.T) {
	pricingConfig := &routingtestkit.Configuration{
		ID:     1,
		Status: billing.StatusActive,
	}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, "anthropic")

	result := contractProviderStatsCost(
		context.Background(),
		cs, nil, // billingService is nil
		"", 1, 10, "claude-sonnet-4", "",
		pricing.UsageTokens{InputTokens: 100}, 1, 0.0, "",
	)
	require.Nil(t, result)
}

func TestResolveProviderStatsCost_CustomRuleDoesNotUseUserPrice(t *testing.T) {
	// Provider cost rules use prices independent of the user charge.
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
		tokens, 1, 99.0, "", // The user charge is excluded from provider costs.
	)
	require.NotNil(t, result)
	// The custom rule charges 100 * 0.05 = 5.0.
	require.InDelta(t, 5.0, *result, 1e-12)
}

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
	require.InDelta(t, 0.4, *usageLog.ProviderStatsCost, 1e-12)
}

// applyContractProviderStatsCost calculates provider cost for a usage log.
// It uses the requested model when the upstream model is empty, then resolves cost rules.
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
	if len(resolvers) > 0 && resolvers[0] != nil {
		usageLog.ProviderStatsCost = resolvers[0].ResolveProviderStats(ctx, billing.ProviderStatsCostInput{PreferRequestedModel: usageLog.Platform == "qoder", ProviderID: providerID, GroupID: groupID, UpstreamModel: model, RequestedModel: requestedModel, MappedModel: groupMappedModel, Tokens: tokens, RequestCount: requestCount, ServiceTier: serviceTier, ReasoningEffort: reasoningEffort})
		return
	}
	usageLog.ProviderStatsCost = contractProviderStatsWithMapping(
		ctx, cs, bs, usageLog.Platform, providerID, groupID, model, requestedModel, groupMappedModel, tokens, requestCount, totalCost, serviceTier,
		reasoningEffort,
	)
}

// newTestBillingServiceWithPrices constructs a calculator from the supplied catalog prices.
// Map keys use the matched model name, such as claude-sonnet-4.
func newTestBillingServiceWithPrices(prices map[string]*pricing.ModelPricing) *billing.Calculator {
	return newCalculatorWithPrices(nil, prices)
}
