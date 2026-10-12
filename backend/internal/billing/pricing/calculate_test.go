package pricing

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPriorityCacheTTLUsesPricesRegardlessOfSource checks Fast TTL cost and display parity across sources.
func TestPriorityCacheTTLUsesPricesRegardlessOfSource(t *testing.T) {
	for _, source := range []string{"models.dev", "local_supplement"} {
		t.Run(source, func(t *testing.T) {
			entries, diagnostics, err := ParsePricingEntries(map[string]json.RawMessage{"custom": json.RawMessage(`{"source":"` + source + `","input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"input_cost_per_token_priority":0.000002,"output_cost_per_token_priority":0.000004,"cache_write_multiplier":1.25,"cache_write_1h_multiplier":2}`)})
			require.NoError(t, err)
			require.NoError(t, diagnostics.ValidationError())
			price, err := ResolveModelPricing("custom", entries["custom"])
			require.NoError(t, err)
			for _, tc := range []struct {
				tokens UsageTokens
				cost   float64
			}{
				{UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: 100}, 0.00025},
				{UsageTokens{CacheCreationTokens: 100, CacheCreation1hTokens: 100}, 0.0004},
				{UsageTokens{CacheCreationTokens: 200, CacheCreation5mTokens: 100, CacheCreation1hTokens: 100}, 0.00065},
			} {
				cost := ComputeTokenBreakdown(price, tc.tokens, 1, "priority", true)
				require.InDelta(t, tc.cost, cost.TotalCost, 1e-12)
			}
			fast, ok := FastModeDisplayPricing(price)
			require.True(t, ok)
			require.InDelta(t, 2.5e-6, fast.CacheCreation5mPrice, 1e-12)
			require.InDelta(t, 4e-6, fast.CacheCreation1hPrice, 1e-12)
			require.InDelta(t, 1.25e-6, price.CacheCreation5mPrice, 1e-12, "不得修改标准档")
		})
	}
}

// TestPartialImageCardDoesNotDeclareMissingSizesFree leaves missing sizes unpriced for application-level catalog lookup.
func TestPartialImageCardDoesNotDeclareMissingSizesFree(t *testing.T) {
	zero := 0.0
	resolved := ResolvePriceCards(&ModelPricingEntry{BillingMode: BillingModeImage, Intervals: []PricingInterval{{TierLabel: "1K", PerRequestPrice: &zero}}}, nil, PricingSourceUnpriced, true)
	shown, ok := DisplayPricingFromResolved("custom-image", 1, resolved)
	require.True(t, ok)
	require.Equal(t, []string{"1K"}, shown.ImagePriceSizes)
	require.Zero(t, shown.ImagePrice1K)
}

// TestContextTierImageOverrides checks image overrides in costs, display intervals, and catalog copies.
func TestContextTierImageOverrides(t *testing.T) {
	input, imageInput, imageOutput, zero, multiplier := 2e-6, 1e-6, 3e-6, 0.0, 2.0
	base := &ModelPricing{
		InputPricePerToken:       2e-6,
		OutputPricePerToken:      5e-6,
		ImageInputPricePerToken:  9e-6,
		ImageOutputPricePerToken: 10e-6,
		ContextPrices: []ContextModelPrice{
			{
				Threshold: 100,
				Pricing: &ModelPricing{
					InputPricePerToken:       4e-6,
					OutputPricePerToken:      10e-6,
					ImageInputPricePerToken:  18e-6,
					ImageOutputPricePerToken: 20e-6,
				},
			},
			{
				Threshold: 200,
				Pricing: &ModelPricing{
					InputPricePerToken:       8e-6,
					OutputPricePerToken:      20e-6,
					ImageInputPricePerToken:  36e-6,
					ImageOutputPricePerToken: 40e-6,
				},
			},
		},
	}
	before := MultiplyModelPricing(base, 1)
	for _, tc := range []struct {
		name                    string
		config                  ModelPricingEntry
		imageInput, imageOutput float64
	}{
		{"custom", ModelPricingEntry{ImageInputPrice: &imageInput, ImageOutputPrice: &imageOutput}, 1e-6, 3e-6},
		{"scaled", ModelPricingEntry{ImageInputPrice: &imageInput, ImageOutputPrice: &imageOutput, PriceMultiplier: &multiplier}, 2e-6, 6e-6},
		{"zero", ModelPricingEntry{InputPrice: &input, ImageInputPrice: &zero, ImageOutputPrice: &zero}, 0, 0},
		{"omitted", ModelPricingEntry{InputPrice: &input}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := ResolvePriceCards(&tc.config, base, PricingSourceCatalog, true)
			for _, tokens := range []int{100, 101, 200, 201} {
				cost, err := CalculateTokenCost(resolved, CostInput{
					Model: "test",
					Tokens: UsageTokens{
						InputTokens:       tokens,
						ImageInputTokens:  10,
						OutputTokens:      10,
						ImageOutputTokens: 10,
					},
					RateMultiplier: 1,
				})
				require.NoError(t, err)
				inputPrice := tc.imageInput
				if inputPrice == 0 {
					inputPrice = input
				}
				require.InDelta(t, 10*inputPrice, cost.ImageInputCost, 1e-12)
				require.InDelta(t, 10*tc.imageOutput, cost.ImageOutputCost, 1e-12)
			}
			for _, value := range []*ModelPricing{resolved.BasePricing, ApplyConfigPrice(base, &tc.config)} {
				intervals := LongContextDisplayPricingIntervals(value, 1)
				require.Len(t, intervals, 3)
				for _, interval := range intervals {
					require.InDelta(t, tc.imageInput, interval.ImageInputPricePerToken, 1e-12)
					require.InDelta(t, tc.imageOutput, interval.ImageOutputPricePerToken, 1e-12)
				}
				for _, tier := range value.ContextPrices {
					require.True(t, tier.Pricing.ImageOutputPriceExplicit)
				}
			}
			require.Equal(t, before, MultiplyModelPricing(base, 1))
		})
	}
}

// TestPriceCardsPreserveInputsAndPricePresence checks inherited multipliers and distinguishes free from missing prices.
func TestPriceCardsPreserveInputsAndPricePresence(t *testing.T) {
	base := &ModelPricing{InputPricePerToken: 0.000001, SupportsServiceTier: true}
	pricingConfigMultiplier := 1.5
	configPricing := &ModelPricingEntry{BillingMode: BillingModeToken, FastMultiplier: &pricingConfigMultiplier}
	resolved := ResolvePriceCards(configPricing, base, PricingSourceCatalog, true)
	cost, err := CalculateCost(resolved, CostInput{Model: "custom", Tokens: UsageTokens{InputTokens: 100}, RateMultiplier: 1, ServiceTier: "priority"})
	require.NoError(t, err)
	require.InDelta(t, 0.00015, cost.ActualCost, 1e-12)
	require.Nil(t, base.FastMultiplier)
	require.Equal(t, 1.5, *configPricing.FastMultiplier)
	require.Equal(t, PricingSourceCatalog, resolved.Source)

	missing := ResolvePriceCards(configPricing, nil, PricingSourceUnpriced, true)
	_, err = CalculateCost(missing, CostInput{Model: "missing", Tokens: UsageTokens{InputTokens: 100}, RateMultiplier: 1})
	require.True(t, errors.Is(err, ErrModelPricingUnavailable))
	zero := 0.0
	free := ResolvePriceCards(&ModelPricingEntry{InputPrice: &zero}, nil, PricingSourceUnpriced, true)
	cost, err = CalculateCost(free, CostInput{Model: "free", Tokens: UsageTokens{InputTokens: 100}, RateMultiplier: 1})
	require.NoError(t, err)
	require.Zero(t, cost.ActualCost)
	require.Equal(t, PricingSourceConfig, free.Source)
}

// TestZeroPricingTimeDoesNotEnablePricingTimeMultiplier checks that a zero PricingAt disables card time rules.
func TestZeroPricingTimeDoesNotEnablePricingTimeMultiplier(t *testing.T) {
	at := time.Date(2026, 6, 29, 2, 0, 0, 0, time.UTC)
	card := &ModelPricingEntry{TimePricing: &TimePricingConfig{Timezone: "UTC", Periods: []TimePricingPeriod{{StartTime: "01:00", EndTime: "04:00", Multiplier: 2}}}}
	resolved := ResolvePriceCards(card, &ModelPricing{InputPricePerToken: 0.000001}, PricingSourceCatalog, true)
	input := CostInput{Model: "custom", Tokens: UsageTokens{InputTokens: 100}, RateMultiplier: 1, ModelPricingAt: at, TimePricingLocation: time.UTC}
	cost, err := CalculateCost(resolved, input)
	require.NoError(t, err)
	require.InDelta(t, 0.0001, cost.ActualCost, 1e-12)
	input.PricingAt = at
	cost, err = CalculateCost(resolved, input)
	require.NoError(t, err)
	require.InDelta(t, 0.0002, cost.ActualCost, 1e-12)
}
