package billing

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

func TestDefaultPriceUsesCatalogAndPreservesZero(t *testing.T) {
	catalog := defaultCatalogStub{entries: map[string]*pricing.CatalogModelPricing{
		"custom": {CatalogRules: pricing.CatalogRules{FlexMultiplier: newFloatForCatalogTest(0.5)}, InputCostPerToken: 0, OutputCostPerToken: 0.000004, SupportsServiceTier: true, InputCostPerTokenPriority: 0, OutputCostPerTokenPriority: 0.000008, LongContextInputTokenThreshold: 100000, LongContextInputCostMultiplier: 2, LongContextOutputCostMultiplier: 1.5},
	}}
	calculator := NewCalculator(catalog, CalculatorOptions{})
	row := calculator.DefaultModelPrice("custom", "openai", "token")
	require.Equal(t, "priced", row.PriceStatus)
	require.Equal(t, 100000, row.LongContextThreshold)
	values := make(map[string]float64)
	for _, price := range row.Prices {
		if price.Value != nil {
			values[price.Key] = *price.Value
		}
	}
	require.Contains(t, values, "input")
	require.Zero(t, values["input"])
	require.Equal(t, 4.0, values["output"])
	require.Equal(t, 8.0, values["fast_output"])
	require.Equal(t, 2.0, values["flex_output"])
	// 长上下文返回应用倍率后的单价：output 4x1.5，fast 8x1.5，flex 2x1.5。
	require.Zero(t, values["long_input"])
	require.Equal(t, 6.0, values["long_output"])
	require.Equal(t, 12.0, values["long_fast_output"])
	require.Equal(t, 3.0, values["long_flex_output"])
	require.NotContains(t, values, "long_context_input")
	require.NotContains(t, values, "long_context_output")
	require.Equal(t, "unpriced", calculator.DefaultModelPrice("unknown-model", "openai", "token").PriceStatus)
	require.Equal(t, "unpriced", calculator.DefaultModelPrice("claude-sonnet-4", "anthropic", "token").PriceStatus)
}

func TestDefaultPriceContextIntervalsUseInclusiveBoundary(t *testing.T) {
	catalog := defaultCatalogStub{entries: map[string]*pricing.CatalogModelPricing{
		"grok-test": {
			Source:             "models.dev",
			Provider:           "xai",
			InputCostPerToken:  2e-6,
			OutputCostPerToken: 6e-6,
			ContextPrices: []pricing.CatalogContextPrice{
				{Threshold: 200000, Pricing: &pricing.CatalogModelPricing{InputCostPerToken: 4e-6, OutputCostPerToken: 12e-6}},
			},
		},
	}}
	calculator := NewCalculator(catalog, CalculatorOptions{})
	row := calculator.DefaultModelPrice("grok-test", "xai", "token")
	require.Len(t, row.ContextIntervals, 2)
	require.Equal(t, 199999, *row.ContextIntervals[0].MaxTokens)
	require.Equal(t, 199999, row.ContextIntervals[1].MinTokens)
	// 目录阈值保持 100，重复查询得到相同的区间。
	require.Equal(t, 200000, catalog.entries["grok-test"].ContextPrices[0].Threshold)
	require.Equal(t, row, calculator.DefaultModelPrice("grok-test", "xai", "token"))
}

func TestDefaultMediaPriceRequiresKnownUnits(t *testing.T) {
	entries, _, err := pricing.ParsePricingEntries(map[string]json.RawMessage{
		"free-image":   json.RawMessage(`{"source":"local_override","mode":"image_generation","output_cost_per_image":0}`),
		"custom-image": json.RawMessage(`{"source":"models.dev","mode":"chat","output_cost_per_image":0.1,"price_sources":{"image":"local_override"}}`),
		"vision-chat":  json.RawMessage(`{"source":"models.dev","mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"output_cost_per_image":0.1}`),
	})
	require.NoError(t, err)
	calculator := NewCalculator(defaultCatalogStub{entries: entries}, CalculatorOptions{})
	for _, model := range []string{"free-image", "custom-image"} {
		row := calculator.DefaultModelPrice(model, "other", entries[model].Mode)
		require.Equal(t, "image", row.BillingMode)
		require.Equal(t, "priced", row.PriceStatus)
		require.Len(t, row.Prices, 3)
		for _, price := range row.Prices {
			require.NotNil(t, price.Value)
			require.Equal(t, "USD/image", price.Unit)
			require.Equal(t, "local_override", row.PriceSources[price.Key])
			unit, err := calculator.DefaultImagePrice(model, price.Key)
			require.NoError(t, err)
			require.Equal(t, unit, *price.Value)
			if model == "free-image" {
				require.Zero(t, *price.Value)
			}
		}
	}
	for _, mode := range []string{"video", "image"} {
		row := calculator.DefaultModelPrice("unknown-media", "other", mode)
		require.Equal(t, mode, row.BillingMode)
		require.Equal(t, "unpriced", row.PriceStatus)
		require.Empty(t, row.Prices)
	}
	row := calculator.DefaultModelPrice("vision-chat", "other", "chat")
	require.Equal(t, "token", row.BillingMode)
	for _, price := range row.Prices {
		require.NotEqual(t, "USD/image", price.Unit)
	}
}

func TestDefaultPriceContextIntervalsKeepRootAndTierQuotesIndependent(t *testing.T) {
	for _, rootSupportsTier := range []bool{false, true} {
		name := "root_flex_multiplier"
		var rootFlex *float64
		if rootSupportsTier {
			name = "root_service_tier_without_flex_multiplier"
		} else {
			rootFlex = newFloatForCatalogTest(0.5)
		}
		t.Run(name, func(t *testing.T) {
			raw := &pricing.CatalogModelPricing{
				CatalogRules: pricing.CatalogRules{
					FastMultiplier:               newFloatForCatalogTest(2),
					FlexMultiplier:               rootFlex,
					MaxReasoningEffortMultiplier: newFloatForCatalogTest(1.5),
				},
				InputCostPerToken:     2e-6,
				OutputCostPerToken:    8e-6,
				SupportsServiceTier:   rootSupportsTier,
				CacheReadPricePresent: true,
				SupportsVision:        true,
				ContextPrices: []pricing.CatalogContextPrice{
					{Threshold: 100, Pricing: &pricing.CatalogModelPricing{
						CatalogRules:                pricing.CatalogRules{FastMultiplier: newFloatForCatalogTest(3), FlexMultiplier: newFloatForCatalogTest(0.25)},
						InputCostPerToken:           4e-6,
						OutputCostPerToken:          12e-6,
						CacheCreationInputTokenCost: 5e-6,
						OutputCostPerImageToken:     9e-6,
						SupportsServiceTier:         true,
					}},
					{Threshold: 200, Pricing: &pricing.CatalogModelPricing{
						CatalogRules:            pricing.CatalogRules{FastMultiplier: newFloatForCatalogTest(4), FlexMultiplier: newFloatForCatalogTest(0.1)},
						InputCostPerToken:       8e-6,
						OutputCostPerToken:      20e-6,
						CacheReadInputTokenCost: 1e-6,
					}},
				},
			}
			calculator := NewCalculator(defaultCatalogStub{entries: map[string]*pricing.CatalogModelPricing{"custom": raw}}, CalculatorOptions{})
			row := calculator.DefaultModelPrice("custom", "openai", "token")
			rootValues := []*float64{newFloatForCatalogTest(2), newFloatForCatalogTest(8), newFloatForCatalogTest(0), nil, nil, newFloatForCatalogTest(2), nil}
			rootPrices := expectedCatalogTokenQuotes("", 1, rootValues)
			rootPrices = append(rootPrices, expectedCatalogTokenQuotes("fast_", 2, rootValues)...)
			firstIntervalPrices := append([]pricing.DefaultPriceValue(nil), rootPrices...)
			if rootFlex != nil {
				rootPrices = append(rootPrices, expectedCatalogTokenQuotes("flex_", 0.5, rootValues)...)
			}
			rootPrices = append(rootPrices, pricing.DefaultPriceValue{Key: "max_reasoning", Value: newFloatForCatalogTest(1.5), Unit: "multiplier"})
			if rootSupportsTier {
				firstIntervalPrices = append(firstIntervalPrices, expectedCatalogTokenQuotes("flex_", 1, rootValues)...)
			}
			middleValues := []*float64{newFloatForCatalogTest(4), newFloatForCatalogTest(12), newFloatForCatalogTest(0), newFloatForCatalogTest(5), nil, newFloatForCatalogTest(4), newFloatForCatalogTest(9)}
			middlePrices := expectedCatalogTokenQuotes("", 1, middleValues)
			middlePrices = append(middlePrices, expectedCatalogTokenQuotes("fast_", 3, middleValues)...)
			middlePrices = append(middlePrices, expectedCatalogTokenQuotes("flex_", 0.25, middleValues)...)
			lastValues := []*float64{newFloatForCatalogTest(8), newFloatForCatalogTest(20), newFloatForCatalogTest(1), nil, nil, newFloatForCatalogTest(8), nil}
			lastPrices := expectedCatalogTokenQuotes("", 1, lastValues)
			lastPrices = append(lastPrices, expectedCatalogTokenQuotes("fast_", 4, lastValues)...)
			assertCatalogTokenQuotes(t, rootPrices, row.Prices)
			require.Len(t, row.ContextIntervals, 3)
			for i, want := range [][]pricing.DefaultPriceValue{firstIntervalPrices, middlePrices, lastPrices} {
				interval := row.ContextIntervals[i]
				require.Equal(t, i*100, interval.MinTokens)
				if i < 2 {
					require.NotNil(t, interval.MaxTokens)
					require.Equal(t, (i+1)*100, *interval.MaxTokens)
				} else {
					require.Nil(t, interval.MaxTokens)
				}
				assertCatalogTokenQuotes(t, want, interval.Prices)
			}
			row.ContextIntervals[0].Prices[0].Key = "changed"
			*row.ContextIntervals[0].Prices[0].Value = -1
			assertCatalogTokenQuotes(t, rootPrices, row.Prices)
			assertCatalogTokenQuotes(t, middlePrices, row.ContextIntervals[1].Prices)
			assertCatalogTokenQuotes(t, lastPrices, row.ContextIntervals[2].Prices)
			assertCatalogTokenQuotes(t, rootPrices, calculator.DefaultModelPrice("custom", "openai", "token").Prices)
		})
	}
}

func TestDefaultPriceLongContextQuotesCrossProviderThreshold(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			raw := &pricing.CatalogModelPricing{
				CatalogRules:                        pricing.CatalogRules{FastMultiplier: newFloatForCatalogTest(2), FlexMultiplier: newFloatForCatalogTest(0.5)},
				Provider:                            provider,
				InputCostPerToken:                   2e-6,
				OutputCostPerToken:                  8e-6,
				CacheReadInputTokenCost:             0.5e-6,
				CacheCreationInputTokenCost:         1e-6,
				CacheCreationInputTokenCostAbove1hr: 2e-6,
				SupportsVision:                      true,
				LongContextInputTokenThreshold:      100,
				LongContextInputCostMultiplier:      2,
				LongContextOutputCostMultiplier:     3,
			}
			calculator := NewCalculator(defaultCatalogStub{entries: map[string]*pricing.CatalogModelPricing{"custom": raw}}, CalculatorOptions{})
			row := calculator.DefaultModelPrice("custom", provider, "token")
			require.Equal(t, 100, row.LongContextThreshold)
			require.Equal(t, provider == "xai", row.LongContextThresholdInclusive)
			baseValues := []*float64{newFloatForCatalogTest(2), newFloatForCatalogTest(8), newFloatForCatalogTest(0.5), newFloatForCatalogTest(1), newFloatForCatalogTest(2), newFloatForCatalogTest(2), nil}
			longValues := []*float64{newFloatForCatalogTest(4), newFloatForCatalogTest(24), newFloatForCatalogTest(1), newFloatForCatalogTest(2), newFloatForCatalogTest(4), newFloatForCatalogTest(4), nil}
			want := expectedCatalogTokenQuotes("", 1, baseValues)
			want = append(want, expectedCatalogTokenQuotes("fast_", 2, baseValues)...)
			want = append(want, expectedCatalogTokenQuotes("flex_", 0.5, baseValues)...)
			want = append(want, expectedCatalogTokenQuotes("long_", 1, longValues)...)
			want = append(want, expectedCatalogTokenQuotes("long_fast_", 2, longValues)...)
			want = append(want, expectedCatalogTokenQuotes("long_flex_", 0.5, longValues)...)
			assertCatalogTokenQuotes(t, want, row.Prices)
		})
	}
}

func expectedCatalogTokenQuotes(prefix string, multiplier float64, values []*float64) []pricing.DefaultPriceValue {
	keys := []string{"input", "output", "cache_read", "cache_write", "cache_write_1h", "image_input", "image_output"}
	prices := make([]pricing.DefaultPriceValue, len(keys))
	for i, key := range keys {
		prices[i] = pricing.DefaultPriceValue{Key: prefix + key, Unit: "USD/MTok"}
		if values[i] != nil {
			prices[i].Value = newFloatForCatalogTest(*values[i] * multiplier)
		}
	}
	return prices
}

func assertCatalogTokenQuotes(t *testing.T, expected, actual []pricing.DefaultPriceValue) {
	t.Helper()
	require.Len(t, actual, len(expected))
	for i, want := range expected {
		got := actual[i]
		require.Equal(t, want.Key, got.Key)
		require.Equal(t, want.Unit, got.Unit)
		if want.Value == nil {
			require.Nil(t, got.Value, want.Key)
		} else {
			require.NotNil(t, got.Value, want.Key)
			require.InDelta(t, *want.Value, *got.Value, 1e-12, want.Key)
		}
	}
}

func newFloatForCatalogTest(value float64) *float64 { return &value }
