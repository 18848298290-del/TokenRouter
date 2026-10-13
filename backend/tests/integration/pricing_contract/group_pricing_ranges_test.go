package pricingcontract

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	pricingprovider "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Manual context intervals can leave gaps whose prices come from the card's
// default amount. Marketplace quotes and billed amounts are checked at the
// same left-open, right-closed boundaries, including explicit free prices and
// ranges whose multiplier has no usable base.

// TestPricingDisplayPreservesDefaultRanges supplies reversed manual intervals
// with a priced default and optional Free Fast. It checks displayed gap
// boundaries, the first source interval, Fast ratios, and billed amounts at
// selected context endpoints.
func TestPricingDisplayPreservesDefaultRanges(t *testing.T) {
	for _, source := range []string{"channel"} {
		for _, freeFast := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/freeFast=%v", source, freeFast), func(t *testing.T) {
				card := routing.ModelPricingEntry{
					Models: []string{"custom-ranges"}, BillingMode: routing.BillingModeToken,
					InputPrice: testPtrFloat64(0.001), FastMultiplier: testPtrFloat64(3),
					// Supply the later range first so quote sorting can be
					// checked against the retained source order
					// after display construction.
					Intervals: []routing.PricingInterval{
						{MinTokens: 200, MaxTokens: testPtrInt(300), InputPrice: testPtrFloat64(0.003)},
						{MinTokens: 100, MaxTokens: testPtrInt(150), InputPrice: testPtrFloat64(0.002)},
					},
				}
				group := &routing.Group{
					ID:             100,
					RateMultiplier: 1.5,
				}
				rCalculator := billingtestkit.ResolverCalculator()
				settings := pricing.DefaultBillingSettings()
				settings.FreeOpenAIFast = freeFast
				r := billingtestkit.SharedPriceResolver(rCalculator, group.ID, settings, []routing.ModelPricingEntry{card})
				market := newPricingMarketplaceFixture(nil, nil, r, rCalculator, nil, nil, nil)
				display := market.PublicModelPricing(context.Background(), group, "custom-ranges")
				require.Len(t, display.ContextIntervals, 5)
				require.Equal(t, 200, card.Intervals[0].MinTokens)
				fastRatio := 3.0
				if freeFast {
					fastRatio = 1
				}
				for i, min := range []int{0, 100, 150, 200, 300} {
					require.Equal(t, min, display.ContextIntervals[i].MinTokens)
					if i < 4 {
						require.Equal(t, []int{100, 150, 200, 300}[i], *display.ContextIntervals[i].MaxTokens)
					} else {
						require.Nil(t, display.ContextIntervals[i].MaxTokens)
					}
					// Displayed USD-per-token rates use an absolute tolerance
					// for multiplier rounding.
					require.InDelta(t, display.ContextIntervals[i].InputPricePerToken*fastRatio, display.ContextIntervals[i].FastInputPricePerToken, 1e-12)
				}
				for _, count := range []int{50, 100, 101, 150, 151, 200, 201, 300, 301} {
					var displayed float64
					// Use the quote's (min,max] convention for the expected
					// amount, so exact endpoints exercise the same context
					// range used by billing.
					for _, interval := range display.ContextIntervals {
						if count > interval.MinTokens && (interval.MaxTokens == nil || count <= *interval.MaxTokens) {
							displayed = interval.InputPricePerToken
							break
						}
					}
					cost, err := rCalculator.CalculateCostUnified(billing.CostInput{
						Ctx: context.Background(), Model: "custom-ranges", GroupID: &group.ID,
						Tokens: pricing.UsageTokens{InputTokens: count}, RateMultiplier: group.RateMultiplier, Resolver: r,
					})
					require.NoError(t, err)
					// The billed USD amount allows rounding of the
					// displayed rate product.
					require.InDelta(t, displayed*float64(count), cost.ActualCost, 1e-12, "context=%d", count)
				}
			})
		}
	}
}

// TestPricingDisplayOnlyFlattensCompleteUniformRanges gives one interval the
// same amount as an optional default price. It checks a flattened display when
// all contexts are priced and retains the interval when the default is absent.
func TestPricingDisplayOnlyFlattensCompleteUniformRanges(t *testing.T) {
	rCalculator := billingtestkit.ResolverCalculator()
	for _, pricedBase := range []bool{true, false} {
		card := routing.ModelPricingEntry{
			Models: []string{"custom-uniform"}, BillingMode: routing.BillingModeToken,
			Intervals: []routing.PricingInterval{{MinTokens: 100, MaxTokens: testPtrInt(200), InputPrice: testPtrFloat64(0.001)}},
		}
		if pricedBase {
			card.InputPrice = testPtrFloat64(0.001)
		}
		group := &routing.Group{
			ID:             1,
			RateMultiplier: 1,
		}
		r := billingtestkit.SharedPriceResolver(rCalculator, 1, pricing.DefaultBillingSettings(), []routing.ModelPricingEntry{card})
		market := newPricingMarketplaceFixture(nil, nil, r, rCalculator, nil, nil, nil)
		display := market.PublicModelPricing(context.Background(), group, "custom-uniform")
		if pricedBase {
			require.Empty(t, display.ContextIntervals)
			require.Equal(t, 0.001, display.InputPricePerToken)
		} else {
			require.Len(t, display.ContextIntervals, 1)
			require.Equal(t, 100, display.ContextIntervals[0].MinTokens)
			require.Equal(t, 200, *display.ContextIntervals[0].MaxTokens)
		}
	}
}

// TestPricingIntervalsDistinguishMissingBaseFromExplicitZero supplies
// multiplier intervals with missing, catalog, or explicit zero base amounts.
// It checks unavailable versus priced display status and the corresponding
// error or user cost.
func TestPricingIntervalsDistinguishMissingBaseFromExplicitZero(t *testing.T) {
	for _, source := range []string{"channel"} {
		for _, kind := range []string{"missing", "builtin", "zero_base", "zero_interval"} {
			t.Run(source+"/"+kind, func(t *testing.T) {
				model := "custom-no-base"
				if kind == "builtin" {
					model = "claude-sonnet-4"
				}
				card := routing.ModelPricingEntry{
					Models: []string{model}, BillingMode: routing.BillingModeToken,
					Intervals: []routing.PricingInterval{{MinTokens: 0, InputMultiplier: testPtrFloat64(2)}},
				}
				if kind == "zero_base" {
					card.InputPrice = testPtrFloat64(0)
				}
				if kind == "zero_interval" {
					card.Intervals[0].InputPrice = testPtrFloat64(0)
				}
				err := (routing.PricingConfigValidation{LoadLocation: pricingprovider.LoadPricingLocation}).PricingEntries([]routing.ModelPricingEntry{card})
				require.NoError(t, err)
				group := &routing.Group{
					ID:             100,
					RateMultiplier: 1,
				}
				rCalculator := billingtestkit.ResolverCalculator()
				r := billingtestkit.ResolverWithCards(t, rCalculator, nil)
				if source == "channel" {
					rCalculator = billingtestkit.ResolverCalculator()
					r = billingtestkit.ResolverWithCards(t, rCalculator, []routing.ModelPricingEntry{card})
				}
				cost, err := rCalculator.CalculateCostUnified(billing.CostInput{
					Ctx: context.Background(), Model: model, GroupID: &group.ID,
					Tokens: pricing.UsageTokens{InputTokens: 50}, RateMultiplier: 1, Resolver: r,
				})
				market := newPricingMarketplaceFixture(nil, nil, r, rCalculator, nil, nil, nil)
				display := market.PublicModelPricing(context.Background(), group, model)
				if kind == "missing" {
					require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
					require.Nil(t, cost)
					require.Equal(t, "unpriced", display.PriceStatus)
				} else {
					require.NoError(t, err)
					require.Equal(t, "priced", display.PriceStatus)
					expected := 0.0
					if kind == "builtin" {
						expected = 50 * 3e-6 * 2
					}
					// The interval USD charge allows rounding of token-rate
					// products.
					require.InDelta(t, expected, cost.ActualCost, 1e-12)
				}
			})
		}
	}
}

// TestPricingMissingMultiplierRangeDoesNotBorrowOtherIntervalPrice gives one
// context range only a multiplier and another an absolute price. It checks
// unavailable billing in the first range and exposes the priced second range
// in the quote.
func TestPricingMissingMultiplierRangeDoesNotBorrowOtherIntervalPrice(t *testing.T) {
	rCalculator := billingtestkit.ResolverCalculator()
	r := billingtestkit.SharedPriceResolver(rCalculator, 1, pricing.DefaultBillingSettings(), []routing.ModelPricingEntry{{Models: []string{"custom-partial"}, BillingMode: routing.BillingModeToken, Intervals: []routing.PricingInterval{{MinTokens: 0, MaxTokens: testPtrInt(100), InputMultiplier: testPtrFloat64(2)}, {MinTokens: 100, InputPrice: testPtrFloat64(0.003)}}}})
	group := &routing.Group{
		ID:             1,
		RateMultiplier: 1,
	}
	for _, count := range []int{50, 150} {
		cost, err := rCalculator.CalculateCostUnified(billing.CostInput{
			Ctx: context.Background(), Model: "custom-partial", GroupID: &group.ID,
			Tokens: pricing.UsageTokens{InputTokens: count}, RateMultiplier: 1, Resolver: r,
		})
		// The first context range has no base price to multiply.
		if count <= 100 {
			require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
		} else {
			require.NoError(t, err)
			require.Equal(t, 0.45, cost.ActualCost)
		}
	}
	market := newPricingMarketplaceFixture(nil, nil, r, rCalculator, nil, nil, nil)
	display := market.PublicModelPricing(context.Background(), group, "custom-partial")
	require.Len(t, display.ContextIntervals, 1)
	require.Equal(t, 100, display.ContextIntervals[0].MinTokens)
}
