package pricingcontract

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// TestMediaPricingCardsHaveSameGroupAndPricingConfigSemantics checks configured media prices across billing modes and zero-price cases.
func TestMediaPricingCardsHaveSameGroupAndPricingConfigSemantics(t *testing.T) {
	for _, media := range []string{"image", "video"} {
		for _, perRequest := range []bool{false, true} {
			for _, zero := range []bool{false, true} {
				model, mode, tier := "gpt-image-2", routing.BillingModeImage, "4K"
				result := &forwardcore.OpenAIResult{Model: model, ImageCount: 3, ImageSize: tier}
				units := 3.0
				if media == "video" {
					model, mode, tier = "grok-imagine-video", routing.BillingModeVideo, "720p"
					result = &forwardcore.OpenAIResult{Model: model, VideoCount: 2, VideoDurationSeconds: 7, VideoResolution: tier}
					units = 14
				}
				if perRequest {
					mode = routing.BillingModePerRequest
					if media == "video" {
						units = 2
					}
				}
				price := 0.2
				if zero {
					price = 0
				}
				for _, scope := range []string{"channel"} {
					t.Run(media+"/"+string(mode)+"/"+scope+"/"+map[bool]string{true: "free", false: "paid"}[zero], func(t *testing.T) {
						card := routing.ModelPricingEntry{Models: []string{model}, BillingMode: mode, PerRequestPrice: testPtrFloat64(9), Intervals: []routing.PricingInterval{{TierLabel: tier, PerRequestPrice: &price}}}
						group := &routing.Group{ID: 100, RateMultiplier: 1.5}
						cards := []routing.ModelPricingEntry{card}

						billing := newCalculator(nil)
						resolver := billingtestkit.ResolverWithCards(t, billing, cards)
						svc := completion.NewRecorder(completion.Dependencies{Calculator: billing, Prices: resolver}, completion.RecorderOptions{DefaultMultiplier: 1})

						key := &apikey.APIKey{GroupID: &group.ID, Group: group}
						cost, err := svc.CalculateOpenAIRecordUsageCostAt(context.Background(), gatewaycapture.ProjectOpenAICompletionResult(result, nil), gatewaycapture.ProjectCompletionKey(key), []string{model}, 6, 1.5, 1.5, 1.5, pricing.UsageTokens{}, "priority", time.Time{})
						require.NoError(t, err)
						require.InDelta(t, price*units, cost.TotalCost, 1e-12)
						require.InDelta(t, price*units*1.5, cost.ActualCost, 1e-12)
					})
				}
			}
		}
	}
}

// TestAsyncImageUnitPricingUsesCardsAndPerImageFallback checks per-image prices by card and size.
func TestAsyncImageUnitPricingUsesCardsAndPerImageFallback(t *testing.T) {
	ctx := context.Background()
	model := "gemini-3.1-flash-image"
	billing := newCalculator(newCatalogFixture(catalogFixture{pricingData: map[string]*pricing.CatalogModelPricing{model: {OutputCostPerToken: 0.000001, OutputCostPerImageToken: 0.000002, OutputCostPerImage: 0.2}}}))
	pricingConfig := routing.ModelPricingEntry{Models: []string{model}, BillingMode: routing.BillingModeImage, PerRequestPrice: testPtrFloat64(0.4), Intervals: []routing.PricingInterval{{TierLabel: "512", PerRequestPrice: testPtrFloat64(0)}}}
	resolver := billingtestkit.ResolverWithCards(t, billing, []routing.ModelPricingEntry{pricingConfig})
	group := &routing.Group{ID: 100}
	batch := &batchimage.Pricing{Resolver: resolver}
	creativeService := &creative.Public{ImageUnitPrice: creativePriceFixture(billing, resolver)}
	for _, tc := range []struct {
		size string
		want float64
	}{{"512", 0}, {"1K", 0.4}, {"4K", 0.4}} {
		price, err := batch.BatchImageUnitPrice(ctx, batchimage.BatchImagePriceInput{Model: model, GroupID: &group.ID, Group: &batchimage.GroupView{}, ImageSize: tc.size})
		require.NoError(t, err)
		require.InDelta(t, tc.want, price, 1e-12)
		unit, ok := creativeService.ImageUnitPrice(ctx, creativeGroupProjection(group), model, tc.size)
		require.True(t, ok)
		require.Equal(t, price, unit)
	}
	resolver = billingtestkit.SharedPriceResolver(billing, group.ID, pricing.DefaultBillingSettings(), []routing.ModelPricingEntry{{Models: []string{model}, BillingMode: routing.BillingModeImage, PerRequestPrice: testPtrFloat64(0.6)}})
	price, err := resolver.ResolveImageUnitPrice(ctx, billingcore.PricingInput{Model: model, GroupID: &group.ID}, "1K")
	require.NoError(t, err)
	require.InDelta(t, 0.6, price, 1e-12)
	resolver = billingtestkit.SharedPriceResolver(billing, group.ID, pricing.DefaultBillingSettings(), []routing.ModelPricingEntry{{Models: []string{model}, BillingMode: routing.BillingModeImage, Intervals: []routing.PricingInterval{{TierLabel: "512", PerRequestPrice: testPtrFloat64(0)}}}})
	price, err = resolver.ResolveImageUnitPrice(ctx, billingcore.PricingInput{Model: model, GroupID: &group.ID}, "2K")
	require.NoError(t, err)
	require.InDelta(t, 0.2, price, 1e-12)
	price, err = resolver.ResolveImageUnitPrice(ctx, billingcore.PricingInput{Model: model, GroupID: &group.ID}, "512")
	require.NoError(t, err)
	require.Zero(t, price)

	resolver = billingtestkit.SharedPriceResolver(billing, group.ID, pricing.DefaultBillingSettings(), []routing.ModelPricingEntry{{Models: []string{model}, BillingMode: routing.BillingModeToken, ImageOutputPrice: testPtrFloat64(0.000009)}})
	price, err = resolver.ResolveImageUnitPrice(ctx, billingcore.PricingInput{Model: model, GroupID: &group.ID}, "2K")
	require.NoError(t, err)
	require.InDelta(t, 0.2, price, 1e-12)
}

func creativeGroupProjection(value *routing.Group) *creative.GroupView {
	if value == nil {
		return nil
	}
	return &creative.GroupView{ID: value.ID, Name: value.Name, IsExclusive: value.IsExclusive, AllowImageGeneration: value.AllowImageGeneration, Active: value.IsActive(), RateMultiplier: value.RateMultiplier, Operations: creative.OperationsForGroup(value.ResponsesImagePolicy != "" || value.ProtocolFallbacks != nil, value.AllowsClientProtocol)}
}

// creativePriceFixture resolves image prices with the supplied resolver or a catalog-backed resolver.
func creativePriceFixture(calculator *billingcore.Calculator, resolver *billingcore.PriceResolver) func(context.Context, *creative.GroupView, string, string) (float64, bool) {
	return func(ctx context.Context, group *creative.GroupView, model, size string) (float64, bool) {
		if group == nil {
			return 0, false
		}
		selected := resolver
		if selected == nil && calculator != nil {
			selected = billingcore.NewPriceResolver(nil, calculator, modelidentity.Identity, func(model string, err error) {
				slog.Debug("failed to get model pricing from model catalog, using fallback", "model", model, "error", err)
			})
		}
		value, err := selected.ResolveImageUnitPrice(ctx, billingcore.PricingInput{Model: model, GroupID: &group.ID}, size)
		return value, err == nil
	}
}
