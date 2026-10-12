package pricingcontract

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	pricingprovider "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestOpenAIMediaPricingUsesModifierOnlyCards checks that token cards retain their multipliers when base prices come from the media catalog.
func TestOpenAIMediaPricingUsesModifierOnlyCards(t *testing.T) {
	for _, media := range []string{"image", "video"} {
		model := "gpt-image-1"
		if media == "video" {
			model = "grok-imagine-video"
		}
		for _, scope := range []string{"channel"} {
			for _, kind := range []string{"fast", "flex", "max", "time", "combined"} {
				t.Run(media+"/"+scope+"/"+kind, func(t *testing.T) {
					card := routing.ModelPricingEntry{Models: []string{model}, BillingMode: routing.BillingModeToken}
					factor, tier, effort := 1.0, "", ""
					if kind == "fast" || kind == "combined" {
						card.FastMultiplier, tier = testPtrFloat64(2), "priority"
						factor *= 2
					}
					if kind == "flex" {
						card.FlexMultiplier, tier = testPtrFloat64(0.4), "flex"
						factor *= 0.4
					}
					if kind == "max" || kind == "combined" {
						card.MaxReasoningEffortMultiplier, effort = testPtrFloat64(3), "max"
						factor *= 3
					}
					if kind == "time" || kind == "combined" {
						card.TimePricing = &routing.TimePricingConfig{Timezone: "UTC", Periods: []routing.TimePricingPeriod{{StartTime: "00:00", EndTime: "12:00", Multiplier: 2}}}
						factor *= 2
					}
					require.NoError(t, (routing.PricingConfigValidation{LoadLocation: pricingprovider.LoadPricingLocation}).PricingEntries([]routing.ModelPricingEntry{card}))
					billing := newCalculator(newCatalogFixture(catalogFixture{pricingData: map[string]*pricing.CatalogModelPricing{
						model: {Mode: media, InputCostPerToken: 0.001, OutputCostPerToken: 0.002, OutputCostPerImageToken: 0.004},
					}}))
					group := &routing.Group{ID: 100}
					pricingConfigCards := []routing.ModelPricingEntry{card}

					resolver := billingtestkit.ResolverWithCards(t, billing, pricingConfigCards)
					svc := completion.NewRecorder(completion.Dependencies{Calculator: billing, Prices: resolver}, completion.RecorderOptions{DefaultMultiplier: 1})

					key := &apikey.APIKey{GroupID: &group.ID, Group: group}
					resolved := svc.ResolveConfigPricing(context.Background(), model, gatewaycapture.ProjectCompletionKey(key))
					require.NotNil(t, resolved)
					require.Equal(t, pricing.PricingSourceCatalog, resolved.Source)
					result := &forwardcore.OpenAIResult{Model: model, ReasoningEffort: &effort, ImageCount: 1}
					if media == "video" {
						result.ImageCount, result.VideoCount = 0, 1
						result.VideoDurationSeconds = 8
					}
					cost, err := svc.CalculateOpenAIRecordUsageCostAt(context.Background(), gatewaycapture.ProjectOpenAICompletionResult(result, nil), gatewaycapture.ProjectCompletionKey(key), []string{model}, 1.5, 0.7, 0.8, 1,
						pricing.UsageTokens{InputTokens: 100, OutputTokens: 50, ImageOutputTokens: 50}, tier, time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC))
					require.NoError(t, err)
					require.Equal(t, string(routing.BillingModeToken), cost.BillingMode)
					require.InDelta(t, 0.3*factor, cost.TotalCost, 1e-12)
					require.InDelta(t, 0.3*factor*1.5, cost.ActualCost, 1e-12)
				})
			}
		}
	}
}

// TestOpenAIMediaModifiersPreserveInheritedRequestBilling checks image and video modes without token-only multipliers.
func TestOpenAIMediaModifiersPreserveInheritedRequestBilling(t *testing.T) {
	for _, mode := range []routing.BillingMode{routing.BillingModeImage, routing.BillingModeVideo} {
		t.Run(string(mode), func(t *testing.T) {
			model := "gpt-image-1"
			result := &forwardcore.OpenAIResult{Model: model, ImageCount: 2}
			wantTotal, rate := 0.5, 0.7
			if mode == routing.BillingModeVideo {
				model = "grok-imagine-video"
				result = &forwardcore.OpenAIResult{Model: model, VideoCount: 2, VideoDurationSeconds: 8}
				wantTotal, rate = 4, 0.8
			}
			billing := newCalculator(nil)
			resolver := billingtestkit.ResolverWithCards(t, billing, []routing.ModelPricingEntry{{Models: []string{model}, BillingMode: mode, PerRequestPrice: testPtrFloat64(0.25)}})
			group := &routing.Group{ID: 100}
			svc := completion.NewRecorder(completion.Dependencies{Calculator: billing, Prices: resolver}, completion.RecorderOptions{DefaultMultiplier: 1})

			cost, err := svc.CalculateOpenAIRecordUsageCostAt(context.Background(), gatewaycapture.ProjectOpenAICompletionResult(result, nil), gatewaycapture.ProjectCompletionKey(&apikey.APIKey{Group: group}), []string{model}, 1.5, 0.7, 0.8, 1,
				pricing.UsageTokens{InputTokens: 100}, "priority", time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC))
			require.NoError(t, err)
			require.Equal(t, string(mode), cost.BillingMode)
			require.InDelta(t, wantTotal, cost.TotalCost, 1e-12)
			require.InDelta(t, wantTotal*rate, cost.ActualCost, 1e-12)
		})
	}
}

// TestCNProviderPricingModifiersDoNotCountAsExplicitPrices checks that multiplier-only cards cannot enable Claude catalog prices for CN providers.
func TestCNProviderPricingModifiersDoNotCountAsExplicitPrices(t *testing.T) {
	for _, platform := range []string{capability.PlatformKimi, capability.PlatformZhipu, capability.PlatformDeepseek} {
		for _, scope := range []string{"channel"} {
			t.Run(platform+"/"+scope, func(t *testing.T) {
				model := "claude-sonnet-4"
				card := routing.ModelPricingEntry{Models: []string{model}, FastMultiplier: testPtrFloat64(2)}
				group := &routing.Group{ID: 100}
				pricingConfigCards := []routing.ModelPricingEntry{card}

				resolver := billingtestkit.ResolverWithCards(t, newCalculator(nil), pricingConfigCards)
				svc := completion.NewRecorder(completion.Dependencies{Prices: resolver}, completion.RecorderOptions{DefaultMultiplier: 1})

				key := &apikey.APIKey{Group: group}
				require.NotNil(t, svc.ResolveConfigPricing(context.Background(), model, gatewaycapture.ProjectCompletionKey(key)))
				require.Empty(t, svc.FilterCNProviderBillingModelCandidates(context.Background(), gatewaycapture.ProjectCompletionProvider(gatewaycapture.ExecutionCompletionRecord(&gatewaycapture.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: platform}})), gatewaycapture.ProjectCompletionKey(key), []string{model}))
				// An explicit zero price is an administrator-provided price, so the candidate remains billable.
				resolver = billingtestkit.SharedPriceResolver(newCalculator(nil), group.ID, pricing.DefaultBillingSettings(), []routing.ModelPricingEntry{{Models: []string{model}, InputPrice: testPtrFloat64(0)}})
				svc = completion.NewRecorder(completion.Dependencies{Calculator: newCalculator(nil), Prices: resolver}, completion.RecorderOptions{DefaultMultiplier: 1})
				require.Equal(t, []string{model}, svc.FilterCNProviderBillingModelCandidates(context.Background(), gatewaycapture.ProjectCompletionProvider(gatewaycapture.ExecutionCompletionRecord(&gatewaycapture.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: platform}})), gatewaycapture.ProjectCompletionKey(key), []string{model}))
			})
		}
	}
}
