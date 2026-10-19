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

// A media card can price output images, video seconds, or completed operations
// according to its selected billing mode. Gateway and asynchronous image-unit
// queries use the same cards to resolve their configured amounts. Image-mode
// unit lookup can use catalog per-image pricing for a missing manual size
// amount, while an explicit zero price remains free.

// TestMediaPricingCardsHaveSameGroupAndPricingConfigSemantics supplies paid
// and free size-label cards for image, video, and per-request modes. It checks
// output-unit totals and the configured group multiplier through
// the shared-card fixture.
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
					// Video mode prices the total seconds across
					// both reported outputs.
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
						// Output-unit USD totals allow rounding of
						// the size-price product.
						require.InDelta(t, price*units, cost.TotalCost, 1e-12)
						require.InDelta(t, price*units*1.5, cost.ActualCost, 1e-12)
					})
				}
			}
		}
	}
}

// TestAsyncImageUnitPricingUsesCardsAndPerImageFallback queries batch and
// creative unit prices from the same card. It checks size-label zero prices,
// card defaults, and independent catalog per-image pricing when manual media
// prices are absent.
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
		size string  // image size label
		want float64 // expected unit price USD
	}{{"512", 0}, {"1K", 0.4}, {"4K", 0.4}} {
		price, err := batch.BatchImageUnitPrice(ctx, batchimage.BatchImagePriceInput{Model: model, GroupID: &group.ID, Group: &batchimage.GroupView{}, ImageSize: tc.size})
		require.NoError(t, err)
		// Compare USD-per-image prices with an
		// absolute floating-point tolerance.
		require.InDelta(t, tc.want, price, 1e-12)
		unit, ok := creativeService.ImageUnitPrice(ctx, creativeGroupProjection(group), model, tc.size)
		require.True(t, ok)
		require.Equal(t, price, unit)
	}
	resolver = billingtestkit.SharedPriceResolver(billing, group.ID, pricing.DefaultBillingSettings(), []routing.ModelPricingEntry{{Models: []string{model}, BillingMode: routing.BillingModeImage, PerRequestPrice: testPtrFloat64(0.6)}})
	price, err := resolver.ResolveImageUnitPrice(ctx, billingcore.PricingInput{Model: model, GroupID: &group.ID}, "1K")
	require.NoError(t, err)
	// Compare USD-per-image prices with an absolute floating-point tolerance.
	require.InDelta(t, 0.6, price, 1e-12)
	resolver = billingtestkit.SharedPriceResolver(billing, group.ID, pricing.DefaultBillingSettings(), []routing.ModelPricingEntry{{Models: []string{model}, BillingMode: routing.BillingModeImage, Intervals: []routing.PricingInterval{{TierLabel: "512", PerRequestPrice: testPtrFloat64(0)}}}})
	price, err = resolver.ResolveImageUnitPrice(ctx, billingcore.PricingInput{Model: model, GroupID: &group.ID}, "2K")
	require.NoError(t, err)
	// Compare USD-per-image prices with an absolute floating-point tolerance.
	require.InDelta(t, 0.2, price, 1e-12)
	price, err = resolver.ResolveImageUnitPrice(ctx, billingcore.PricingInput{Model: model, GroupID: &group.ID}, "512")
	require.NoError(t, err)
	require.Zero(t, price)

	resolver = billingtestkit.SharedPriceResolver(billing, group.ID, pricing.DefaultBillingSettings(), []routing.ModelPricingEntry{{Models: []string{model}, BillingMode: routing.BillingModeToken, ImageOutputPrice: testPtrFloat64(0.000009)}})
	price, err = resolver.ResolveImageUnitPrice(ctx, billingcore.PricingInput{Model: model, GroupID: &group.ID}, "2K")
	require.NoError(t, err)
	// Compare USD-per-image prices with an absolute floating-point tolerance.
	require.InDelta(t, 0.2, price, 1e-12)
}

// creativeGroupProjection converts a routing group into creative
// image-permission metadata. Nil groups return nil, and the returned view
// carries the group's identifiers, rate, and
// protocol-aware operation settings.
func creativeGroupProjection(value *routing.Group) *creative.GroupView {
	if value == nil {
		return nil
	}
	return &creative.GroupView{ID: value.ID, Name: value.Name, IsExclusive: value.IsExclusive, AllowImageGeneration: value.AllowImageGeneration, Active: value.IsActive(), RateMultiplier: value.RateMultiplier, Operations: creative.OperationsForGroup(value.ResponsesImagePolicy != "" || value.ProtocolFallbacks != nil, value.AllowsClientProtocol)}
}

// creativePriceFixture returns a callback that resolves one image's unit
// price. It builds a catalog-backed resolver when only calculator is supplied;
// nil groups, missing pricing dependencies, and lookup errors produce
// false for availability.
func creativePriceFixture(calculator *billingcore.Calculator, resolver *billingcore.PriceResolver) func(context.Context, *creative.GroupView, string, string) (float64, bool) {
	return func(ctx context.Context, group *creative.GroupView, model, size string) (float64, bool) {
		if group == nil {
			return 0, false
		}
		selected := resolver
		// An omitted resolver can still query the calculator's catalog-backed
		// image price. Construct that dependency before invoking the
		// common image-unit lookup.
		if selected == nil && calculator != nil {
			selected = billingcore.NewPriceResolver(nil, calculator, modelidentity.Identity, func(model string, err error) {
				slog.Debug("failed to get model pricing from model catalog, using fallback", "model", model, "error", err)
			})
		}
		value, err := selected.ResolveImageUnitPrice(ctx, billingcore.PricingInput{Model: model, GroupID: &group.ID}, size)
		// Expose lookup success through the creative callback's boolean. An
		// error leaves the returned amount available but marks unit pricing
		// unavailable to the caller.
		return value, err == nil
	}
}

// TestVideoPublicQuoteMatchesConfiguredSettlement compares public group quotes
// with completed task charges under card and group multipliers. Custom dimensions,
// aliases, explicit free tiers, and default-only cards use identical unit prices.
func TestVideoPublicQuoteMatchesConfiguredSettlement(t *testing.T) {
	ctx := context.Background()
	groupID := int64(100)
	for _, tiers := range []bool{false, true} {
		card := routing.ModelPricingEntry{Models: []string{"custom-video"}, BillingMode: routing.BillingModeVideo, PerRequestPrice: testPtrFloat64(0.4), PriceMultiplier: testPtrFloat64(2)}
		if tiers {
			card.Intervals = []routing.PricingInterval{{TierLabel: "768P", PerRequestPrice: testPtrFloat64(0.12)}, {TierLabel: "2K", PerRequestPrice: testPtrFloat64(0)}, {TierLabel: "HD", PerRequestPrice: testPtrFloat64(0.1)}}
		}
		calculator := newCalculator(nil)
		resolver := billingtestkit.ResolverWithCards(t, calculator, []routing.ModelPricingEntry{card})
		recorder := completion.NewRecorder(completion.Dependencies{Calculator: calculator, Prices: resolver}, completion.RecorderOptions{DefaultMultiplier: 1})
		quote := resolver.PublicQuote(ctx, billingcore.PublicQuoteInput{PricingInput: billingcore.PricingInput{Model: "custom-video", GroupID: &groupID}, RateMultiplier: 1.5})
		require.Equal(t, "video", quote.PricingMode)
		require.NotNil(t, quote.VideoPricePerSecond)
		require.InDelta(t, 1.2, *quote.VideoPricePerSecond, 1e-12)
		for _, resolution := range []string{"768p", "2k", "720p", "unlisted"} {
			result := &completion.Result{VideoCount: 2, VideoDurationSeconds: 60, VideoResolution: resolution}
			cost, err := recorder.CalculateOpenAIVideoCost(ctx, "custom-video", &completion.KeySnapshot{GroupID: &groupID, Group: &completion.GroupSnapshot{ID: groupID, RateMultiplier: 1.5}}, result, 1.5)
			require.NoError(t, err)
			unit, found := quote.VideoPrices[resolution]
			if !found {
				unit = *quote.VideoPricePerSecond
			}
			require.InDelta(t, unit*120, cost.ActualCost, 1e-12)
		}
	}
}
