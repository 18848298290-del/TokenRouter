package billing

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// TestPerRequestContextPricingIgnoresModelName checks that model names do not affect per-request interval or
// default-price selection.
func TestPerRequestContextPricingIgnoresModelName(t *testing.T) {
	for _, model := range []string{"custom-chat", "custom-image-analyzer", "gpt-image-alias"} {
		for _, withDefault := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/default=%t", model, withDefault), func(t *testing.T) {
				defaultPrice, shortPrice, longPrice := 0.05, 0.03, 0.10
				boundary := 128000
				card := &ModelPricingEntry{
					Models: []string{model}, BillingMode: pricing.BillingModePerRequest,
					Intervals: []pricing.PricingInterval{
						{MinTokens: 0, MaxTokens: &boundary, PerRequestPrice: &shortPrice},
						{MinTokens: boundary, PerRequestPrice: &longPrice},
					},
				}
				if withDefault {
					card.PerRequestPrice = &defaultPrice
				}
				calculator := NewCalculator(nil, CalculatorOptions{})
				resolver := NewPriceResolver(mediaPriceCards{card: card}, calculator, nil, nil)
				groupID := int64(1)
				for _, tc := range []struct {
					input int
					want  float64
				}{
					{1000, 0.03}, {127000, 0.03}, {127001, 0.10}, {200000, 0.10},
				} {
					resolved := resolver.Resolve(context.Background(), PricingInput{Model: model, GroupID: &groupID})
					before := append([]pricing.PricingInterval(nil), resolved.RequestTiers...)
					cost, err := calculator.CalculateCostUnified(CostInput{
						Ctx: context.Background(), Model: model, GroupID: &groupID,
						Tokens:       UsageTokens{InputTokens: tc.input, CacheReadTokens: 500, CacheCreationTokens: 500},
						RequestCount: 2, RateMultiplier: 1.5, Resolver: resolver, Resolved: resolved,
					})
					require.NoError(t, err)
					require.InDelta(t, tc.want*2, cost.TotalCost, 1e-12)
					require.InDelta(t, tc.want*3, cost.ActualCost, 1e-12)
					require.Equal(t, string(pricing.BillingModePerRequest), cost.BillingMode)
					require.Equal(t, before, resolved.RequestTiers, "不能清空或修改已解析区间")
				}
			})
		}
	}
}

// TestPerRequestLabelStillPrecedesContext checks that labels take precedence over context intervals and default prices.
func TestPerRequestLabelStillPrecedesContext(t *testing.T) {
	labelPrice, contextPrice, defaultPrice := 0.07, 0.10, 0.05
	card := &ModelPricingEntry{
		BillingMode: pricing.BillingModePerRequest, PerRequestPrice: &defaultPrice,
		Intervals: []pricing.PricingInterval{
			{TierLabel: "HD", PerRequestPrice: &labelPrice, MinTokens: 300000},
			{MinTokens: 128000, PerRequestPrice: &contextPrice},
		},
	}
	calculator := NewCalculator(nil, CalculatorOptions{})
	resolver := NewPriceResolver(mediaPriceCards{card: card}, calculator, nil, nil)
	groupID := int64(1)
	for _, tc := range []struct {
		size   string
		tokens int
		want   float64
	}{
		{"HD", 200000, 0.07}, {"", 200000, 0.10}, {"", 1000, 0.05},
	} {
		cost, err := calculator.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: "custom-image-analyzer", GroupID: &groupID,
			Tokens: UsageTokens{InputTokens: tc.tokens}, SizeTier: tc.size,
			RequestCount: 1, RateMultiplier: 1, Resolver: resolver,
		})
		require.NoError(t, err)
		require.InDelta(t, tc.want, cost.ActualCost, 1e-12)
	}
}
