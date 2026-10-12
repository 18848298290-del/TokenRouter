package billing_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
)

const (
	// gpt56LadderCatalogJSON supplies tiered GPT-5.6 catalog prices.
	gpt56LadderCatalogJSON = `{
	"gpt-5.6-sol": {"provider": "openai", "mode": "chat", "cache_write_multiplier": 1.25, "flex_multiplier": 0.5,
		"input_cost_per_token": 5e-06, "input_cost_per_token_priority": 1e-05,
		"output_cost_per_token": 3e-05, "output_cost_per_token_priority": 6e-05,
		"cache_read_input_token_cost": 5e-07, "cache_read_input_token_cost_priority": 1e-06,
		"input_cost_per_token_above_272k_tokens": 1e-05,
		"output_cost_per_token_above_272k_tokens": 4.5e-05,
		"cache_read_input_token_cost_above_272k_tokens": 1e-06},
	"gpt-5.6-terra": {"provider": "openai", "mode": "chat", "cache_write_multiplier": 1.25, "flex_multiplier": 0.5,
		"input_cost_per_token": 2e-06, "input_cost_per_token_priority": 4e-06,
		"output_cost_per_token": 1.2e-05, "output_cost_per_token_priority": 2.4e-05,
		"cache_read_input_token_cost": 2e-07, "cache_read_input_token_cost_priority": 4e-07,
		"input_cost_per_token_above_272k_tokens": 4e-06,
		"output_cost_per_token_above_272k_tokens": 1.8e-05,
		"cache_read_input_token_cost_above_272k_tokens": 4e-07},
	"gpt-5.6-luna": {"provider": "openai", "mode": "chat", "cache_write_multiplier": 1.25, "flex_multiplier": 0.5,
		"input_cost_per_token": 2e-07, "input_cost_per_token_priority": 4e-07,
		"output_cost_per_token": 1.2e-06, "output_cost_per_token_priority": 2.4e-06,
		"cache_read_input_token_cost": 2e-08, "cache_read_input_token_cost_priority": 4e-08,
		"input_cost_per_token_above_272k_tokens": 4e-07,
		"output_cost_per_token_above_272k_tokens": 1.8e-06,
		"cache_read_input_token_cost_above_272k_tokens": 4e-08}
}`

	// openAILadderCatalogJSON models synced catalog data with absolute above_272k rates.
	// The parser converts those rates into a threshold and multipliers.
	openAILadderCatalogJSON = `{
	"gpt-5.4": {"provider": "openai", "mode": "chat", "fast_multiplier": 2, "flex_multiplier": 0.5,
		"input_cost_per_token": 2.5e-06, "output_cost_per_token": 1.5e-05,
		"cache_read_input_token_cost": 2.5e-07, "cache_creation_input_token_cost": 2.5e-06,
		"input_cost_per_token_above_272k_tokens": 5e-06,
		"output_cost_per_token_above_272k_tokens": 2.25e-05,
		"cache_read_input_token_cost_above_272k_tokens": 5e-07},
	"gpt-5.5-pro": {"provider": "openai", "mode": "chat",
		"input_cost_per_token": 3e-05, "output_cost_per_token": 1.8e-04,
		"input_cost_per_token_above_272k_tokens": 6e-05,
		"output_cost_per_token_above_272k_tokens": 2.7e-04}
}`
)

// TestCalculateCost_RateMultiplier_NegativeClampedToZero checks that negative multipliers produce zero charges.
func TestCalculateCost_RateMultiplier_NegativeClampedToZero(t *testing.T) {
	svc := newTestCalculator()
	tokens := billingpricing.UsageTokens{InputTokens: 1000, OutputTokens: 500}

	tests := []struct {
		name       string
		multiplier float64
		wantRatio  float64 // ActualCost / TotalCost
	}{
		{"negative clamped to 0", -1.5, 0},
		{"zero passes through as 0 (defense in depth)", 0, 0},
		{"positive 2x applied", 2.0, 2.0},
		{"positive 0.5x applied", 0.5, 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost, err := svc.CalculateCost("claude-sonnet-4", tokens, tt.multiplier)
			require.NoError(t, err)
			require.Greater(t, cost.TotalCost, 0.0, "TotalCost should be non-zero")
			require.InDelta(t, tt.wantRatio*cost.TotalCost, cost.ActualCost, 1e-9)
		})
	}
}

// TestCalculateImageCost_RateMultiplier_NegativeClampedToZero checks that image charges clamp negative multipliers
// to zero.
func TestCalculateImageCost_RateMultiplier_NegativeClampedToZero(t *testing.T) {
	svc := newTestCalculator()

	tests := []struct {
		name       string
		multiplier float64
		wantRatio  float64
	}{
		{"negative clamped to 0", -0.5, 0},
		{"zero passes through", 0, 0},
		{"positive 3x applied", 3.0, 3.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost, mediaErr := svc.CalculateImageCost("grok-imagine-image-quality", "1K", 2, tt.multiplier)
			if mediaErr != nil {
				t.Fatal(mediaErr)
			}
			require.NotNil(t, cost)
			require.Greater(t, cost.TotalCost, 0.0)
			require.InDelta(t, tt.wantRatio*cost.TotalCost, cost.ActualCost, 1e-9)
		})
	}
}

func TestCalculateCost_BasicComputation(t *testing.T) {
	svc := newTestCalculator()

	// The fixture prices claude-sonnet-4 at $3/MTok input and $15/MTok output.
	tokens := billingpricing.UsageTokens{
		InputTokens:  1000,
		OutputTokens: 500,
	}
	cost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	// 1000 * 3e-6 = 0.003, 500 * 15e-6 = 0.0075
	expectedInput := 1000 * 3e-6
	expectedOutput := 500 * 15e-6
	require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
	require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.TotalCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.ActualCost, 1e-10)
}

func TestCalculateCost_WithCacheTokens(t *testing.T) {
	svc := newTestCalculator()

	tokens := billingpricing.UsageTokens{
		InputTokens:         1000,
		OutputTokens:        500,
		CacheCreationTokens: 2000,
		CacheReadTokens:     3000,
	}
	cost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	expectedCacheCreation := 2000 * 3.75e-6
	expectedCacheRead := 3000 * 0.3e-6
	require.InDelta(t, expectedCacheCreation, cost.CacheCreationCost, 1e-10)
	require.InDelta(t, expectedCacheRead, cost.CacheReadCost, 1e-10)

	expectedTotal := cost.InputCost + cost.OutputCost + expectedCacheCreation + expectedCacheRead
	require.InDelta(t, expectedTotal, cost.TotalCost, 1e-10)
}

func TestCalculateCost_RateMultiplier(t *testing.T) {
	svc := newTestCalculator()

	tokens := billingpricing.UsageTokens{InputTokens: 1000, OutputTokens: 500}

	cost1x, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	cost2x, err := svc.CalculateCost("claude-sonnet-4", tokens, 2.0)
	require.NoError(t, err)

	// The multiplier leaves TotalCost unchanged and doubles ActualCost.
	require.InDelta(t, cost1x.TotalCost, cost2x.TotalCost, 1e-10)
	require.InDelta(t, cost1x.ActualCost*2, cost2x.ActualCost, 1e-10)
}

func TestGetModelPricing_FallbackMatchesByFamily(t *testing.T) {
	svc := newTestCalculator()

	tests := []struct {
		model         string
		expectedInput float64
	}{
		{"claude-opus-4.5-20250101", 5e-6},
		{"claude-opus-4-8", 5e-6},
		{"claude-3-opus-20240229", 15e-6},
		{"claude-sonnet-4-20250514", 3e-6},
		{"claude-3-5-sonnet-20241022", 3e-6},
		{"claude-3-5-haiku-20241022", 1e-6},
		{"claude-3-haiku-20240307", 0.25e-6},
	}

	for _, tt := range tests {
		pricing, err := svc.GetModelPricing(tt.model)
		if tt.model != "claude-opus-4-8" {
			require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable)
			require.Nil(t, pricing)
			continue
		}
		require.NoError(t, err, "模型 %s", tt.model)
		require.InDelta(t, tt.expectedInput, pricing.InputPricePerToken, 1e-12, "模型 %s 输入价格", tt.model)
	}
}

func TestGetModelPricing_CaseInsensitive(t *testing.T) {
	svc := newTestCalculator()

	p1, err := svc.GetModelPricing("Claude-Sonnet-4")
	require.NoError(t, err)

	p2, err := svc.GetModelPricing("claude-sonnet-4")
	require.NoError(t, err)

	require.Equal(t, p1.InputPricePerToken, p2.InputPricePerToken)
}

func TestGetModelPricing_GLM52UsesOwnPrice(t *testing.T) {
	svc := newTestCalculator()

	pricing, err := svc.GetModelPricing("glm-5.2")
	require.NoError(t, err)
	require.NotNil(t, pricing)
	// GLM-5.2 uses its own fixture rates, matching GLM-5.1; a glm-5 substring must not select another price.
	require.InDelta(t, 1.4e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 4.4e-6, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, 0.26e-6, pricing.CacheReadPricePerToken, 1e-12)
}

func TestGetModelPricing_UnknownClaudeModelIsUnpriced(t *testing.T) {
	value, err := newTestCalculator().GetModelPricing("claude-unknown-model")
	require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable)
	require.Nil(t, value)
}

func TestGetModelPricing_UnknownOpenAIModelReturnsError(t *testing.T) {
	svc := newTestCalculator()

	pricing, err := svc.GetModelPricing("gpt-unknown-model")
	require.Error(t, err)
	require.Nil(t, pricing)
	require.Contains(t, err.Error(), "pricing not found")
}

func TestGetModelPricing_OpenAIGPT54Fallback(t *testing.T) {
	svc := newTestCalculator()

	pricing, err := svc.GetModelPricing("gpt-5.4")
	require.NoError(t, err)
	require.NotNil(t, pricing)
	require.InDelta(t, 2.5e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 15e-6, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, 0.25e-6, pricing.CacheReadPricePerToken, 1e-12)
	// Long-context tiers require above-threshold catalog fields, which this fixture omits.
	require.Zero(t, pricing.LongContextInputThreshold)
	require.Zero(t, pricing.LongContextInputMultiplier)
	require.Zero(t, pricing.LongContextOutputMultiplier)
}

func TestGetModelPricing_CatalogAboveTierFieldsDriveLongContext(t *testing.T) {
	svc := newLadderCalculator(t)

	pricing, err := svc.GetModelPricing("gpt-5.4")
	require.NoError(t, err)
	require.Equal(t, 272000, pricing.LongContextInputThreshold)
	require.InDelta(t, 2.0, pricing.LongContextInputMultiplier, 1e-12)
	require.InDelta(t, 1.5, pricing.LongContextOutputMultiplier, 1e-12)
	require.False(t, pricing.LongContextThresholdInclusive)
}

func TestGetModelPricing_OpenAIImplicitAliasesAreUnpriced(t *testing.T) {
	svc := newTestCalculator()
	for _, model := range []string{"gpt5.5", "gpt-5.5-pro-high", "openai/gpt5.5pro", "openai/gpt5.4", "gpt5.4-mini", "gpt5.3codexspark"} {
		value, err := svc.GetModelPricing(model)
		require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable, model)
		require.Nil(t, value)
	}
}

func TestGetModelPricing_OpenAIGPT54MiniFallback(t *testing.T) {
	svc := newTestCalculator()

	pricing, err := svc.GetModelPricing("gpt-5.4-mini")
	require.NoError(t, err)
	require.NotNil(t, pricing)
	require.InDelta(t, 7.5e-7, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 4.5e-6, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, 7.5e-8, pricing.CacheReadPricePerToken, 1e-12)
	require.Zero(t, pricing.LongContextInputThreshold)
}

func TestCalculateCost_OpenAIGPT54LongContextAppliesWholeSessionMultipliers(t *testing.T) {
	svc := newLadderCalculator(t)

	tokens := billingpricing.UsageTokens{
		InputTokens:  300000,
		OutputTokens: 4000,
	}

	cost, err := svc.CalculateCost("gpt-5.4", tokens, 1.0)
	require.NoError(t, err)

	expectedInput := float64(tokens.InputTokens) * 2.5e-6 * 2.0
	expectedOutput := float64(tokens.OutputTokens) * 15e-6 * 1.5
	require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
	require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.TotalCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.ActualCost, 1e-10)
	require.True(t, cost.LongContextBillingApplied)
}

func TestCalculateCost_OpenAIGPT54LongContextMarkerRequiresActualCostIncrease(t *testing.T) {
	svc := newLadderCalculator(t)

	cost, err := svc.CalculateCostWithServiceTier(
		"gpt-5.4",
		billingpricing.UsageTokens{InputTokens: 300000},
		0,
		"",
	)

	require.NoError(t, err)
	require.Zero(t, cost.ActualCost)
	require.False(t, cost.LongContextBillingApplied)
}

func TestCalculateCost_OpenAILongContextBoundaryIncludesCacheTokens(t *testing.T) {
	svc := newLadderCalculator(t)
	tests := []struct {
		name        string
		cacheRead   int
		wantApplied bool
	}{
		{name: "exact threshold uses base price", cacheRead: 72000, wantApplied: false},
		{name: "one token above threshold uses long price", cacheRead: 72001, wantApplied: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost, err := svc.CalculateCost("gpt-5.4", billingpricing.UsageTokens{
				InputTokens:         100000,
				CacheCreationTokens: 100000,
				CacheReadTokens:     tt.cacheRead,
				OutputTokens:        1000,
			}, 1)

			require.NoError(t, err)
			require.Equal(t, tt.wantApplied, cost.LongContextBillingApplied)
		})
	}
}

func TestCalculateCost_GPT56SolMarketplaceIntervalsMatchSettlement(t *testing.T) {
	svc := newCalculator(newStubCatalogFromJSON(t, gpt56LadderCatalogJSON))
	const groupRate = 3.0

	display := svc.DisplayPricing("gpt-5.6-sol", groupRate)
	require.Equal(t, "token", display.PricingMode)
	require.Len(t, display.ContextIntervals, 2)
	baseInterval := display.ContextIntervals[0]
	longInterval := display.ContextIntervals[1]
	require.NotNil(t, baseInterval.MaxTokens)
	require.Equal(t, 272000, *baseInterval.MaxTokens)
	require.Equal(t, 272000, longInterval.MinTokens)
	require.Nil(t, longInterval.MaxTokens)
	require.InDelta(t, 15.0, baseInterval.InputPricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 90.0, baseInterval.OutputPricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 18.75, baseInterval.CacheWritePricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 1.5, baseInterval.CacheReadPricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 30.0, baseInterval.FastInputPricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 180.0, baseInterval.FastOutputPricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 37.5, baseInterval.FastCacheWritePricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 3.0, baseInterval.FastCacheReadPricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 30.0, longInterval.InputPricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 135.0, longInterval.OutputPricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 37.5, longInterval.CacheWritePricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 3.0, longInterval.CacheReadPricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 60.0, longInterval.FastInputPricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 270.0, longInterval.FastOutputPricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 75.0, longInterval.FastCacheWritePricePerToken*1_000_000, 1e-10)
	require.InDelta(t, 6.0, longInterval.FastCacheReadPricePerToken*1_000_000, 1e-10)

	tokens := billingpricing.UsageTokens{
		InputTokens:         100000,
		CacheCreationTokens: 100000,
		CacheReadTokens:     72001,
		OutputTokens:        1000,
	}
	tiers := []struct {
		name, serviceTier               string
		inputPrice, outputPrice         float64
		cacheWritePrice, cacheReadPrice float64
	}{
		{
			name:            "standard",
			inputPrice:      longInterval.InputPricePerToken,
			outputPrice:     longInterval.OutputPricePerToken,
			cacheWritePrice: longInterval.CacheWritePricePerToken,
			cacheReadPrice:  longInterval.CacheReadPricePerToken,
		},
		{
			name:            "priority",
			serviceTier:     "priority",
			inputPrice:      longInterval.FastInputPricePerToken,
			outputPrice:     longInterval.FastOutputPricePerToken,
			cacheWritePrice: longInterval.FastCacheWritePricePerToken,
			cacheReadPrice:  longInterval.FastCacheReadPricePerToken,
		},
	}
	for _, tier := range tiers {
		t.Run(tier.name, func(t *testing.T) {
			cost, err := svc.CalculateCostWithServiceTier("gpt-5.6-sol", tokens, groupRate, tier.serviceTier)
			require.NoError(t, err)
			require.True(t, cost.LongContextBillingApplied)
			require.InDelta(t, float64(tokens.InputTokens)*tier.inputPrice, cost.InputCost*groupRate, 1e-10)
			require.InDelta(t, float64(tokens.OutputTokens)*tier.outputPrice, cost.OutputCost*groupRate, 1e-10)
			require.InDelta(t, float64(tokens.CacheCreationTokens)*tier.cacheWritePrice, cost.CacheCreationCost*groupRate, 1e-10)
			require.InDelta(t, float64(tokens.CacheReadTokens)*tier.cacheReadPrice, cost.CacheReadCost*groupRate, 1e-10)
			wantActualCost := float64(tokens.InputTokens)*tier.inputPrice +
				float64(tokens.OutputTokens)*tier.outputPrice +
				float64(tokens.CacheCreationTokens)*tier.cacheWritePrice +
				float64(tokens.CacheReadTokens)*tier.cacheReadPrice
			require.InDelta(t, wantActualCost, cost.ActualCost, 1e-10)
		})
	}
}

func TestApplyLongContextDisplayMultipliersScalesAllCachePrices(t *testing.T) {
	pricing := &billingpricing.ModelPricing{
		InputPricePerToken:                 1,
		InputPricePerTokenPriority:         2,
		OutputPricePerToken:                3,
		OutputPricePerTokenPriority:        4,
		CacheCreationPricePerToken:         5,
		CacheCreationPricePerTokenPriority: 6,
		CacheCreation5mPrice:               7,
		CacheCreation1hPrice:               8,
		CacheReadPricePerToken:             9,
		CacheReadPricePerTokenPriority:     10,
		LongContextInputMultiplier:         2,
		LongContextOutputMultiplier:        1.5,
	}

	adjusted := billingpricing.ApplyLongContextDisplayMultipliers(pricing)

	require.NotSame(t, pricing, adjusted)
	require.Equal(t, float64(1), pricing.InputPricePerToken)
	require.Equal(t, float64(2), adjusted.InputPricePerToken)
	require.Equal(t, float64(4), adjusted.InputPricePerTokenPriority)
	require.Equal(t, 4.5, adjusted.OutputPricePerToken)
	require.Equal(t, float64(6), adjusted.OutputPricePerTokenPriority)
	require.Equal(t, float64(10), adjusted.CacheCreationPricePerToken)
	require.Equal(t, float64(12), adjusted.CacheCreationPricePerTokenPriority)
	require.Equal(t, float64(14), adjusted.CacheCreation5mPrice)
	require.Equal(t, float64(16), adjusted.CacheCreation1hPrice)
	require.Equal(t, float64(18), adjusted.CacheReadPricePerToken)
	require.Equal(t, float64(20), adjusted.CacheReadPricePerTokenPriority)
}

func TestCalculateCostUnified_ExplicitIntervalsDoNotReapplyLongContextMultiplier(t *testing.T) {
	svc := newTestCalculator()
	resolver := testkit.PriceResolver(nil, svc)
	basePricing, err := svc.GetModelPricing("gpt-5.6-sol")
	require.NoError(t, err)

	shortMax := 272000
	shortInput := 7e-6
	shortOutput := 31e-6
	shortCacheWrite := 3e-6
	shortCacheRead := 0.3e-6
	longInput := 11e-6
	longOutput := 41e-6
	longCacheWrite := 4e-6
	longCacheRead := 0.4e-6
	resolved := &billingpricing.ResolvedPricing{
		Mode:        routing.BillingModeToken,
		BasePricing: basePricing,
		Source:      billingpricing.PricingSourceConfig,
		Intervals: []routing.PricingInterval{
			{
				MinTokens: 0, MaxTokens: &shortMax,
				InputPrice: &shortInput, OutputPrice: &shortOutput,
				CacheWritePrice: &shortCacheWrite, CacheReadPrice: &shortCacheRead,
			},
			{
				MinTokens:  shortMax,
				InputPrice: &longInput, OutputPrice: &longOutput,
				CacheWritePrice: &longCacheWrite, CacheReadPrice: &longCacheRead,
			},
		},
	}
	tokens := billingpricing.UsageTokens{
		InputTokens:         100000,
		CacheCreationTokens: 100000,
		CacheReadTokens:     72001,
		OutputTokens:        1000,
	}
	const groupRate = 2.0

	cost, err := svc.CalculateCostUnified(billing.CostInput{
		Ctx:            context.Background(),
		Model:          "gpt-5.6-sol",
		Tokens:         tokens,
		RateMultiplier: groupRate,
		Resolver:       resolver,
		Resolved:       resolved,
	})

	require.NoError(t, err)
	require.False(t, cost.LongContextBillingApplied)
	require.InDelta(t, float64(tokens.InputTokens)*longInput, cost.InputCost, 1e-10)
	require.InDelta(t, float64(tokens.OutputTokens)*longOutput, cost.OutputCost, 1e-10)
	require.InDelta(t, float64(tokens.CacheCreationTokens)*longCacheWrite, cost.CacheCreationCost, 1e-10)
	require.InDelta(t, float64(tokens.CacheReadTokens)*longCacheRead, cost.CacheReadCost, 1e-10)
	wantActualCost := (float64(tokens.InputTokens)*longInput +
		float64(tokens.OutputTokens)*longOutput +
		float64(tokens.CacheCreationTokens)*longCacheWrite +
		float64(tokens.CacheReadTokens)*longCacheRead) * groupRate
	require.InDelta(t, wantActualCost, cost.ActualCost, 1e-10)

	display, ok := billingpricing.DisplayPricingFromResolved("gpt-5.6-sol", groupRate, resolved)
	require.True(t, ok)
	require.Len(t, display.ContextIntervals, 2)
	displayLong := display.ContextIntervals[1]
	require.InDelta(t, longInput*groupRate, displayLong.InputPricePerToken, 1e-12)
	require.InDelta(t, longOutput*groupRate, displayLong.OutputPricePerToken, 1e-12)
	require.InDelta(t, longCacheWrite*groupRate, displayLong.CacheWritePricePerToken, 1e-12)
	require.InDelta(t, longCacheRead*groupRate, displayLong.CacheReadPricePerToken, 1e-12)
}

func TestCalculateCost_OpenAIGPT55ProLongContextAppliesWholeSessionMultipliers(t *testing.T) {
	svc := newLadderCalculator(t)

	tokens := billingpricing.UsageTokens{
		InputTokens:  300000,
		OutputTokens: 4000,
	}

	cost, err := svc.CalculateCost("gpt-5.5-pro", tokens, 1.0)
	require.NoError(t, err)

	expectedInput := float64(tokens.InputTokens) * 30e-6 * 2.0
	expectedOutput := float64(tokens.OutputTokens) * 180e-6 * 1.5
	require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
	require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.TotalCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.ActualCost, 1e-10)
}

// TestCalculateCost_OpenAIGPT54LongContextAppliesMultiplierToCacheRead covers issue #2293: cache reads use
// LongContextInputMultiplier.
// Here CacheReadCost = tokens * 0.25e-6 * 2.0.
func TestCalculateCost_OpenAIGPT54LongContextAppliesMultiplierToCacheRead(t *testing.T) {
	svc := newLadderCalculator(t)

	// InputTokens + CacheReadTokens = 301000, above the 272000 threshold.
	tokens := billingpricing.UsageTokens{
		InputTokens:     1000,
		CacheReadTokens: 300000,
		OutputTokens:    1000,
	}

	cost, err := svc.CalculateCost("gpt-5.4", tokens, 1.0)
	require.NoError(t, err)

	expectedInput := float64(tokens.InputTokens) * 2.5e-6 * 2.0
	expectedOutput := float64(tokens.OutputTokens) * 15e-6 * 1.5
	expectedCacheRead := float64(tokens.CacheReadTokens) * 0.25e-6 * 2.0

	require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
	require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
	require.InDelta(t, expectedCacheRead, cost.CacheReadCost, 1e-10,
		"cache_read_cost should be scaled by LongContextInputMultiplier when long-context pricing applies (issue #2293)")

	expectedTotal := expectedInput + expectedOutput + expectedCacheRead
	require.InDelta(t, expectedTotal, cost.TotalCost, 1e-10)
	require.InDelta(t, expectedTotal, cost.ActualCost, 1e-10)
}

// TestCalculateCost_OpenAIGPT54NoLongContextKeepsCacheReadAtBasePrice checks base cache-read rates below the threshold.
func TestCalculateCost_OpenAIGPT54NoLongContextKeepsCacheReadAtBasePrice(t *testing.T) {
	svc := newLadderCalculator(t)

	// InputTokens + CacheReadTokens = 101000, below the 272000 threshold.
	tokens := billingpricing.UsageTokens{
		InputTokens:     1000,
		CacheReadTokens: 100000,
		OutputTokens:    1000,
	}

	cost, err := svc.CalculateCost("gpt-5.4", tokens, 1.0)
	require.NoError(t, err)

	expectedCacheRead := float64(tokens.CacheReadTokens) * 0.25e-6
	require.InDelta(t, expectedCacheRead, cost.CacheReadCost, 1e-10,
		"cache_read_cost should remain at base price when below long-context threshold")
}

// TestCalculateCost_OpenAIGPT54LongContextAppliesMultiplierToCacheCreation covers issue #2816: cache writes use
// LongContextInputMultiplier.
// Cache-write rates are read separately; omitting the 2x multiplier would halve their charge.
func TestCalculateCost_OpenAIGPT54LongContextAppliesMultiplierToCacheCreation(t *testing.T) {
	svc := newLadderCalculator(t)

	// InputTokens + CacheReadTokens = 301000, above the 272000 threshold.
	tokens := billingpricing.UsageTokens{
		InputTokens:         1000,
		CacheReadTokens:     300000,
		CacheCreationTokens: 10000,
		OutputTokens:        1000,
	}

	cost, err := svc.CalculateCost("gpt-5.4", tokens, 1.0)
	require.NoError(t, err)

	// The fixture sets CacheCreationPricePerToken to 2.5e-6 and LongContextInputMultiplier to 2.0.
	expectedCacheCreation := float64(tokens.CacheCreationTokens) * 2.5e-6 * 2.0
	require.InDelta(t, expectedCacheCreation, cost.CacheCreationCost, 1e-10,
		"cache_creation_cost should be scaled by LongContextInputMultiplier when long-context pricing applies")
}

// TestCalculateCost_OpenAIGPT54NoLongContextKeepsCacheCreationAtBasePrice checks base cache-write rates below the
// threshold.
func TestCalculateCost_OpenAIGPT54NoLongContextKeepsCacheCreationAtBasePrice(t *testing.T) {
	svc := newLadderCalculator(t)

	// InputTokens + CacheReadTokens = 101000, below the 272000 threshold.
	tokens := billingpricing.UsageTokens{
		InputTokens:         1000,
		CacheReadTokens:     100000,
		CacheCreationTokens: 10000,
		OutputTokens:        1000,
	}

	cost, err := svc.CalculateCost("gpt-5.4", tokens, 1.0)
	require.NoError(t, err)

	expectedCacheCreation := float64(tokens.CacheCreationTokens) * 2.5e-6
	require.InDelta(t, expectedCacheCreation, cost.CacheCreationCost, 1e-10,
		"cache_creation_cost should remain at base price when below long-context threshold")
}

// TestCalculateCost_LongContextAppliesMultiplierToCacheCreation5mAnd1h checks that both ephemeral cache-write
// tiers receive the long-context multiplier.
// A custom price fixture controls both cache breakdown support
// and the long-context threshold.
func TestCalculateCost_LongContextAppliesMultiplierToCacheCreation5mAnd1h(t *testing.T) {
	svc := newCalculatorWithPrices(nil, map[string]*billingpricing.ModelPricing{
		"claude-sonnet-4": {
			InputPricePerToken:          3e-6,
			OutputPricePerToken:         15e-6,
			CacheReadPricePerToken:      0.3e-6,
			SupportsCacheBreakdown:      true,
			CacheCreation5mPrice:        4e-6,
			CacheCreation1hPrice:        5e-6,
			LongContextInputThreshold:   272000,
			LongContextInputMultiplier:  2.0,
			LongContextOutputMultiplier: 1.5,
		},
	})

	// InputTokens + CacheReadTokens = 301000, above the 272000 threshold.
	tokens := billingpricing.UsageTokens{
		InputTokens:           1000,
		CacheReadTokens:       300000,
		CacheCreation5mTokens: 8000,
		CacheCreation1hTokens: 4000,
		OutputTokens:          1000,
	}

	cost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	expected5m := float64(tokens.CacheCreation5mTokens) * 4e-6 * 2.0
	expected1h := float64(tokens.CacheCreation1hTokens) * 5e-6 * 2.0
	require.InDelta(t, expected5m+expected1h, cost.CacheCreationCost, 1e-10,
		"both 5m and 1h cache_creation prices should be scaled by LongContextInputMultiplier")
}

func TestGetModelPricing_DoubaoEmbeddingVisionImageInputRate(t *testing.T) {
	svc := newTestCalculator()

	for _, model := range []string{
		"doubao-embedding-vision",
		"doubao-embedding-vision-251215",
		"Doubao-Embedding-Vision",
	} {
		pricing, err := svc.GetModelPricing(model)
		if model == "grok-4.5-latest" || model == "grok-4.6-latest" || model == "grok-4.20-reasoning" || model == "grok-4.20-non-reasoning" || model == "grok-build" || model == "grok-composer" || model == "composer-2.5" || model == "doubao-embedding-vision-251215" {
			require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable)
			require.Nil(t, pricing)
			continue
		}
		require.NoError(t, err)
		require.InDelta(t, 0.098e-6, pricing.InputPricePerToken, 1e-12)
		require.InDelta(t, 0.252e-6, pricing.ImageInputPricePerToken, 1e-12)
		require.Zero(t, pricing.OutputPricePerToken)
	}
}

// TestCalculateCost_DoubaoEmbeddingVisionDifferentialInput checks separate text and image input charges.
// Zero image tokens use text rates; excess image tokens are capped at total input, leaving no text charge.
func TestCalculateCost_DoubaoEmbeddingVisionDifferentialInput(t *testing.T) {
	svc := newTestCalculator()

	// Mixed input has 1340 prompt tokens: 28 image and 1312 text tokens.
	cost, err := svc.CalculateCost("doubao-embedding-vision", billingpricing.UsageTokens{
		InputTokens:      1340,
		ImageInputTokens: 28,
	}, 1.0)
	require.NoError(t, err)
	wantText := float64(1312) * 0.098e-6
	wantImage := float64(28) * 0.252e-6
	require.InDelta(t, wantText, cost.InputCost, 1e-15, "InputCost 仅计文本输入")
	require.InDelta(t, wantImage, cost.ImageInputCost, 1e-15, "ImageInputCost 单独计图片输入")
	require.InDelta(t, wantText+wantImage, cost.TotalCost, 1e-15, "TotalCost 口径不变")
	require.Zero(t, cost.OutputCost)

	// Text-only input uses the text rate throughout.
	textOnly := billingpricing.UsageTokens{InputTokens: 1340}
	costText, err := svc.CalculateCost("doubao-embedding-vision", textOnly, 1.0)
	require.NoError(t, err)
	require.InDelta(t, float64(1340)*0.098e-6, costText.InputCost, 1e-15)
	require.Zero(t, costText.ImageInputCost)

	// Cap upstream image token counts at total input tokens.
	costWeird, err := svc.CalculateCost("doubao-embedding-vision", billingpricing.UsageTokens{
		InputTokens:      10,
		ImageInputTokens: 50,
	}, 1.0)
	require.NoError(t, err)
	require.Zero(t, costWeird.InputCost, "全为图片输入时文本费用为 0")
	require.InDelta(t, float64(10)*0.252e-6, costWeird.ImageInputCost, 1e-15)
	require.InDelta(t, float64(10)*0.252e-6, costWeird.TotalCost, 1e-15)
}

// TestComputeTokenBreakdown_GptImage2ImageEditIssue4386 covers one input image in gpt-image-2 edits from issue #4386.
// The fixture has 371 input tokens (352 image and 19 text) and 439 image output tokens.
// Rates per million tokens are $5 text input, $8 image input, $10 text output, and $30 image output.
// Charging image input at the text rate would undercharge this request by about 6.6%.
func TestComputeTokenBreakdown_GptImage2ImageEditIssue4386(t *testing.T) {
	svc := newTestCalculator()

	pricing := &billingpricing.ModelPricing{
		InputPricePerToken:       5e-6,
		ImageInputPricePerToken:  8e-6,
		OutputPricePerToken:      10e-6,
		ImageOutputPricePerToken: 30e-6,
		ImageOutputPriceExplicit: true,
	}
	tokens := billingpricing.UsageTokens{
		InputTokens:       371,
		ImageInputTokens:  352,
		OutputTokens:      439,
		ImageOutputTokens: 439,
	}

	cost := svc.ComputeTokenBreakdown(pricing, tokens, 1.0, "", false)

	wantTextInput := float64(19) * 5e-6     // 0.000095
	wantImageInput := float64(352) * 8e-6   // 0.002816
	wantImageOutput := float64(439) * 30e-6 // 0.013170
	require.InDelta(t, wantTextInput, cost.InputCost, 1e-15, "InputCost 仅含文本输入")
	require.InDelta(t, wantImageInput, cost.ImageInputCost, 1e-15, "图片输入按 $8/1M 独立计费")
	require.Zero(t, cost.OutputCost, "输出全部为图片，文本输出费用为 0")
	require.InDelta(t, wantImageOutput, cost.ImageOutputCost, 1e-15)
	require.InDelta(t, 0.016081, cost.TotalCost, 1e-9, "总额应为 $0.016081（修复前为 $0.015025）")
}

func TestCalculateImageCost(t *testing.T) {
	svc := newTestCalculator()

	cost, mediaErr := svc.CalculateImageCost("grok-imagine-image-quality", "1K", 3, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}

	require.InDelta(t, 0.05*3, cost.TotalCost, 1e-10)
	require.InDelta(t, 0.05*3, cost.ActualCost, 1e-10)
}

func TestCalculateVideoCostBillsPerSecond(t *testing.T) {
	svc := newTestCalculator()

	oneSecond, mediaErr := svc.CalculateVideoCost("grok-imagine-video", "720p", 1, 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	fifteenSeconds, mediaErr := svc.CalculateVideoCost("grok-imagine-video", "720p", 1, 15, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	// Nonpositive duration defaults to 8 seconds; duration above 15 seconds is capped at 15.
	defaultDuration, mediaErr := svc.CalculateVideoCost("grok-imagine-video", "720p", 1, 0, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	clampedDuration, mediaErr := svc.CalculateVideoCost("grok-imagine-video", "720p", 1, 999, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}

	require.InDelta(t, 0.07, oneSecond.TotalCost, 1e-10)
	require.InDelta(t, 0.07*15, fifteenSeconds.TotalCost, 1e-10)
	require.InDelta(t, 0.07*8, defaultDuration.TotalCost, 1e-10)
	require.InDelta(t, 0.07*15, clampedDuration.TotalCost, 1e-10)
}

func TestCalculateGrokImagineImageCostUsesDefaultRateCard(t *testing.T) {
	svc := newTestCalculator()

	standard1K, mediaErr := svc.CalculateImageCost("grok-imagine-image", "1K", 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	standard2K, mediaErr := svc.CalculateImageCost("grok-imagine-image", "2K", 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	quality1K, mediaErr := svc.CalculateImageCost("grok-imagine-image-quality", "1K", 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	quality2K, mediaErr := svc.CalculateImageCost("grok-imagine-image-quality", "2K", 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}

	require.InDelta(t, 0.02, standard1K.TotalCost, 1e-10)
	require.InDelta(t, 0.02, standard2K.TotalCost, 1e-10)
	require.InDelta(t, 0.05, quality1K.TotalCost, 1e-10)
	require.InDelta(t, 0.07, quality2K.TotalCost, 1e-10)
}

func TestCalculateGrokImagineVideoCostUsesDefaultRateCard(t *testing.T) {
	svc := newTestCalculator()

	// Use one second to check the fixture's per-second rates.
	standard480P, mediaErr := svc.CalculateVideoCost("grok-imagine-video", "480p", 1, 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	standard720P, mediaErr := svc.CalculateVideoCost("grok-imagine-video", "720p", 1, 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	video15_480P, mediaErr := svc.CalculateVideoCost("grok-imagine-video-1.5", "480p", 1, 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	video15_720P, mediaErr := svc.CalculateVideoCost("grok-imagine-video-1.5", "720p", 1, 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	video15_1080P, mediaErr := svc.CalculateVideoCost("grok-imagine-video-1.5", "1080p", 1, 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}

	require.InDelta(t, 0.05, standard480P.TotalCost, 1e-10)
	require.InDelta(t, 0.07, standard720P.TotalCost, 1e-10)
	require.InDelta(t, 0.08, video15_480P.TotalCost, 1e-10)
	require.InDelta(t, 0.14, video15_720P.TotalCost, 1e-10)
	require.InDelta(t, 0.25, video15_1080P.TotalCost, 1e-10)
}

func TestCalculateCost_ZeroTokens(t *testing.T) {
	svc := newTestCalculator()

	cost, err := svc.CalculateCost("claude-sonnet-4", billingpricing.UsageTokens{}, 1.0)
	require.NoError(t, err)
	require.Equal(t, 0.0, cost.TotalCost)
	require.Equal(t, 0.0, cost.ActualCost)
}

func TestForceUpdatePricing_NilService(t *testing.T) {
	svc := billing.NewCalculator(nil, billing.CalculatorOptions{})

	err := svc.ForceUpdatePricing()
	require.Error(t, err)
	require.Contains(t, err.Error(), "not initialized")
}

func TestGetModelPricing_Grok45OfficialFallback(t *testing.T) {
	svc := newTestCalculator()

	for _, model := range []string{"grok-4.5", "grok-4.5-latest"} {
		t.Run(model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(model)
			if model == "grok-4.5-latest" || model == "grok-4.6-latest" || model == "grok-4.20-reasoning" || model == "grok-4.20-non-reasoning" || model == "grok-build" || model == "grok-composer" || model == "composer-2.5" || model == "doubao-embedding-vision-251215" {
				require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable)
				require.Nil(t, pricing)
				return
			}
			require.NoError(t, err)
			require.InDelta(t, 2e-6, pricing.InputPricePerToken, 1e-12)
			require.InDelta(t, 6e-6, pricing.OutputPricePerToken, 1e-12)
			require.InDelta(t, 0.3e-6, pricing.CacheReadPricePerToken, 1e-12)
			require.False(t, pricing.SupportsCacheBreakdown)
		})
	}
}

func TestGetModelPricing_GrokBareAliasesUseGrok46(t *testing.T) {
	svc := newTestCalculator()
	for _, model := range []string{"grok", "grok-latest"} {
		pricing, err := svc.GetModelPricing(model)
		require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable)
		require.Nil(t, pricing)
	}
}

func TestGetModelPricing_Grok46OfficialFallback(t *testing.T) {
	svc := newTestCalculator()

	for _, model := range []string{"grok-4.6", "grok-4.6-latest"} {
		t.Run(model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(model)
			if model == "grok-4.5-latest" || model == "grok-4.6-latest" || model == "grok-4.20-reasoning" || model == "grok-4.20-non-reasoning" || model == "grok-build" || model == "grok-composer" || model == "composer-2.5" || model == "doubao-embedding-vision-251215" {
				require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable)
				require.Nil(t, pricing)
				return
			}
			require.NoError(t, err)
			require.InDelta(t, 2e-6, pricing.InputPricePerToken, 1e-12)
			require.InDelta(t, 6e-6, pricing.OutputPricePerToken, 1e-12)
			require.InDelta(t, 0.5e-6, pricing.CacheReadPricePerToken, 1e-12)
			require.Equal(t, 200000, pricing.LongContextInputThreshold)
			require.InDelta(t, 2.0, pricing.LongContextInputMultiplier, 1e-12)
			require.InDelta(t, 2.0, pricing.LongContextOutputMultiplier, 1e-12)
			require.False(t, pricing.SupportsCacheBreakdown)
		})
	}
}

func TestGetModelPricing_GrokOfficialFamilyCards(t *testing.T) {
	svc := newTestCalculator()
	for _, tc := range []struct {
		model                 string
		input, cached, output float64
	}{
		{"grok-4.3", 1.25e-6, 0.2e-6, 2.5e-6},
		{"grok-4.20-0309-reasoning", 1.25e-6, 0.2e-6, 2.5e-6},
		{"grok-build-0.1", 1e-6, 0.2e-6, 2e-6},
	} {
		p, err := svc.GetModelPricing(tc.model)
		require.NoError(t, err, tc.model)
		require.InDelta(t, tc.input, p.InputPricePerToken, 1e-12)
		require.InDelta(t, tc.cached, p.CacheReadPricePerToken, 1e-12)
		require.InDelta(t, tc.output, p.OutputPricePerToken, 1e-12)
		require.Equal(t, 200000, p.LongContextInputThreshold)
	}
}

func TestCalculateCostUnified_ConfigLongContextToggleUsesPresetLadder(t *testing.T) {
	svc := newTestCalculator()
	settings := billingpricing.DefaultBillingSettings()
	settings.LongContextPricingEnabled = false
	resolver, source := settingsResolver(svc, settings, nil)
	tokens := billingpricing.UsageTokens{InputTokens: 250000, OutputTokens: 1000}

	disabled, err := svc.CalculateCostUnified(billing.CostInput{
		Model: "grok-4.5", GroupID: testkit.GroupID(), Tokens: tokens, RateMultiplier: 1, Resolver: resolver,
	})
	require.NoError(t, err)

	source.settings.LongContextPricingEnabled = true
	enabled, err := svc.CalculateCostUnified(billing.CostInput{
		Model: "grok-4.5", GroupID: testkit.GroupID(), Tokens: tokens, RateMultiplier: 1, Resolver: resolver,
	})
	require.NoError(t, err)

	require.False(t, disabled.LongContextBillingApplied)
	require.True(t, enabled.LongContextBillingApplied)
	require.InDelta(t, disabled.InputCost*2, enabled.InputCost, 1e-12)
	require.InDelta(t, disabled.OutputCost*2, enabled.OutputCost, 1e-12)
}

func TestGetModelPricing_UnknownGrokTextFallsBackToGrok46(t *testing.T) {
	svc := newTestCalculator()
	_, err := svc.GetModelPricing("grok-4.6")
	require.NoError(t, err)

	for _, model := range []string{"grok-5", "grok-5-latest", "x-ai/grok-7", "grok-4.7-beta", "grok-2-vision-1212"} {
		pricing, err := svc.GetModelPricing(model)
		require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable, model)
		require.Nil(t, pricing)
	}

	for _, model := range []string{
		"grok-2-image-1212",
		"grok-2-audio",
		"grok-5-video",
		"x-ai/grok-6-image",
		"grok-imagine-image-3.0",
		"grok-imagine-video-2",
		"grok-voice-latest",
		"grok-web-search",
		"grok-x-search",
		"grok-speech-1",
	} {
		_, err := svc.GetModelPricing(model)
		require.Error(t, err, "non-text grok family %s must not inherit grok-4.5 token rates", model)
		require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable)
	}

	// The registered price card takes precedence over a 4.5 family price.
	build, err := svc.GetModelPricing("grok-build-0.1")
	require.NoError(t, err)
	require.InDelta(t, 1e-6, build.InputPricePerToken, 1e-12)
}

func TestGetModelPricing_GrokCatalogFallbacks(t *testing.T) {
	svc := newTestCalculator()

	tests := []struct {
		name      string
		models    []string
		input     float64
		cacheRead float64
		output    float64
	}{
		{
			name: "Grok 4.3 family",
			models: []string{
				"grok-4.3",
				"grok-4.20-0309-reasoning",
				"grok-4.20-0309-non-reasoning",
				"grok-4.20-multi-agent-0309",
				"grok-4.20-reasoning",
				"grok-4.20-non-reasoning",
			},
			input:     1.25e-6,
			cacheRead: 0.2e-6,
			output:    2.5e-6,
		},
		{
			name: "Grok coding and Composer family",
			models: []string{
				"grok-build",
				"grok-build-0.1",
				"grok-composer",
				"grok-composer-2.5-fast",
				"composer-2.5",
			},
			input:     1e-6,
			cacheRead: 0.2e-6,
			output:    2e-6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, model := range tt.models {
				pricing, err := svc.GetModelPricing(model)
				if model == "grok-4.5-latest" || model == "grok-4.6-latest" || model == "grok-4.20-reasoning" || model == "grok-4.20-non-reasoning" || model == "grok-build" || model == "grok-composer" || model == "composer-2.5" || model == "doubao-embedding-vision-251215" {
					require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable)
					require.Nil(t, pricing)
					continue
				}
				require.NoError(t, err, "model %s", model)
				require.InDelta(t, tt.input, pricing.InputPricePerToken, 1e-12, "model %s input", model)
				require.InDelta(t, tt.cacheRead, pricing.CacheReadPricePerToken, 1e-12, "model %s cached input", model)
				require.InDelta(t, tt.output, pricing.OutputPricePerToken, 1e-12, "model %s output", model)
			}
		})
	}
}

func TestCalculateCost_SupportsCacheBreakdown(t *testing.T) {
	svc := newCalculatorWithPrices(nil, map[string]*billingpricing.ModelPricing{
		"claude-sonnet-4": {
			InputPricePerToken:     3e-6,
			OutputPricePerToken:    15e-6,
			SupportsCacheBreakdown: true,
			CacheCreation5mPrice:   4e-6,
			CacheCreation1hPrice:   5e-6,
		},
	})

	tokens := billingpricing.UsageTokens{
		InputTokens:           1000,
		OutputTokens:          500,
		CacheCreation5mTokens: 100000,
		CacheCreation1hTokens: 50000,
	}
	cost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	expected5m := float64(tokens.CacheCreation5mTokens) * 4e-6
	expected1h := float64(tokens.CacheCreation1hTokens) * 5e-6
	require.InDelta(t, expected5m+expected1h, cost.CacheCreationCost, 1e-10)
}

func TestComputeCacheCreationCost_CapsContradictoryBreakdownAtAggregate(t *testing.T) {
	pricing := &billingpricing.ModelPricing{
		SupportsCacheBreakdown: true,
		CacheCreation5mPrice:   1,
		CacheCreation1hPrice:   1,
	}

	tokens := billingpricing.UsageTokens{
		CacheCreationTokens:   463184,
		CacheCreation5mTokens: 463184,
		CacheCreation1hTokens: 463184,
	}

	cost := billingpricing.ComputeCacheCreationCost(pricing, tokens, 0, 1)
	require.Equal(t, float64(tokens.CacheCreationTokens), cost,
		"billed cache-creation token equivalent must not exceed the positive aggregate")
}

func TestNormalizeCacheCreationBreakdown_BillingSafetyInvariant(t *testing.T) {
	tests := []struct {
		name   string
		tokens billingpricing.UsageTokens
		want5m int
		want1h int
	}{
		{
			name:   "preserves ratio when capping",
			tokens: billingpricing.UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: 90, CacheCreation1hTokens: 60},
			want5m: 60,
			want1h: 40,
		},
		{
			name:   "details below aggregate unchanged",
			tokens: billingpricing.UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: 30, CacheCreation1hTokens: 60},
			want5m: 30,
			want1h: 60,
		},
		{
			name:   "absent 5m detail unchanged",
			tokens: billingpricing.UsageTokens{CacheCreationTokens: 100, CacheCreation1hTokens: 60},
			want5m: 0,
			want1h: 60,
		},
		{
			name:   "absent 1h detail unchanged",
			tokens: billingpricing.UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: 30},
			want5m: 30,
			want1h: 0,
		},
		{
			name:   "negative detail clamped",
			tokens: billingpricing.UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: -50, CacheCreation1hTokens: 60},
			want5m: 0,
			want1h: 60,
		},
		{
			name:   "negative detail cannot hide oversized positive detail",
			tokens: billingpricing.UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: -50, CacheCreation1hTokens: 150},
			want5m: 0,
			want1h: 100,
		},
		{
			name:   "integer boundary details capped without overflow",
			tokens: billingpricing.UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: int(^uint(0) >> 1), CacheCreation1hTokens: int(^uint(0) >> 1)},
			want5m: 50,
			want1h: 50,
		},
		{
			name:   "integer boundary aggregate avoids float conversion overflow",
			tokens: billingpricing.UsageTokens{CacheCreationTokens: int(^uint(0) >> 1), CacheCreation5mTokens: int(^uint(0) >> 1), CacheCreation1hTokens: 1},
			want5m: int(^uint(0) >> 1),
			want1h: 0,
		},
		{
			name:   "zero aggregate unchanged",
			tokens: billingpricing.UsageTokens{CacheCreation5mTokens: 90, CacheCreation1hTokens: 60},
			want5m: 90,
			want1h: 60,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got5m, got1h := billingpricing.NormalizeCacheCreationBreakdown(tt.tokens)
			require.Equal(t, tt.want5m, got5m)
			require.Equal(t, tt.want1h, got1h)
		})
	}
}

func TestComputeCacheCreationCost_PreservesZeroDetailFallback(t *testing.T) {
	pricing := &billingpricing.ModelPricing{
		SupportsCacheBreakdown: true,
		CacheCreation5mPrice:   4e-6,
		CacheCreation1hPrice:   5e-6,
	}

	tests := []struct {
		name   string
		tokens billingpricing.UsageTokens
	}{
		{name: "zero details", tokens: billingpricing.UsageTokens{CacheCreationTokens: 100}},
		{name: "one negative detail", tokens: billingpricing.UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: -25}},
		{name: "both negative details", tokens: billingpricing.UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: -25, CacheCreation1hTokens: -75}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := billingpricing.ComputeCacheCreationCost(pricing, tt.tokens, 0, 1)
			require.InDelta(t, 100*4e-6, cost, 1e-12)
		})
	}
}

func TestCalculateCost_LargeTokenCount(t *testing.T) {
	svc := newTestCalculator()

	tokens := billingpricing.UsageTokens{
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
	}
	cost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	// Input: 1M * 3e-6 = $3, Output: 1M * 15e-6 = $15
	require.InDelta(t, 3.0, cost.InputCost, 1e-6)
	require.InDelta(t, 15.0, cost.OutputCost, 1e-6)
	require.False(t, math.IsNaN(cost.TotalCost))
	require.False(t, math.IsInf(cost.TotalCost, 0))
}

func TestCalculateCostWithServiceTier_PricingConfigFlexMultiplier(t *testing.T) {
	svc := newTestCalculator()
	configPricing := &routing.ModelPricingEntry{
		FlexMultiplier: testPtrFloat64(0.25),
		InputPrice:     testPtrFloat64(10e-6),
		OutputPrice:    testPtrFloat64(20e-6),
	}
	tokens := billingpricing.UsageTokens{InputTokens: 100, OutputTokens: 50}
	standard, err := svc.CalculateCostInternal("claude-sonnet-4", tokens, 1, "", configPricing)
	require.NoError(t, err)
	flex, err := svc.CalculateCostInternal("claude-sonnet-4", tokens, 1, "flex", configPricing)
	require.NoError(t, err)
	require.InDelta(t, standard.TotalCost*0.25, flex.TotalCost, 1e-12)
}

func TestCalculateCostWithServiceTier_OpenAIPriorityUsesPriorityPricing(t *testing.T) {
	svc := newTestCalculator()
	tokens := billingpricing.UsageTokens{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 20}

	baseCost, err := svc.CalculateCost("gpt-5.3-codex", tokens, 1.0)
	require.NoError(t, err)

	priorityCost, err := svc.CalculateCostWithServiceTier("gpt-5.3-codex", tokens, 1.0, "priority")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost*2, priorityCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*2, priorityCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*2, priorityCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*2, priorityCost.TotalCost, 1e-10)
}

func TestCalculateCostWithServiceTier_FlexAppliesHalfMultiplier(t *testing.T) {
	svc := newTestCalculator()
	tokens := billingpricing.UsageTokens{InputTokens: 100, OutputTokens: 50, CacheCreationTokens: 40, CacheReadTokens: 20}

	baseCost, err := svc.CalculateCost("gpt-5.4", tokens, 1.0)
	require.NoError(t, err)

	flexCost, err := svc.CalculateCostWithServiceTier("gpt-5.4", tokens, 1.0, "flex")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost*0.5, flexCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*0.5, flexCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheCreationCost*0.5, flexCost.CacheCreationCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*0.5, flexCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*0.5, flexCost.TotalCost, 1e-10)
}

func TestCalculateCostWithServiceTier_Gpt54MiniPriorityFallsBackToTierMultiplier(t *testing.T) {
	svc := newTestCalculator()
	tokens := billingpricing.UsageTokens{InputTokens: 120, OutputTokens: 30, CacheCreationTokens: 12, CacheReadTokens: 8}

	baseCost, err := svc.CalculateCost("gpt-5.4-mini", tokens, 1.0)
	require.NoError(t, err)

	priorityCost, err := svc.CalculateCostWithServiceTier("gpt-5.4-mini", tokens, 1.0, "priority")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost*2, priorityCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*2, priorityCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheCreationCost*2, priorityCost.CacheCreationCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*2, priorityCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*2, priorityCost.TotalCost, 1e-10)
}

func TestCalculateCostWithServiceTier_Gpt54NanoFlexAppliesHalfMultiplier(t *testing.T) {
	svc := newTestCalculator()
	tokens := billingpricing.UsageTokens{InputTokens: 100, OutputTokens: 50, CacheCreationTokens: 40, CacheReadTokens: 20}

	baseCost, err := svc.CalculateCost("gpt-5.4-nano", tokens, 1.0)
	require.NoError(t, err)

	flexCost, err := svc.CalculateCostWithServiceTier("gpt-5.4-nano", tokens, 1.0, "flex")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost*0.5, flexCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*0.5, flexCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheCreationCost*0.5, flexCost.CacheCreationCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*0.5, flexCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*0.5, flexCost.TotalCost, 1e-10)
}

func TestCalculateCostWithServiceTier_PriorityFallsBackToTierMultiplierWithoutExplicitPriorityPrice(t *testing.T) {
	svc := newTestCalculator()
	tokens := billingpricing.UsageTokens{InputTokens: 120, OutputTokens: 30, CacheCreationTokens: 12, CacheReadTokens: 8}

	baseCost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	priorityCost, err := svc.CalculateCostWithServiceTier("claude-sonnet-4", tokens, 1.0, "priority")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost*2, priorityCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*2, priorityCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheCreationCost*2, priorityCost.CacheCreationCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*2, priorityCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*2, priorityCost.TotalCost, 1e-10)
}

func TestCalculateCostWithServiceTier_ClaudeOpus48FastUsesDoublePricing(t *testing.T) {
	svc := newTestCalculator()
	tokens := billingpricing.UsageTokens{
		InputTokens:           100,
		OutputTokens:          50,
		CacheCreation5mTokens: 40,
		CacheCreation1hTokens: 20,
		CacheReadTokens:       10,
	}

	pricing, err := svc.GetModelPricing("claude-opus-4-8")
	require.NoError(t, err)
	require.InDelta(t, 5e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 25e-6, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, 6.25e-6, pricing.CacheCreation5mPrice, 1e-12)
	require.InDelta(t, 10e-6, pricing.CacheCreation1hPrice, 1e-12)
	require.InDelta(t, 0.5e-6, pricing.CacheReadPricePerToken, 1e-12)
	require.True(t, pricing.SupportsCacheBreakdown)
	require.True(t, pricing.SupportsServiceTier)

	baseCost, err := svc.CalculateCost("claude-opus-4-8", tokens, 1.0)
	require.NoError(t, err)

	fastCost, err := svc.CalculateCostWithServiceTier("claude-opus-4-8", tokens, 1.0, "priority")
	require.NoError(t, err)

	// The fixture's Claude Opus 4.8 Fast rates are twice its standard rates.
	require.InDelta(t, baseCost.InputCost*2, fastCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*2, fastCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheCreationCost*2, fastCost.CacheCreationCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*2, fastCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*2, fastCost.TotalCost, 1e-10)
}

func TestBillingServiceGetModelPricing_UsesDynamicPriorityFields(t *testing.T) {
	pricingSvc := newCatalogFixture(catalogFixture{
		pricingData: map[string]*billingpricing.CatalogModelPricing{
			"gpt-5.4": {
				InputCostPerToken:               2.5e-6,
				InputCostPerTokenPriority:       5e-6,
				OutputCostPerToken:              15e-6,
				OutputCostPerTokenPriority:      30e-6,
				CacheCreationInputTokenCost:     2.5e-6,
				CacheReadInputTokenCost:         0.25e-6,
				CacheReadInputTokenCostPriority: 0.5e-6,
				LongContextInputTokenThreshold:  272000,
				LongContextInputCostMultiplier:  2.0,
				LongContextOutputCostMultiplier: 1.5,
			},
		},
	})
	svc := newCalculator(pricingSvc)

	pricing, err := svc.GetModelPricing("gpt-5.4")
	require.NoError(t, err)
	require.InDelta(t, 2.5e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 5e-6, pricing.InputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 15e-6, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, 30e-6, pricing.OutputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 0.25e-6, pricing.CacheReadPricePerToken, 1e-12)
	require.InDelta(t, 0.5e-6, pricing.CacheReadPricePerTokenPriority, 1e-12)
	require.Equal(t, 272000, pricing.LongContextInputThreshold)
	require.InDelta(t, 2.0, pricing.LongContextInputMultiplier, 1e-12)
	require.InDelta(t, 1.5, pricing.LongContextOutputMultiplier, 1e-12)
}

func TestBillingServiceGetModelPricing_OpenAIFallbackGpt52Variants(t *testing.T) {
	svc := newTestCalculator()

	gpt52, err := svc.GetModelPricing("gpt-5.2")
	require.NoError(t, err)
	require.NotNil(t, gpt52)
	require.InDelta(t, 1.75e-6, gpt52.InputPricePerToken, 1e-12)
	require.InDelta(t, 3.5e-6, gpt52.InputPricePerTokenPriority, 1e-12)

	value, err := svc.GetModelPricing("gpt-5.2-codex")
	require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable)
	require.Nil(t, value)
}

func TestCalculateCostWithServiceTier_MissingPriorityDoesNotInventMultiplier(t *testing.T) {
	svc := newCalculator(newCatalogFixture(catalogFixture{
		pricingData: map[string]*billingpricing.CatalogModelPricing{
			"custom-no-priority": {
				InputCostPerToken:           1e-6,
				OutputCostPerToken:          2e-6,
				CacheCreationInputTokenCost: 0.5e-6,
				CacheReadInputTokenCost:     0.25e-6,
			},
		},
	}))
	tokens := billingpricing.UsageTokens{InputTokens: 100, OutputTokens: 50, CacheCreationTokens: 40, CacheReadTokens: 20}

	baseCost, err := svc.CalculateCost("custom-no-priority", tokens, 1.0)
	require.NoError(t, err)

	priorityCost, err := svc.CalculateCostWithServiceTier("custom-no-priority", tokens, 1.0, "priority")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost, priorityCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost, priorityCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheCreationCost, priorityCost.CacheCreationCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost, priorityCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost, priorityCost.TotalCost, 1e-10)
}

func TestGetModelPricing_OpenAIGpt52FallbacksExposePriorityPrices(t *testing.T) {
	svc := newTestCalculator()

	gpt52, err := svc.GetModelPricing("gpt-5.2")
	require.NoError(t, err)
	require.InDelta(t, 1.75e-6, gpt52.InputPricePerToken, 1e-12)
	require.InDelta(t, 3.5e-6, gpt52.InputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 14e-6, gpt52.OutputPricePerToken, 1e-12)
	require.InDelta(t, 28e-6, gpt52.OutputPricePerTokenPriority, 1e-12)

	value, err := svc.GetModelPricing("gpt-5.2-codex")
	require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable)
	require.Nil(t, value)
}

func TestGetModelPricing_MapsDynamicPriorityFieldsIntoBillingPricing(t *testing.T) {
	svc := newCalculator(newCatalogFixture(catalogFixture{
		pricingData: map[string]*billingpricing.CatalogModelPricing{
			"dynamic-tier-model": {
				InputCostPerToken:                   1e-6,
				InputCostPerTokenPriority:           2e-6,
				OutputCostPerToken:                  3e-6,
				OutputCostPerTokenPriority:          6e-6,
				CacheCreationInputTokenCost:         4e-6,
				CacheCreationInputTokenCostAbove1hr: 5e-6,
				CacheReadInputTokenCost:             7e-7,
				CacheReadInputTokenCostPriority:     8e-7,
				LongContextInputTokenThreshold:      999,
				LongContextInputCostMultiplier:      1.5,
				LongContextOutputCostMultiplier:     1.25,
			},
		},
	}))

	pricing, err := svc.GetModelPricing("dynamic-tier-model")
	require.NoError(t, err)
	require.InDelta(t, 1e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 2e-6, pricing.InputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 3e-6, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, 6e-6, pricing.OutputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 4e-6, pricing.CacheCreation5mPrice, 1e-12)
	require.InDelta(t, 5e-6, pricing.CacheCreation1hPrice, 1e-12)
	require.True(t, pricing.SupportsCacheBreakdown)
	require.InDelta(t, 7e-7, pricing.CacheReadPricePerToken, 1e-12)
	require.InDelta(t, 8e-7, pricing.CacheReadPricePerTokenPriority, 1e-12)
	require.Equal(t, 999, pricing.LongContextInputThreshold)
	require.InDelta(t, 1.5, pricing.LongContextInputMultiplier, 1e-12)
	require.InDelta(t, 1.25, pricing.LongContextOutputMultiplier, 1e-12)
}

func TestGetModelPricingWithConfig_NilConfigPricing_ReturnsOriginal(t *testing.T) {
	svc := newTestCalculator()

	pricing, err := svc.GetModelPricingWithConfig("claude-sonnet-4", nil)
	require.NoError(t, err)
	require.NotNil(t, pricing)

	// Should be identical to GetModelPricing
	original, err := svc.GetModelPricing("claude-sonnet-4")
	require.NoError(t, err)
	require.InDelta(t, original.InputPricePerToken, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, original.OutputPricePerToken, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, original.CacheCreationPricePerToken, pricing.CacheCreationPricePerToken, 1e-12)
	require.InDelta(t, original.CacheReadPricePerToken, pricing.CacheReadPricePerToken, 1e-12)
}

func TestGetModelPricingWithConfig_OverrideInputPriceOnly(t *testing.T) {
	svc := newTestCalculator()

	chPricing := &routing.ModelPricingEntry{
		InputPrice: testPtrFloat64(99e-6),
	}
	pricing, err := svc.GetModelPricingWithConfig("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	// The input override applies to standard rates; this catalog model has no priority price.
	require.InDelta(t, 99e-6, pricing.InputPricePerToken, 1e-12)
	require.Zero(t, pricing.InputPricePerTokenPriority)

	// The catalog output price remains 15e-6.
	require.InDelta(t, 15e-6, pricing.OutputPricePerToken, 1e-12)
}

func TestGetModelPricingWithConfig_PriceMultiplierAppliesAfterOverrides(t *testing.T) {
	svc := newTestCalculator()

	chPricing := &routing.ModelPricingEntry{
		PriceMultiplier: testPtrFloat64(2),
		InputPrice:      testPtrFloat64(10e-6),
	}
	pricing, err := svc.GetModelPricingWithConfig("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	// The multiplier applies after merging the manual input rate and inherited output rate.
	require.InDelta(t, 20e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 30e-6, pricing.OutputPricePerToken, 1e-12)
}

func TestGetModelPricingWithConfig_PreservesNativeTierRatio(t *testing.T) {
	svc := newTestCalculator()

	pricing, err := svc.GetModelPricingWithConfig("gpt-5.4", &routing.ModelPricingEntry{
		InputPrice:      testPtrFloat64(10e-6),
		OutputPrice:     testPtrFloat64(40e-6),
		CacheWritePrice: testPtrFloat64(8e-6),
		CacheReadPrice:  testPtrFloat64(1e-6),
	})
	require.NoError(t, err)

	// GPT-5.4 priority rates are twice standard rates; an absent cache-write priority price stays unset.
	require.InDelta(t, 20e-6, pricing.InputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 80e-6, pricing.OutputPricePerTokenPriority, 1e-12)
	require.Zero(t, pricing.CacheCreationPricePerTokenPriority)
	require.InDelta(t, 2e-6, pricing.CacheReadPricePerTokenPriority, 1e-12)
}

func TestCalculateCostWithPricingConfigFastModeMultiplierUsesFinalStandardPrice(t *testing.T) {
	svc := newTestCalculator()
	configPricing := &routing.ModelPricingEntry{
		PriceMultiplier:    testPtrFloat64(1.25),
		FastModeMultiplier: testPtrFloat64(1.5),
		InputPrice:         testPtrFloat64(10e-6),
		ImageInputPrice:    testPtrFloat64(12e-6),
		OutputPrice:        testPtrFloat64(20e-6),
		CacheWritePrice:    testPtrFloat64(4e-6),
		CacheReadPrice:     testPtrFloat64(2e-6),
		ImageOutputPrice:   testPtrFloat64(30e-6),
	}
	tokens := billingpricing.UsageTokens{
		InputTokens:         100,
		ImageInputTokens:    20,
		OutputTokens:        50,
		ImageOutputTokens:   10,
		CacheCreationTokens: 30,
		CacheReadTokens:     40,
	}

	standard, err := svc.CalculateCostInternal("gpt-5.4", tokens, 1, "", configPricing)
	require.NoError(t, err)
	fast, err := svc.CalculateCostInternal("gpt-5.4", tokens, 1, "priority", configPricing)
	require.NoError(t, err)

	// The configured multiplier overrides catalog priority rates for text, image, and cache charges.
	require.InDelta(t, standard.InputCost*1.5, fast.InputCost, 1e-12)
	require.InDelta(t, standard.ImageInputCost*1.5, fast.ImageInputCost, 1e-12)
	require.InDelta(t, standard.OutputCost*1.5, fast.OutputCost, 1e-12)
	require.InDelta(t, standard.ImageOutputCost*1.5, fast.ImageOutputCost, 1e-12)
	require.InDelta(t, standard.CacheCreationCost*1.5, fast.CacheCreationCost, 1e-12)
	require.InDelta(t, standard.CacheReadCost*1.5, fast.CacheReadCost, 1e-12)
	require.InDelta(t, standard.TotalCost*1.5, fast.TotalCost, 1e-12)
}

func TestGetModelPricingWithConfig_DoesNotMutateFallbackPricing(t *testing.T) {
	svc := newTestCalculator()

	chPricing := &routing.ModelPricingEntry{
		InputPrice: testPtrFloat64(99e-6),
	}
	_, err := svc.GetModelPricingWithConfig("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	pricing, err := svc.GetModelPricing("claude-sonnet-4")
	require.NoError(t, err)
	require.InDelta(t, 3e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 15e-6, pricing.OutputPricePerToken, 1e-12)
}

func TestGetModelPricingWithConfig_OverrideOutputPriceOnly(t *testing.T) {
	svc := newTestCalculator()

	chPricing := &routing.ModelPricingEntry{
		OutputPrice: testPtrFloat64(88e-6),
	}
	pricing, err := svc.GetModelPricingWithConfig("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	// The output override applies to standard rates; this catalog model has no priority price.
	require.InDelta(t, 88e-6, pricing.OutputPricePerToken, 1e-12)
	require.Zero(t, pricing.OutputPricePerTokenPriority)

	// The catalog input price remains 3e-6.
	require.InDelta(t, 3e-6, pricing.InputPricePerToken, 1e-12)
}

func TestGetModelPricingWithConfig_OverrideAllFields(t *testing.T) {
	svc := newTestCalculator()

	chPricing := &routing.ModelPricingEntry{
		InputPrice:       testPtrFloat64(10e-6),
		OutputPrice:      testPtrFloat64(20e-6),
		CacheWritePrice:  testPtrFloat64(5e-6),
		CacheReadPrice:   testPtrFloat64(1e-6),
		ImageOutputPrice: testPtrFloat64(50e-6),
	}
	pricing, err := svc.GetModelPricingWithConfig("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	require.InDelta(t, 10e-6, pricing.InputPricePerToken, 1e-12)
	require.Zero(t, pricing.InputPricePerTokenPriority)
	require.InDelta(t, 20e-6, pricing.OutputPricePerToken, 1e-12)
	require.Zero(t, pricing.OutputPricePerTokenPriority)
	require.InDelta(t, 5e-6, pricing.CacheCreationPricePerToken, 1e-12)
	require.InDelta(t, 5e-6, pricing.CacheCreation5mPrice, 1e-12)
	require.InDelta(t, 5e-6, pricing.CacheCreation1hPrice, 1e-12)
	require.InDelta(t, 1e-6, pricing.CacheReadPricePerToken, 1e-12)
	require.Zero(t, pricing.CacheReadPricePerTokenPriority)
	require.InDelta(t, 50e-6, pricing.ImageOutputPricePerToken, 1e-12)
}

func TestGetModelPricingWithConfig_CacheWritePriceAffects5mAnd1h(t *testing.T) {
	svc := newTestCalculator()

	chPricing := &routing.ModelPricingEntry{
		CacheWritePrice: testPtrFloat64(7e-6),
	}
	pricing, err := svc.GetModelPricingWithConfig("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	// CacheWritePrice should set all three: CacheCreationPricePerToken, 5m, and 1h
	require.InDelta(t, 7e-6, pricing.CacheCreationPricePerToken, 1e-12)
	require.InDelta(t, 7e-6, pricing.CacheCreation5mPrice, 1e-12)
	require.InDelta(t, 7e-6, pricing.CacheCreation1hPrice, 1e-12)
}

func TestGetModelPricingWithConfig_CacheWriteTTLPricesCanDiffer(t *testing.T) {
	svc := newTestCalculator()

	pricing, err := svc.GetModelPricingWithConfig("claude-fable-5-1", &routing.ModelPricingEntry{
		CacheWritePrice:   testPtrFloat64(13e-6),
		CacheWrite1hPrice: testPtrFloat64(21e-6),
	})
	require.NoError(t, err)
	require.True(t, pricing.SupportsCacheBreakdown)
	require.InDelta(t, 13e-6, pricing.CacheCreationPricePerToken, 1e-12)
	require.InDelta(t, 13e-6, pricing.CacheCreation5mPrice, 1e-12)
	require.InDelta(t, 21e-6, pricing.CacheCreation1hPrice, 1e-12)
}

func TestGetModelPricing_Fable51FallbackPricing(t *testing.T) {
	svc := newTestCalculator()

	pricing, err := svc.GetModelPricing("claude-fable-5-1")
	require.NoError(t, err)
	require.InDelta(t, 10e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 50e-6, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, 12.5e-6, pricing.CacheCreation5mPrice, 1e-12)
	require.InDelta(t, 20e-6, pricing.CacheCreation1hPrice, 1e-12)
	require.InDelta(t, 0.25e-6, pricing.CacheReadPricePerToken, 1e-12)
	require.NotNil(t, pricing.MaxReasoningEffortMultiplier)
	require.Equal(t, 3.0, *pricing.MaxReasoningEffortMultiplier)
}

func TestGetModelPricingWithConfig_CacheReadPriceAffectsPriority(t *testing.T) {
	svc := newTestCalculator()

	chPricing := &routing.ModelPricingEntry{
		CacheReadPrice: testPtrFloat64(2e-6),
	}
	pricing, err := svc.GetModelPricingWithConfig("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	// CacheReadPrice overrides the standard rate; this model has no configured priority rate.
	require.InDelta(t, 2e-6, pricing.CacheReadPricePerToken, 1e-12)
	require.Zero(t, pricing.CacheReadPricePerTokenPriority)
}

func TestGetModelPricingWithConfig_UnknownModelReturnsError(t *testing.T) {
	svc := newTestCalculator()

	chPricing := &routing.ModelPricingEntry{
		InputPrice: testPtrFloat64(1e-6),
	}
	pricing, err := svc.GetModelPricingWithConfig("totally-unknown-model", chPricing)
	require.Error(t, err)
	require.Nil(t, pricing)
	require.Contains(t, err.Error(), "pricing not found")
}

func TestGetModelPricingWithConfig_NilImageOutputPriceZerosAndMarksExplicit(t *testing.T) {
	svc := newTestCalculator()

	chPricing := &routing.ModelPricingEntry{
		InputPrice:  testPtrFloat64(10e-6),
		OutputPrice: testPtrFloat64(20e-6),
		// Leave image output pricing unset to test inheritance.
	}
	pricing, err := svc.GetModelPricingWithConfig("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	require.Equal(t, 0.0, pricing.ImageOutputPricePerToken)
	require.True(t, pricing.ImageOutputPriceExplicit)
}

func TestComputeTokenBreakdown_ExplicitZeroImagePrice_NoFallback(t *testing.T) {
	svc := newTestCalculator()

	pricing := &billingpricing.ModelPricing{
		InputPricePerToken:       3e-6,
		OutputPricePerToken:      15e-6,
		ImageOutputPricePerToken: 0,
		ImageOutputPriceExplicit: true,
	}
	tokens := billingpricing.UsageTokens{
		InputTokens:       100,
		OutputTokens:      200,
		ImageOutputTokens: 50,
	}
	bd := svc.ComputeTokenBreakdown(pricing, tokens, 1.0, "", false)

	// An explicit zero image-output rate prevents use of the text-output rate.
	require.Equal(t, 0.0, bd.ImageOutputCost)
	// Text output tokens = 200 - 50 = 150.
	require.InDelta(t, 150*15e-6, bd.OutputCost, 1e-12)
}

func TestComputeTokenBreakdown_NonExplicitZeroImagePrice_FallsBackToOutput(t *testing.T) {
	svc := newTestCalculator()

	pricing := &billingpricing.ModelPricing{
		InputPricePerToken:       3e-6,
		OutputPricePerToken:      15e-6,
		ImageOutputPricePerToken: 0,
		ImageOutputPriceExplicit: false,
	}
	tokens := billingpricing.UsageTokens{
		InputTokens:       100,
		OutputTokens:      200,
		ImageOutputTokens: 50,
	}
	bd := svc.ComputeTokenBreakdown(pricing, tokens, 1.0, "", false)

	// An unset image-output rate uses the text-output rate.
	require.InDelta(t, 50*15e-6, bd.ImageOutputCost, 1e-12)
	// Text output tokens = 200 - 50 = 150.
	require.InDelta(t, 150*15e-6, bd.OutputCost, 1e-12)
}

// TestCalculateCostUnified_LongContextContract checks whole-request pricing and the actual-charge marker.
func TestCalculateCostUnified_LongContextContract(t *testing.T) {
	svc := newTestCalculator()
	resolver := testkit.PriceResolver(nil, svc)
	cases := []struct {
		name                            string
		input, cacheRead, cacheWrite    int
		threshold                       int
		inclusive, disabled, unitPrices bool
		rate, wantTotal, wantActual     float64
		applied                         bool
	}{
		{name: "低于阈值", input: 99, cacheRead: 100, threshold: 200, rate: 1, wantTotal: 111, wantActual: 111},
		{name: "等于排他阈值", input: 100, cacheRead: 100, threshold: 200, rate: 1, wantTotal: 112, wantActual: 112},
		{name: "超过阈值整段计价", input: 101, cacheRead: 100, threshold: 200, rate: 2, wantTotal: 225, wantActual: 450, applied: true},
		{name: "缓存读取超过阈值", input: 10, cacheRead: 210, threshold: 200, rate: 1, wantTotal: 65, wantActual: 65, applied: true},
		{name: "缓存创建参与阈值和加价", input: 60, cacheRead: 30, cacheWrite: 120, threshold: 200, rate: 1, wantTotal: 249, wantActual: 249, applied: true},
		{name: "零倍率不标记加价", input: 101, cacheRead: 100, threshold: 200, rate: 0, wantTotal: 225, wantActual: 0},
		{name: "关闭长上下文", input: 101, cacheRead: 100, threshold: 200, disabled: true, rate: 1, wantTotal: 113, wantActual: 113},
		{name: "零阈值", input: 101, cacheRead: 100, rate: 1, wantTotal: 113, wantActual: 113},
		{name: "倍率均为一", input: 101, cacheRead: 100, threshold: 200, unitPrices: true, rate: 1, wantTotal: 113, wantActual: 113},
		{name: "等于包含阈值", input: 100, cacheRead: 100, threshold: 200, inclusive: true, rate: 1, wantTotal: 223, wantActual: 223, applied: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prices := &billingpricing.ModelPricing{
				InputPricePerToken: 1, OutputPricePerToken: 2,
				CacheReadPricePerToken: 0.1, CacheCreationPricePerToken: 0.5,
				LongContextInputThreshold: tc.threshold, LongContextThresholdInclusive: tc.inclusive,
				LongContextInputMultiplier: 2, LongContextOutputMultiplier: 1.5,
			}
			if tc.unitPrices {
				prices.LongContextInputMultiplier = 1
				prices.LongContextOutputMultiplier = 1
			}
			cost, err := svc.CalculateCostUnified(billing.CostInput{
				Ctx: t.Context(), Model: "contract-long-context", RateMultiplier: tc.rate,
				Tokens:   billingpricing.UsageTokens{InputTokens: tc.input, OutputTokens: 1, CacheReadTokens: tc.cacheRead, CacheCreationTokens: tc.cacheWrite},
				Resolver: resolver,
				Resolved: &billingpricing.ResolvedPricing{Mode: billingpricing.BillingModeToken, BasePricing: prices, Source: billingpricing.PricingSourceConfig, LongContextPricingEnabled: !tc.disabled},
			})
			require.NoError(t, err)
			require.InDelta(t, tc.wantTotal, cost.TotalCost, 1e-10)
			require.InDelta(t, tc.wantActual, cost.ActualCost, 1e-10)
			require.Equal(t, tc.applied, cost.LongContextBillingApplied)
		})
	}
}

func TestCalculateCostUnified_NilResolver_FallsBackToOldPath(t *testing.T) {
	svc := newTestCalculator()

	tokens := billingpricing.UsageTokens{InputTokens: 1000, OutputTokens: 500}
	input := billing.CostInput{
		Model:          "claude-sonnet-4",
		Tokens:         tokens,
		RateMultiplier: 1.0,
		Resolver:       nil, // no resolver
	}
	cost, err := svc.CalculateCostUnified(input)
	require.NoError(t, err)

	// With no resolver, costs match the direct token calculation.
	expected, err := svc.CalculateCostInternal("claude-sonnet-4", tokens, 1.0, "", nil)
	require.NoError(t, err)
	require.InDelta(t, expected.TotalCost, cost.TotalCost, 1e-10)
	require.InDelta(t, expected.ActualCost, cost.ActualCost, 1e-10)
	// The no-resolver path leaves BillingMode empty.
	require.Empty(t, cost.BillingMode)
}

func TestCalculateCostUnified_TokenMode(t *testing.T) {
	bs := newTestCalculator()
	resolver := testkit.PriceResolver(nil, bs)

	tokens := billingpricing.UsageTokens{InputTokens: 1000, OutputTokens: 500}
	input := billing.CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         tokens,
		RateMultiplier: 1.5,
		Resolver:       resolver,
	}
	cost, err := bs.CalculateCostUnified(input)
	require.NoError(t, err)
	require.NotNil(t, cost)

	// Verify token billing: Input: 1000*3e-6=0.003, Output: 500*15e-6=0.0075
	expectedTotal := 1000*3e-6 + 500*15e-6
	require.InDelta(t, expectedTotal, cost.TotalCost, 1e-10)
	require.InDelta(t, expectedTotal*1.5, cost.ActualCost, 1e-10)
	require.Equal(t, string(routing.BillingModeToken), cost.BillingMode)
}

func TestCalculateCostUnified_Fable51MaxReasoningMultiplier(t *testing.T) {
	bs := newTestCalculator()
	resolver := testkit.PriceResolver(nil, bs)
	tokens := billingpricing.UsageTokens{InputTokens: 1000, OutputTokens: 100}

	standard, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx: context.Background(), Model: "claude-fable-5-1", Tokens: tokens,
		RateMultiplier: 1, ReasoningEffort: "xhigh", Resolver: resolver,
	})
	require.NoError(t, err)
	max, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx: context.Background(), Model: "claude-fable-5-1", Tokens: tokens,
		RateMultiplier: 1, ReasoningEffort: "max", Resolver: resolver,
	})
	require.NoError(t, err)
	require.InDelta(t, standard.TotalCost*3, max.TotalCost, 1e-12)
	require.InDelta(t, standard.ActualCost*3, max.ActualCost, 1e-12)
}

func TestCalculateCostUnified_PricingConfigOverridesFable51MaxReasoningMultiplier(t *testing.T) {
	configured := 1.5
	groupID := int64(7)
	cs := newTestPricingConfigServiceWithCache(t, &routingtestkit.ModelConfigData{
		Prices: map[routingtestkit.ModelKey]*routing.ModelPricingEntry{
			{GroupID: groupID, Platform: capability.PlatformAnthropic, Model: "claude-fable-5-1"}: {
				BillingMode: routing.BillingModeToken,
				InputPrice:  testPtrFloat64(10e-6), OutputPrice: testPtrFloat64(50e-6),
				MaxReasoningEffortMultiplier: &configured,
			},
		},
		ByGroup:       map[int64]*routingtestkit.Configuration{groupID: {ID: 1, Status: billing.StatusActive}},
		Platforms:     map[int64]string{groupID: capability.PlatformAnthropic},
		PricePatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.PricePattern{},
		Models:        map[routingtestkit.ModelKey]string{},
		ModelPatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.ModelPattern{},
		ByID:          map[int64]*routingtestkit.Configuration{},
	})
	bs := newTestCalculator()
	resolver := testkit.PriceResolver(cs, bs)
	cost, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx: context.Background(), Model: "claude-fable-5-1", GroupID: &groupID,
		Tokens: billingpricing.UsageTokens{InputTokens: 1000}, RateMultiplier: 1, ReasoningEffort: "max", Resolver: resolver,
	})
	require.NoError(t, err)
	require.InDelta(t, 1000*10e-6*configured, cost.TotalCost, 1e-12)
}

func TestCalculateCostUnified_AppliesPricingConfigPriceMultiplierBeforeRateMultiplier(t *testing.T) {
	cs := newTestPricingConfigServiceWithCache(t, &routingtestkit.ModelConfigData{
		Prices: map[routingtestkit.ModelKey]*routing.ModelPricingEntry{
			{GroupID: 2, Model: "claude-sonnet-4"}: {
				BillingMode:     routing.BillingModeToken,
				PriceMultiplier: testPtrFloat64(2),
				InputPrice:      testPtrFloat64(5e-6),
			},
		},
		ByGroup: map[int64]*routingtestkit.Configuration{
			2: {ID: 2, Status: billing.StatusActive},
		},
		Platforms:     map[int64]string{2: ""},
		PricePatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.PricePattern{},
		Models:        map[routingtestkit.ModelKey]string{},
		ModelPatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.ModelPattern{},
		ByID:          map[int64]*routingtestkit.Configuration{},
	})

	bs := newTestCalculator()
	resolver := testkit.PriceResolver(cs, bs)
	groupID := int64(2)
	cost, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		GroupID:        &groupID,
		Tokens:         billingpricing.UsageTokens{InputTokens: 100, OutputTokens: 10},
		RateMultiplier: 3,
		Resolver:       resolver,
	})
	require.NoError(t, err)

	// Input 5e-6 and inherited output 15e-6 receive the configured 2x multiplier, then the group 3x multiplier.
	expectedTotal := 100*10e-6 + 10*30e-6
	require.InDelta(t, expectedTotal, cost.TotalCost, 1e-12)
	require.InDelta(t, expectedTotal*3, cost.ActualCost, 1e-12)
}

func TestCalculateCostUnified_TokenModeAppliesRateMultiplierToImageTokens(t *testing.T) {
	bs := newTestCalculator()
	resolver := testkit.PriceResolver(nil, bs)

	tokens := billingpricing.UsageTokens{InputTokens: 1000, OutputTokens: 600, ImageOutputTokens: 100}
	cost, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         tokens,
		RateMultiplier: 3.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)

	textInput := 1000 * 3e-6
	textOutput := 500 * 15e-6
	imageOutput := 100 * 15e-6
	require.InDelta(t, textInput+textOutput+imageOutput, cost.TotalCost, 1e-10)
	require.InDelta(t, (textInput+textOutput+imageOutput)*3.0, cost.ActualCost, 1e-10)
	require.InDelta(t, imageOutput, cost.ImageOutputCost, 1e-10)
}

func TestCalculateCostUnified_PerRequestMode(t *testing.T) {
	cs := newTestPricingConfigServiceWithCache(t, &routingtestkit.ModelConfigData{
		Prices: map[routingtestkit.ModelKey]*routing.ModelPricingEntry{
			{GroupID: 1, Model: "claude-sonnet-4"}: {
				BillingMode:     routing.BillingModePerRequest,
				PerRequestPrice: testPtrFloat64(0.05),
			},
		},
		ByGroup: map[int64]*routingtestkit.Configuration{
			1: {ID: 1, Status: billing.StatusActive},
		},
		Platforms:     map[int64]string{1: ""},
		PricePatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.PricePattern{},
		Models:        map[routingtestkit.ModelKey]string{},
		ModelPatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.ModelPattern{},
		ByID:          map[int64]*routingtestkit.Configuration{},
	})

	bs := newTestCalculator()
	resolver := testkit.PriceResolver(cs, bs)
	groupID := int64(1)

	input := billing.CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		GroupID:        &groupID,
		Tokens:         billingpricing.UsageTokens{InputTokens: 100, OutputTokens: 50},
		RequestCount:   3,
		RateMultiplier: 2.0,
		Resolver:       resolver,
	}
	cost, err := bs.CalculateCostUnified(input)
	require.NoError(t, err)
	require.NotNil(t, cost)

	// 3 requests * $0.05 = $0.15
	require.InDelta(t, 0.15, cost.TotalCost, 1e-10)
	// ActualCost = 0.15 * 2.0 = 0.30
	require.InDelta(t, 0.30, cost.ActualCost, 1e-10)
	require.Equal(t, string(routing.BillingModePerRequest), cost.BillingMode)
}

func TestCalculateCostUnified_PerRequestTierExplicitZeroDoesNotFallbackToDefault(t *testing.T) {
	zero := 0.0
	defaultPrice := 0.10
	cs := newTestPricingConfigServiceWithCache(t, &routingtestkit.ModelConfigData{
		Prices: map[routingtestkit.ModelKey]*routing.ModelPricingEntry{
			{GroupID: 3, Model: "qoder-image"}: {
				BillingMode:     routing.BillingModeImage,
				PerRequestPrice: &defaultPrice,
				Intervals: []routing.PricingInterval{
					{TierLabel: "1K", PerRequestPrice: &zero},
				},
			},
		},
		ByGroup: map[int64]*routingtestkit.Configuration{
			3: {ID: 3, Status: billing.StatusActive},
		},
		Platforms:     map[int64]string{3: ""},
		PricePatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.PricePattern{},
		Models:        map[routingtestkit.ModelKey]string{},
		ModelPatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.ModelPattern{},
		ByID:          map[int64]*routingtestkit.Configuration{},
	})

	bs := newTestCalculator()
	resolver := testkit.PriceResolver(cs, bs)
	groupID := int64(3)

	cost, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx:            context.Background(),
		Model:          "qoder-image",
		GroupID:        &groupID,
		SizeTier:       "1K",
		RequestCount:   2,
		RateMultiplier: 1.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)
	require.NotNil(t, cost)
	require.InDelta(t, 0.0, cost.TotalCost, 1e-10)
	require.InDelta(t, 0.0, cost.ActualCost, 1e-10)
	require.Equal(t, string(routing.BillingModeImage), cost.BillingMode)
}

func TestCalculateCostUnified_PerRequestContextTierExplicitZeroDoesNotFallbackToDefault(t *testing.T) {
	zero := 0.0
	defaultPrice := 0.10
	maxTokens := 1000
	cs := newTestPricingConfigServiceWithCache(t, &routingtestkit.ModelConfigData{
		Prices: map[routingtestkit.ModelKey]*routing.ModelPricingEntry{
			{GroupID: 4, Model: "qoder-request"}: {
				BillingMode:     routing.BillingModePerRequest,
				PerRequestPrice: &defaultPrice,
				Intervals: []routing.PricingInterval{
					{MinTokens: 0, MaxTokens: &maxTokens, PerRequestPrice: &zero},
				},
			},
		},
		ByGroup: map[int64]*routingtestkit.Configuration{
			4: {ID: 4, Status: billing.StatusActive},
		},
		Platforms:     map[int64]string{4: ""},
		PricePatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.PricePattern{},
		Models:        map[routingtestkit.ModelKey]string{},
		ModelPatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.ModelPattern{},
		ByID:          map[int64]*routingtestkit.Configuration{},
	})

	bs := newTestCalculator()
	resolver := testkit.PriceResolver(cs, bs)
	groupID := int64(4)

	cost, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx:            context.Background(),
		Model:          "qoder-request",
		GroupID:        &groupID,
		Tokens:         billingpricing.UsageTokens{InputTokens: 500},
		RequestCount:   3,
		RateMultiplier: 1.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)
	require.NotNil(t, cost)
	require.InDelta(t, 0.0, cost.TotalCost, 1e-10)
	require.InDelta(t, 0.0, cost.ActualCost, 1e-10)
	require.Equal(t, string(routing.BillingModePerRequest), cost.BillingMode)
}

func TestCalculateCostUnified_ImageMode(t *testing.T) {
	cs := newTestPricingConfigServiceWithCache(t, &routingtestkit.ModelConfigData{
		Prices: map[routingtestkit.ModelKey]*routing.ModelPricingEntry{
			{GroupID: 2, Model: "gemini-image"}: {
				BillingMode:     routing.BillingModeImage,
				PerRequestPrice: testPtrFloat64(0.10),
			},
		},
		ByGroup: map[int64]*routingtestkit.Configuration{
			2: {ID: 2, Status: billing.StatusActive},
		},
		Platforms:     map[int64]string{2: ""},
		PricePatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.PricePattern{},
		Models:        map[routingtestkit.ModelKey]string{},
		ModelPatterns: map[routingtestkit.GroupPlatform][]*routingtestkit.ModelPattern{},
		ByID:          map[int64]*routingtestkit.Configuration{},
	})

	bs := newCalculatorWithPrices(nil, map[string]*billingpricing.ModelPricing{})
	resolver := testkit.PriceResolver(cs, bs)
	groupID := int64(2)

	input := billing.CostInput{
		Ctx:            context.Background(),
		Model:          "gemini-image",
		GroupID:        &groupID,
		Tokens:         billingpricing.UsageTokens{},
		RequestCount:   2,
		RateMultiplier: 1.0,
		Resolver:       resolver,
	}
	cost, err := bs.CalculateCostUnified(input)
	require.NoError(t, err)
	require.NotNil(t, cost)

	// 2 * $0.10 = $0.20
	require.InDelta(t, 0.20, cost.TotalCost, 1e-10)
	require.InDelta(t, 0.20, cost.ActualCost, 1e-10)
	require.Equal(t, string(routing.BillingModeImage), cost.BillingMode)
}

// TestCalculateCostUnified_RateMultiplierZeroProducesZero checks zero-multiplier charges.
// The calculation accepts zero even though saved group multipliers must be positive.
func TestCalculateCostUnified_RateMultiplierZeroProducesZero(t *testing.T) {
	bs := newTestCalculator()
	resolver := testkit.PriceResolver(nil, bs)

	tokens := billingpricing.UsageTokens{InputTokens: 1000, OutputTokens: 500}

	cost, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         tokens,
		RateMultiplier: 0,
		Resolver:       resolver,
	})
	require.NoError(t, err)
	require.Greater(t, cost.TotalCost, 0.0)
	require.InDelta(t, 0.0, cost.ActualCost, 1e-10)
}

// TestCalculateCostUnified_NegativeRateMultiplierClampedToZero checks that unified billing clamps negative
// multipliers to zero.
func TestCalculateCostUnified_NegativeRateMultiplierClampedToZero(t *testing.T) {
	bs := newTestCalculator()
	resolver := testkit.PriceResolver(nil, bs)

	tokens := billingpricing.UsageTokens{InputTokens: 1000}

	cost, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         tokens,
		RateMultiplier: -5.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)
	require.Greater(t, cost.TotalCost, 0.0)
	require.InDelta(t, 0.0, cost.ActualCost, 1e-10)
}

func TestCalculateCostUnified_BillingModeFieldFilled(t *testing.T) {
	bs := newTestCalculator()
	resolver := testkit.PriceResolver(nil, bs)

	cost, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         billingpricing.UsageTokens{InputTokens: 100},
		RateMultiplier: 1.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)
	require.Equal(t, "token", cost.BillingMode)
}

func TestCalculateCostUnified_UsesPreResolvedPricing(t *testing.T) {
	bs := newTestCalculator()
	resolver := testkit.PriceResolver(nil, bs)

	// Pre-resolve with per_request mode to verify it's used instead of re-resolving
	preResolved := &billingpricing.ResolvedPricing{
		Mode:                   routing.BillingModePerRequest,
		DefaultPerRequestPrice: 0.07,
	}

	cost, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         billingpricing.UsageTokens{InputTokens: 100},
		RequestCount:   2,
		RateMultiplier: 1.0,
		Resolver:       resolver,
		Resolved:       preResolved,
	})
	require.NoError(t, err)
	require.NotNil(t, cost)

	// 2 * $0.07 = $0.14
	require.InDelta(t, 0.14, cost.TotalCost, 1e-10)
	require.Equal(t, string(routing.BillingModePerRequest), cost.BillingMode)
}

// TestEmptyCatalogNeverRecreatesHistoricalPrices checks all historical fixture models remain unpriced without a
// catalog.
func TestEmptyCatalogNeverRecreatesHistoricalPrices(t *testing.T) {
	calculator := billing.NewCalculator(nil, billing.CalculatorOptions{})
	for model := range testkit.HistoricalPrices() {
		price, err := calculator.GetModelPricing(model)
		require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable, model)
		require.Nil(t, price, model)
		require.Equal(t, "unpriced", calculator.DefaultModelPrice(model, "", "token").PriceStatus, model)
	}
}

func TestCalculateSearchCost(t *testing.T) {
	t.Parallel()
	s := testkit.Calculator(nil, map[string]*billingpricing.ModelPricing{})
	require.Equal(t, 0.0, s.CalculateSearchCost(0, floatPtr(10), 1).ActualCost)
	// Missing configured and catalog rates produce zero cost.
	require.Zero(t, s.CalculateSearchCost(5, nil, 1).ActualCost)
	// An explicit zero price means free usage.
	require.Equal(t, 0.0, s.CalculateSearchCost(5, floatPtr(0), 1).ActualCost)
	price := 10.0
	cost := s.CalculateSearchCost(100, &price, 1.5)
	// The charge is 10 / 1000 * 100 * 1.5 = 1.5.
	require.InDelta(t, 1.0, cost.TotalCost, 1e-9)
	require.InDelta(t, 1.5, cost.ActualCost, 1e-9)
}

func TestCalculateAudioCost(t *testing.T) {
	t.Parallel()
	s := testkit.Calculator(nil, map[string]*billingpricing.ModelPricing{})
	rt, tts, stt := 0.10, 15.0, 0.50
	cfg := &billingpricing.AudioPriceConfig{RealtimePerMin: &rt, TTSPerMChars: &tts, STTPerHour: &stt}
	require.InDelta(t, 0.20, s.CalculateAudioCost("realtime", 2, cfg, 1).ActualCost, 1e-9)
	require.InDelta(t, 1.5, s.CalculateAudioCost("tts", 0.1, cfg, 1).ActualCost, 1e-9)
	require.InDelta(t, 0.25, s.CalculateAudioCost("stt", 0.5, cfg, 1).ActualCost, 1e-9)
	require.Equal(t, 0.0, s.CalculateAudioCost("unknown", 1, cfg, 1).ActualCost)
	// Without operation prices, each mode records zero cost.
	require.Zero(t, s.CalculateAudioCost("realtime", 1, nil, 1).ActualCost)
	require.Zero(t, s.CalculateAudioCost("tts", 1, nil, 1).ActualCost)
	require.Zero(t, s.CalculateAudioCost("stt", 1, nil, 1).ActualCost)
	// An explicit zero price means free usage.
	zero := 0.0
	require.Equal(t, 0.0, s.CalculateAudioCost("realtime", 1, &billingpricing.AudioPriceConfig{RealtimePerMin: &zero}, 1).ActualCost)
}

// TestCalculateImageCost_DefaultPricing checks per-image catalog rates without group overrides.
func TestCalculateImageCost_DefaultPricing(t *testing.T) {
	svc := testkit.Calculator(newCatalogFixture(catalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{"gemini-3-pro-image": {CatalogRules: billingpricing.CatalogRules{ImagePrices: map[string]float64{"1K": 0.134, "2K": 0.201, "4K": 0.268}}, OutputCostPerImage: 0.134, ImagePricePresent: true, Mode: "image_generation", TokenPricingAbsent: true}}}), map[string]*billingpricing.ModelPricing{}) // Use the full model name's catalog entry.

	// The fixture's 2K rate is $0.201 per image.
	cost, mediaErr := svc.CalculateImageCost("gemini-3-pro-image", "2K", 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	require.InDelta(t, 0.201, cost.TotalCost, 0.0001)
	require.InDelta(t, 0.201, cost.ActualCost, 0.0001)

	cost, mediaErr = svc.CalculateImageCost("gemini-3-pro-image", "2K", 3, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	require.InDelta(t, 0.603, cost.TotalCost, 0.0001)
}

func TestCalculateImageCost_NormalizesInvalidSizeTo2K(t *testing.T) {
	svc := testkit.Calculator(newCatalogFixture(catalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{"gemini-3-pro-image": {CatalogRules: billingpricing.CatalogRules{ImagePrices: map[string]float64{"1K": 0.134, "2K": 0.201, "4K": 0.268}}, OutputCostPerImage: 0.134, ImagePricePresent: true, Mode: "image_generation", TokenPricingAbsent: true}}}), map[string]*billingpricing.ModelPricing{})

	for _, imageSize := range []string{"", "auto", "not-a-size"} {
		t.Run(imageSize, func(t *testing.T) {
			cost, mediaErr := svc.CalculateImageCost("gemini-3-pro-image", imageSize, 2, 1.0)
			if mediaErr != nil {
				t.Fatal(mediaErr)
			}
			require.InDelta(t, 0.402, cost.TotalCost, 0.0001)
			require.InDelta(t, 0.402, cost.ActualCost, 0.0001)
		})
	}
}

// TestCalculateImageCost_Explicit4KPrice checks the catalog's 4K per-image rate.
func TestCalculateImageCost_Explicit4KPrice(t *testing.T) {
	svc := testkit.Calculator(newCatalogFixture(catalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{"gemini-3-pro-image": {CatalogRules: billingpricing.CatalogRules{ImagePrices: map[string]float64{"1K": 0.134, "2K": 0.201, "4K": 0.268}}, OutputCostPerImage: 0.134, ImagePricePresent: true, Mode: "image_generation", TokenPricingAbsent: true}}}), map[string]*billingpricing.ModelPricing{})

	// The fixture's 4K rate is $0.268 per image.
	cost, mediaErr := svc.CalculateImageCost("gemini-3-pro-image", "4K", 1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	require.InDelta(t, 0.268, cost.TotalCost, 0.0001)
}

// TestCalculateImageCost_RateMultiplier checks that image charges apply the group multiplier.
func TestCalculateImageCost_RateMultiplier(t *testing.T) {
	svc := testkit.Calculator(newCatalogFixture(catalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{"gemini-3-pro-image": {CatalogRules: billingpricing.CatalogRules{ImagePrices: map[string]float64{"1K": 0.134, "2K": 0.201, "4K": 0.268}}, OutputCostPerImage: 0.134, ImagePricePresent: true, Mode: "image_generation", TokenPricingAbsent: true}}}), map[string]*billingpricing.ModelPricing{})

	cost, mediaErr := svc.CalculateImageCost("gemini-3-pro-image", "2K", 1, 1.5)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	require.InDelta(t, 0.201, cost.TotalCost, 0.0001)   // TotalCost uses the 2K unit price before the group multiplier.
	require.InDelta(t, 0.3015, cost.ActualCost, 0.0001) // ActualCost = 0.201 * 1.5

	cost, mediaErr = svc.CalculateImageCost("gemini-3-pro-image", "2K", 2, 2.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	require.InDelta(t, 0.402, cost.TotalCost, 0.0001)
	require.InDelta(t, 0.804, cost.ActualCost, 0.0001)
}

// TestCalculateImageCost_ZeroCount checks that no images produce zero cost.
func TestCalculateImageCost_ZeroCount(t *testing.T) {
	svc := testkit.Calculator(newCatalogFixture(catalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{"gemini-3-pro-image": {CatalogRules: billingpricing.CatalogRules{ImagePrices: map[string]float64{"1K": 0.134, "2K": 0.201, "4K": 0.268}}, OutputCostPerImage: 0.134, ImagePricePresent: true, Mode: "image_generation", TokenPricingAbsent: true}}}), map[string]*billingpricing.ModelPricing{})

	cost, mediaErr := svc.CalculateImageCost("gemini-3-pro-image", "2K", 0, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	require.Equal(t, 0.0, cost.TotalCost)
	require.Equal(t, 0.0, cost.ActualCost)
}

// TestCalculateImageCost_NegativeCount checks that negative counts produce zero cost.
func TestCalculateImageCost_NegativeCount(t *testing.T) {
	svc := testkit.Calculator(newCatalogFixture(catalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{"gemini-3-pro-image": {CatalogRules: billingpricing.CatalogRules{ImagePrices: map[string]float64{"1K": 0.134, "2K": 0.201, "4K": 0.268}}, OutputCostPerImage: 0.134, ImagePricePresent: true, Mode: "image_generation", TokenPricingAbsent: true}}}), map[string]*billingpricing.ModelPricing{})

	cost, mediaErr := svc.CalculateImageCost("gemini-3-pro-image", "2K", -1, 1.0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	require.Equal(t, 0.0, cost.TotalCost)
	require.Equal(t, 0.0, cost.ActualCost)
}

// TestCalculateImageCost_ZeroRateMultiplier checks that a zero multiplier produces zero image charges.
func TestCalculateImageCost_ZeroRateMultiplier(t *testing.T) {
	svc := testkit.Calculator(newCatalogFixture(catalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{"gemini-3-pro-image": {CatalogRules: billingpricing.CatalogRules{ImagePrices: map[string]float64{"1K": 0.134, "2K": 0.201, "4K": 0.268}}, OutputCostPerImage: 0.134, ImagePricePresent: true, Mode: "image_generation", TokenPricingAbsent: true}}}), map[string]*billingpricing.ModelPricing{})

	cost, mediaErr := svc.CalculateImageCost("gemini-3-pro-image", "2K", 1, 0)
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	require.InDelta(t, 0.201, cost.TotalCost, 0.0001)
	require.InDelta(t, 0.0, cost.ActualCost, 1e-10)
}

// TestGetDefaultImagePrice_UnknownModel checks that absent catalog prices return the missing-price error.
func TestGetDefaultImagePrice_UnknownModel(t *testing.T) {
	svc := testkit.Calculator(nil, nil)
	for _, size := range []string{"1K", "2K", "4K"} {
		cost, err := svc.CalculateImageCost("gemini-3-pro-image", size, 1, 1)
		require.ErrorIs(t, err, billingpricing.ErrModelPricingUnavailable)
		require.Nil(t, cost)
	}
}

func TestCalculateCostUnifiedAppliesPricingTimeMultiplierToTokenBuckets(t *testing.T) {
	groupID := int64(71)
	pricing := &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  floatPtr(5e-6),
		TimePricing: &routing.TimePricingConfig{
			Timezone: "Asia/Shanghai",
			Periods:  []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}},
		},
	}
	resolved := &billingpricing.ResolvedPricing{
		Mode:          routing.BillingModeToken,
		Source:        billingpricing.PricingSourceConfig,
		BasePricing:   &billingpricing.ModelPricing{InputPricePerToken: 5e-6, OutputPricePerToken: 15e-6},
		ConfigPricing: pricing,
	}
	service := testkit.Calculator(nil, nil)
	resolver := billing.NewPriceResolver(nil, service, modelidentity.Identity, nil)

	cost, err := service.CalculateCostUnified(billing.CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		GroupID:        &groupID,
		Tokens:         billingpricing.UsageTokens{InputTokens: 100, OutputTokens: 10},
		RateMultiplier: 3,
		PricingAt:      time.Date(2026, 6, 29, 1, 0, 0, 0, time.UTC),
		Resolver:       resolver,
		Resolved:       resolved,
	})
	require.NoError(t, err)
	want := (100*5e-6 + 10*15e-6) * 2
	require.InDelta(t, want, cost.TotalCost, 1e-12)
	require.InDelta(t, want*3, cost.ActualCost, 1e-12)
}

func TestCalculateCostUnifiedDoesNotApplyConfigTimeMultiplierToPerRequest(t *testing.T) {
	groupID := int64(72)
	pricing := &routing.ModelPricingEntry{
		BillingMode:     routing.BillingModePerRequest,
		PerRequestPrice: floatPtr(0.05),
		TimePricing: &routing.TimePricingConfig{
			Timezone: "Asia/Shanghai",
			Periods:  []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}},
		},
	}
	resolved := &billingpricing.ResolvedPricing{Mode: routing.BillingModePerRequest, Source: billingpricing.PricingSourceConfig, ConfigPricing: pricing, DefaultPerRequestPrice: 0.05}
	service := testkit.Calculator(nil, nil)
	resolver := billing.NewPriceResolver(nil, service, modelidentity.Identity, nil)

	cost, err := service.CalculateCostUnified(billing.CostInput{
		Ctx:            context.Background(),
		Model:          "image-model",
		GroupID:        &groupID,
		RequestCount:   3,
		RateMultiplier: 2,
		PricingAt:      time.Date(2026, 6, 29, 1, 0, 0, 0, time.UTC),
		Resolver:       resolver,
		Resolved:       resolved,
	})
	require.NoError(t, err)
	require.InDelta(t, 0.15, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.30, cost.ActualCost, 1e-12)
}

func TestDisplayPricingCarriesConfigTimePricingAndMaxReasoningMultiplier(t *testing.T) {
	maxMultiplier := 1.5
	pricing := &routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  floatPtr(5e-6),
		TimePricing: &routing.TimePricingConfig{
			Timezone:     "Asia/Shanghai",
			WeekdaysOnly: true,
			Periods:      []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}},
		},
	}
	resolved := &billingpricing.ResolvedPricing{
		Mode:   routing.BillingModeToken,
		Source: billingpricing.PricingSourceConfig,
		BasePricing: &billingpricing.ModelPricing{
			InputPricePerToken:           5e-6,
			OutputPricePerToken:          15e-6,
			MaxReasoningEffortMultiplier: &maxMultiplier,
		},
		ConfigPricing: pricing,
	}
	service := testkit.Calculator(nil, nil)

	display := service.DisplayPricingWithResolvedMultipliers("claude-sonnet-4", 2, resolved)

	// Display base rates at 1x; expose time schedules and Max multipliers separately for the UI.
	require.InDelta(t, 10e-6, display.InputPricePerToken, 1e-15)
	require.NotNil(t, display.MaxReasoningEffortMultiplier)
	require.Equal(t, 1.5, *display.MaxReasoningEffortMultiplier)
	require.Equal(t, pricing.TimePricing, display.TimePricing)
	require.NotSame(t, pricing.TimePricing, display.TimePricing)
}

func TestDisplayPricingOmitsNeutralModifiers(t *testing.T) {
	neutral := 1.0
	resolved := &billingpricing.ResolvedPricing{
		Mode:   routing.BillingModeToken,
		Source: billingpricing.PricingSourceConfig,
		BasePricing: &billingpricing.ModelPricing{
			InputPricePerToken:           5e-6,
			MaxReasoningEffortMultiplier: &neutral,
		},
		ConfigPricing: &routing.ModelPricingEntry{
			BillingMode: routing.BillingModeToken,
			InputPrice:  floatPtr(5e-6),
			// Overlapping periods are omitted from display because settlement treats them as 1x.
			TimePricing: &routing.TimePricingConfig{
				Timezone: "UTC",
				Periods: []routing.TimePricingPeriod{
					{StartTime: "01:00", EndTime: "04:00", Multiplier: 2},
					{StartTime: "03:00", EndTime: "05:00", Multiplier: 3},
				},
			},
		},
	}
	service := testkit.Calculator(nil, nil)

	display := service.DisplayPricingWithResolvedMultipliers("claude-sonnet-4", 1, resolved)

	require.Nil(t, display.MaxReasoningEffortMultiplier)
	require.Nil(t, display.TimePricing)
}

func TestCalculateCostUnified_UsesContinuousMediaUnits(t *testing.T) {
	bs := testkit.ResolverCalculator()
	price := 0.08
	cards := []routing.ModelPricingEntry{{
		Models: []string{"grok-voice-think-fast-2.0"}, BillingMode: routing.BillingModePerRequest,
		PerRequestPrice: &price,
	}}
	r, _ := settingsResolver(bs, billingpricing.DefaultBillingSettings(), cards)
	cost, err := bs.CalculateCostUnified(billing.CostInput{
		Ctx: context.Background(), Model: "grok-voice-think-fast-2.0", GroupID: testkit.GroupID(),
		UsageUnits: 1.5, RateMultiplier: 1, Resolver: r,
	})
	require.NoError(t, err)
	require.InDelta(t, 0.12, cost.TotalCost, 1e-12)
}

func newTestCalculator() *billing.Calculator {
	return newCalculator(nil)
}

func newLadderCalculator(t *testing.T) *billing.Calculator {
	t.Helper()
	return newCalculator(newStubCatalogFromJSON(t, openAILadderCatalogJSON))
}

// newTestPricingConfigServiceWithCache constructs a pricing service from the supplied cache snapshot.
func newTestPricingConfigServiceWithCache(t *testing.T, cache *routingtestkit.ModelConfigData) *routing.PricingConfigService {
	t.Helper()

	cache.LoadedAt = time.Now()
	cs := routingtestkit.ModelConfigFromData(cache)
	return cs
}

// newStubCatalogFromJSON parses a catalog fixture through the production decoder and validator.
func newStubCatalogFromJSON(t *testing.T, body string) *provider.Service {
	t.Helper()
	raw, err := billingpricing.DecodeCatalogEntries([]byte(body))
	require.NoError(t, err)
	data, diagnostics, err := billingpricing.ParsePricingEntries(raw)
	require.NoError(t, diagnostics.ValidationError())
	require.NoError(t, err)
	service := newCatalogFixture(catalogFixture{pricingData: data})
	return service
}

func floatPtr(v float64) *float64 { return &v }
