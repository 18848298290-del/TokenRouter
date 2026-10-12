package billing

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// TestPerRequestContextPricingIgnoresModelName prices context intervals for
// chat and image-looking full model names. It checks input-plus-cache
// thresholds, base/user amounts and retained resolved tiers with or without a
// default price.
func TestPerRequestContextPricingIgnoresModelName(t *testing.T) {
	for _, model := range []string{"custom-chat", "custom-image-analyzer", "gpt-image-alias"} {
		for _, withDefault := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/default=%t", model, withDefault), func(t *testing.T) {
				defaultPrice, shortPrice, longPrice := 0.05, 0.03, 0.10
				// Each case also supplies one thousand cache tokens, so interval selection
				// uses more context than the table's text input alone.
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
					input int     // reported text input tokens
					want  float64 // expected quoted USD amount
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
					// Decimal rates and multiplicative quote factors use binary floating
					// point; this tolerance distinguishes arithmetic rounding from a changed
					// unit amount.
					require.InDelta(t, tc.want*2, cost.TotalCost, 1e-12)
					// Decimal rates and multiplicative quote factors use binary floating
					// point; this tolerance distinguishes arithmetic rounding from a changed
					// unit amount.
					require.InDelta(t, tc.want*3, cost.ActualCost, 1e-12)
					require.Equal(t, string(pricing.BillingModePerRequest), cost.BillingMode)
					require.Equal(t, before, resolved.RequestTiers, "不能清空或修改已解析区间")
				}
			})
		}
	}
}

// TestPerRequestLabelStillPrecedesContext combines label, context and default
// amounts on an image-looking model name. It checks the matching label wins
// before context and default selection for the supplied requests.
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
		size   string  // requested price tier label
		tokens int     // reported context token count
		want   float64 // expected quoted USD amount
	}{
		{"HD", 200000, 0.07}, {"", 200000, 0.10}, {"", 1000, 0.05},
	} {
		cost, err := calculator.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: "custom-image-analyzer", GroupID: &groupID,
			Tokens: UsageTokens{InputTokens: tc.tokens}, SizeTier: tc.size,
			RequestCount: 1, RateMultiplier: 1, Resolver: resolver,
		})
		require.NoError(t, err)
		// Decimal rates and multiplicative quote factors use binary floating point;
		// this tolerance distinguishes arithmetic rounding from a changed unit
		// amount.
		require.InDelta(t, tc.want, cost.ActualCost, 1e-12)
	}
}
