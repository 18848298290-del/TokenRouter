package billing

// These cases check quote and settlement consistency across calculator.go, media_pricing.go, and public_quote.go.

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// TestMediaUnitPriceRejectsMissingPrice checks that missing per-image rates do not produce a guessed unit price.
func TestMediaUnitPriceRejectsMissingPrice(t *testing.T) {
	calculator := NewCalculator(defaultCatalogStub{entries: map[string]*pricing.CatalogModelPricing{
		"grok-imagine-video-1.5": {CatalogRules: pricing.CatalogRules{VideoPrices: map[string]float64{"480p": 0.08}}, TokenPricingAbsent: true},
		"token-image":            {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
	}}, CalculatorOptions{})
	resolver := NewPriceResolver(nil, calculator, nil, nil)
	for _, model := range []string{"gemini-3.8-flash-image-high", "grok-imagine", "token-image", "grok-imagine-video"} {
		_, err := resolver.ResolveImageUnitPrice(context.Background(), PricingInput{Model: model}, "1K")
		require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable, model)
		cost, err := calculator.CalculateImageCost(model, "1K", 1, 1)
		require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable, model)
		require.Nil(t, cost)
	}
	for _, model := range []string{"grok-imagine-video-1.5-preview", "custom-video", "grok-imagine-image-quality"} {
		cost, err := calculator.CalculateVideoCost(model, "480p", 1, 5, 1)
		require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable, model)
		require.Nil(t, cost)
	}
	video, err := calculator.CalculateVideoCost("grok-imagine-video-1.5", "480p", 1, 5, 1)
	require.NoError(t, err)
	require.InDelta(t, 0.4, video.ActualCost, 1e-12)
}

// TestPartialImageCardQuoteMatchesReservation checks that displayed sizes use the task reservation unit prices.
func TestPartialImageCardQuoteMatchesReservation(t *testing.T) {
	for _, oneK := range []float64{0, 0.1} {
		t.Run(fmt.Sprint(oneK), func(t *testing.T) {
			groupID := int64(1)
			calculator := NewCalculator(defaultCatalogStub{entries: map[string]*pricing.CatalogModelPricing{
				"custom-image": {CatalogRules: pricing.CatalogRules{ImagePrices: pricing.MediaPrices{"1K": 0.5, "2K": 0.134}}, TokenPricingAbsent: true},
			}}, CalculatorOptions{})
			resolver := NewPriceResolver(mediaPriceCards{card: &ModelPricingEntry{BillingMode: pricing.BillingModeImage, Intervals: []pricing.PricingInterval{{TierLabel: "1K", PerRequestPrice: &oneK}}}}, calculator, nil, nil)
			input := PricingInput{Model: "custom-image", GroupID: &groupID}
			quote := resolver.PublicQuote(context.Background(), PublicQuoteInput{PricingInput: input, RateMultiplier: 2})
			require.Equal(t, "priced", quote.PriceStatus)
			require.ElementsMatch(t, []string{"1K", "2K"}, quote.ImagePriceSizes)
			require.InDelta(t, oneK*2, quote.ImagePrice1K, 1e-12)
			unit, err := resolver.ResolveImageUnitPrice(context.Background(), input, "2K")
			require.NoError(t, err)
			require.InDelta(t, unit*2, quote.ImagePrice2K, 1e-12)
			cost, err := calculator.CalculateCostUnified(CostInput{Ctx: context.Background(), Model: input.Model, GroupID: input.GroupID, RequestCount: 3, SizeTier: "2K", RateMultiplier: 2, Resolver: resolver})
			require.NoError(t, err)
			require.InDelta(t, quote.ImagePrice2K*3, cost.ActualCost, 1e-12)
			_, err = resolver.ResolveImageUnitPrice(context.Background(), input, "4K")
			require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
		})
	}
}

// TestPerRequestMediaPathsShareCardPrices compares public quotes, task unit prices, and request settlement.
func TestPerRequestMediaPathsShareCardPrices(t *testing.T) {
	for _, mode := range []pricing.BillingMode{pricing.BillingModePerRequest, pricing.BillingModeImage} {
		for _, defaultValue := range []*float64{nil, requestTestPrice(0), requestTestPrice(0.3)} {
			label := "missing"
			if defaultValue != nil {
				label = fmt.Sprint(*defaultValue)
			}
			t.Run(fmt.Sprintf("%s/default=%s", mode, label), func(t *testing.T) {
				oneK := 0.1
				calculator := NewCalculator(defaultCatalogStub{entries: map[string]*pricing.CatalogModelPricing{
					"custom-image": {CatalogRules: pricing.CatalogRules{ImagePrices: pricing.MediaPrices{"1K": 0.5, "2K": 0.2}}, TokenPricingAbsent: true},
				}}, CalculatorOptions{})
				resolver := NewPriceResolver(mediaPriceCards{card: &ModelPricingEntry{
					BillingMode: mode, PerRequestPrice: defaultValue,
					Intervals: []pricing.PricingInterval{{TierLabel: "1K", PerRequestPrice: &oneK}},
				}}, calculator, nil, nil)
				groupID := int64(1)
				input := PricingInput{Model: "custom-image", GroupID: &groupID}
				quote := resolver.PublicQuote(context.Background(), PublicQuoteInput{PricingInput: input, RateMultiplier: 2})
				require.Contains(t, quote.ImagePriceSizes, "1K")
				require.InDelta(t, 0.2, quote.ImagePrice1K, 1e-12)
				for _, size := range []string{"1K", "2K", "4K"} {
					fixed, priceErr := resolver.ResolveImageUnitPrice(context.Background(), input, size)
					for _, tokens := range []int{0, 200000} {
						cost, err := calculator.CalculateCostUnified(CostInput{
							Ctx: context.Background(), Model: input.Model, GroupID: input.GroupID,
							Tokens: UsageTokens{InputTokens: tokens}, RequestCount: 3, SizeTier: size,
							RateMultiplier: 2, Resolver: resolver,
						})
						if priceErr != nil {
							require.ErrorIs(t, priceErr, pricing.ErrModelPricingUnavailable)
							require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
							require.NotContains(t, quote.ImagePriceSizes, size)
							continue
						}
						require.NoError(t, err)
						require.Contains(t, quote.ImagePriceSizes, size)
						require.InDelta(t, fixed*6, cost.ActualCost, 1e-12)
						displayed := map[string]float64{"1K": quote.ImagePrice1K, "2K": quote.ImagePrice2K, "4K": quote.ImagePrice4K}[size]
						require.InDelta(t, fixed*2, displayed, 1e-12)
					}
				}
				if mode == pricing.BillingModePerRequest && defaultValue == nil {
					require.Equal(t, []string{"1K"}, quote.ImagePriceSizes, "按次价卡缺尺寸时不能借目录价")
				}
			})
		}
	}
}

// TestContextRequestCardHasNoFixedImageQuote checks that unknown context prevents treating a default or interval
// rate as a fixed image price.
func TestContextRequestCardHasNoFixedImageQuote(t *testing.T) {
	low, high, base := 0.03, 0.10, 0.05
	maxTokens := 128000
	calc := NewCalculator(defaultCatalogStub{entries: map[string]*pricing.CatalogModelPricing{
		"custom-image": {CatalogRules: pricing.CatalogRules{ImagePrices: pricing.MediaPrices{"2K": 0.2}}, TokenPricingAbsent: true},
	}}, CalculatorOptions{})
	resolver := NewPriceResolver(mediaPriceCards{card: &ModelPricingEntry{
		BillingMode: pricing.BillingModePerRequest, PerRequestPrice: &base,
		Intervals: []pricing.PricingInterval{
			{MinTokens: 0, MaxTokens: &maxTokens, PerRequestPrice: &low},
			{MinTokens: maxTokens, PerRequestPrice: &high},
		},
	}}, calc, nil, nil)
	group := int64(1)
	input := PricingInput{Model: "custom-image", GroupID: &group}
	quote := resolver.PublicQuote(context.Background(), PublicQuoteInput{PricingInput: input, RateMultiplier: 1})
	require.Equal(t, "unpriced", quote.PriceStatus, "只能说明没有固定报价，不能伪造单张价")
	_, err := resolver.ResolveImageUnitPrice(context.Background(), input, "2K")
	require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
	for _, tc := range []struct {
		tokens int
		want   float64
	}{{0, 0.05}, {128000, 0.03}, {128001, 0.10}} {
		cost, err := calc.CalculateCostUnified(CostInput{Ctx: context.Background(), Model: input.Model, GroupID: input.GroupID, Tokens: UsageTokens{InputTokens: tc.tokens}, SizeTier: "2K", RequestCount: 1, RateMultiplier: 1, Resolver: resolver})
		require.NoError(t, err)
		require.InDelta(t, tc.want, cost.ActualCost, 1e-12)
	}
}

func requestTestPrice(value float64) *float64 { return &value }
