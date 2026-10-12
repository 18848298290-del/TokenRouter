package pricingcontract

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	pricingprovider "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

type configuredPrices struct {
	base     *billing.PriceResolver
	groupID  int64
	settings pricing.BillingSettings
	cards    []routing.ModelPricingEntry
}

func TestGroupPricingFreeFastWithIntervalsAndTurnTime(t *testing.T) {
	for _, free := range []bool{false, true} {
		t.Run(fmt.Sprint(free), func(t *testing.T) {
			usageRepo := &testkit.UsageLogStore{Inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &testkit.UserStore{}, &testkit.SubscriptionStore{}, nil)
			svc.Dependencies.Prices = billingtestkit.PriceResolver(nil, svc.Dependencies.Calculator)
			groupID := int64(88)
			tier := "priority"
			group := configureBillingGroup(svc, &routing.Group{
				ID:             groupID,
				Hydrated:       true,
				Status:         billing.StatusActive,
				RateMultiplier: 0.5,
			}, pricing.BillingSettings{
				FreeOpenAIFast:               free,
				LongContextPricingEnabled:    true,
				PeakRateMultiplier:           1,
				BatchImageDiscountMultiplier: 0.5,
				BatchImageHoldMultiplier:     0.6,
			}, []routing.ModelPricingEntry{{
				Models: []string{"gpt-5.6-sol"}, BillingMode: routing.BillingModeToken,
				FastMultiplier: testPtrFloat64(3), MaxReasoningEffortMultiplier: testPtrFloat64(2),
				Intervals: []routing.PricingInterval{
					{MinTokens: 0, MaxTokens: testPtrInt(50), InputPrice: testPtrFloat64(0.001), OutputPrice: testPtrFloat64(0)},
					{MinTokens: 50, InputPrice: testPtrFloat64(0.002), OutputPrice: testPtrFloat64(0)},
				},
				TimePricing: &routing.TimePricingConfig{Timezone: "UTC", Periods: []routing.TimePricingPeriod{{StartTime: "01:00", EndTime: "02:00", Multiplier: 0.5}}},
			}})
			err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
				Result: &forwardcore.OpenAIResult{
					RequestID: "resp_group_pricing", Model: "gpt-5.6-sol", ServiceTier: &tier,
					Usage: openai.ForwardUsage{InputTokens: 100}, Duration: time.Second,
				},
				APIKey: &apikey.APIKey{ID: 1020, GroupID: &groupID, Group: group}, User: &identity.User{ID: 2020},
				Provider:  gatewaycapture.ExecutionCompletionRecord(&gatewaycapture.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 3020, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}),
				PricingAt: time.Date(2026, 9, 9, 1, 30, 0, 0, time.UTC),
			})
			require.NoError(t, err)
			standardTotal := 100 * 0.002 * 0.5
			wantBase := standardTotal * 3
			if free {
				wantBase = standardTotal
			}
			require.InDelta(t, standardTotal*3, usageRepo.LastLog.TotalCost, 1e-12)
			require.InDelta(t, wantBase*0.5, usageRepo.LastLog.ActualCost, 1e-12)
			cmd := requireOpenAIRecordUsageBillingRepoStub(t, svc).LastCmd
			require.InDelta(t, wantBase, cmd.BaseAmountUSD, 1e-12)
			require.InDelta(t, wantBase*0.5, cmd.BillableAmountUSD, 1e-12)
			// Free Fast displays Standard prices while provider settlement retains Fast prices.
			market := newPricingMarketplaceFixture(nil, nil, svc.Dependencies.Prices, svc.Dependencies.Calculator, nil, nil, nil)
			display := market.PublicModelPricing(context.Background(), group, "gpt-5.6-sol")
			require.Len(t, display.ContextIntervals, 2)
			ratio := 3.0
			if free {
				ratio = 1
			}
			require.InDelta(t, display.ContextIntervals[1].InputPricePerToken*ratio, display.ContextIntervals[1].FastInputPricePerToken, 1e-12)
			resolved := svc.Dependencies.Prices.Resolve(context.Background(), billing.PricingInput{Model: "gpt-5.6-sol", GroupID: &group.ID})
			require.Equal(t, 3.0, *resolved.BasePricing.FastMultiplier)
		})
	}
}

// TestConfigPricingFreeFastDisplayRespectsModelSupport checks display prices for the model's supported service tiers.
func TestConfigPricingFreeFastDisplayRespectsModelSupport(t *testing.T) {
	bs := billingtestkit.ResolverCalculator()
	settings := pricing.DefaultBillingSettings()
	settings.FreeOpenAIFast = true
	group := &routing.Group{ID: 100, RateMultiplier: 1}
	for _, factor := range []*float64{nil, testPtrFloat64(0)} {
		r := billingtestkit.SharedPriceResolver(bs, group.ID, settings, []routing.ModelPricingEntry{{Models: []string{"embedding-parity"}, InputPrice: testPtrFloat64(0.02), FastModeMultiplier: factor}})
		svc := newPricingMarketplaceFixture(nil, nil, r, bs, nil, nil, nil)
		display := svc.PublicModelPricing(context.Background(), group, "embedding-parity")
		require.Equal(t, 0.02, display.InputPricePerToken)
		if factor == nil {
			require.Zero(t, display.FastInputPricePerToken)
		} else {
			require.Equal(t, 0.02, display.FastInputPricePerToken)
		}
	}
}

func TestConfiguredIntervalsPreserveDefaultPrices(t *testing.T) {
	for _, source := range []string{"channel"} {
		for _, model := range []string{"claude-sonnet-4", "custom-priced"} {
			t.Run(source+"/"+model, func(t *testing.T) {
				card := routing.ModelPricingEntry{
					Models: []string{model}, BillingMode: routing.BillingModeToken,
					InputPrice: testPtrFloat64(0.001), OutputPrice: testPtrFloat64(0.002),
					CacheWritePrice: testPtrFloat64(0.003), CacheWrite1hPrice: testPtrFloat64(0.004), CacheReadPrice: testPtrFloat64(0.005),
					PriceMultiplier: testPtrFloat64(1.2), FastMultiplier: testPtrFloat64(1.5),
					Intervals: []routing.PricingInterval{
						{MinTokens: 100, MaxTokens: testPtrInt(200), InputMultiplier: testPtrFloat64(2), OutputPrice: testPtrFloat64(0), CacheWriteMultiplier: testPtrFloat64(2), CacheReadPrice: testPtrFloat64(0.001)},
						{MinTokens: 200, InputPrice: testPtrFloat64(0.008)},
					},
				}
				err := (routing.PricingConfigValidation{LoadLocation: pricingprovider.LoadPricingLocation}).PricingEntries([]routing.ModelPricingEntry{card})
				require.NoError(t, err)
				rCalculator := billingtestkit.ResolverCalculator()
				r := billingtestkit.ResolverWithCards(t, rCalculator, nil)
				group := &routing.Group{ID: 100}
				if source == "channel" {
					rCalculator = billingtestkit.ResolverCalculator()
					r = billingtestkit.ResolverWithCards(t, rCalculator, []routing.ModelPricingEntry{card})
				}
				for _, tc := range []struct {
					input    int
					standard float64
				}{{10, 0.19}, {110, 0.38}, {210, 1.86}} {
					for _, tier := range []string{"default", "priority"} {
						cost, err := rCalculator.CalculateCostUnified(billing.CostInput{
							Ctx: context.Background(), Model: model, GroupID: &group.ID,
							Tokens:         pricing.UsageTokens{InputTokens: tc.input, OutputTokens: 5, CacheCreationTokens: 20, CacheCreation5mTokens: 10, CacheCreation1hTokens: 10, CacheReadTokens: 20},
							RateMultiplier: 0.7, ServiceTier: tier, Resolver: r,
						})
						require.NoError(t, err)
						expected := tc.standard * 1.2
						if tier == "priority" {
							expected *= 1.5
						}
						require.InDelta(t, expected, cost.TotalCost, 1e-10, "input=%d tier=%s", tc.input, tier)
						require.InDelta(t, expected*0.7, cost.ActualCost, 1e-10)
					}
				}
			})
		}
	}
}

// TestFreeFastIntervalOnlyDisplayMatchesStandard checks that Free Fast displays Standard interval prices for custom models.
func TestFreeFastIntervalOnlyDisplayMatchesStandard(t *testing.T) {
	for _, source := range []string{"channel"} {
		for _, tierCount := range []int{1, 2} {
			for _, free := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/free=%v", source, tierCount, free), func(t *testing.T) {
					intervals := []routing.PricingInterval{{MinTokens: 0, InputPrice: testPtrFloat64(0.001), CacheReadPrice: testPtrFloat64(0.0001)}}
					if tierCount == 2 {
						intervals[0].MaxTokens = testPtrInt(100)
						intervals = append(intervals, routing.PricingInterval{MinTokens: 100, InputPrice: testPtrFloat64(0.002), CacheReadPrice: testPtrFloat64(0.0002)})
					}
					card := routing.ModelPricingEntry{
						Models: []string{"custom-priced"}, BillingMode: routing.BillingModeToken,
						FastMultiplier: testPtrFloat64(3), Intervals: intervals,
					}
					err := (routing.PricingConfigValidation{LoadLocation: pricingprovider.LoadPricingLocation}).PricingEntries([]routing.ModelPricingEntry{card})
					require.NoError(t, err)
					group := &routing.Group{
						ID:             100,
						RateMultiplier: 0.5,
					}
					rCalculator := billingtestkit.ResolverCalculator()
					settings := pricing.DefaultBillingSettings()
					settings.FreeOpenAIFast = free
					r := billingtestkit.SharedPriceResolver(rCalculator, group.ID, settings, []routing.ModelPricingEntry{card})
					svc := newPricingMarketplaceFixture(nil, nil, r, rCalculator, nil, nil, nil)
					display := svc.PublicModelPricing(context.Background(), group, "custom-priced")
					require.Equal(t, "priced", display.PriceStatus)
					ratio := 3.0
					if free {
						ratio = 1
					}
					if tierCount == 1 {
						require.Empty(t, display.ContextIntervals)
						require.InDelta(t, 0.001*group.RateMultiplier, display.InputPricePerToken, 1e-12)
						require.InDelta(t, display.InputPricePerToken*ratio, display.FastInputPricePerToken, 1e-12)
						require.InDelta(t, display.CacheReadPricePerToken*ratio, display.FastCacheReadPricePerToken, 1e-12)
					} else {
						require.Len(t, display.ContextIntervals, 2)
						for i, interval := range display.ContextIntervals {
							require.InDelta(t, float64(i+1)*0.001*group.RateMultiplier, interval.InputPricePerToken, 1e-12)
							require.InDelta(t, interval.InputPricePerToken*ratio, interval.FastInputPricePerToken, 1e-12)
							require.InDelta(t, interval.CacheReadPricePerToken*ratio, interval.FastCacheReadPricePerToken, 1e-12)
						}
					}
					// Quoting display prices must leave the Fast settlement cost intact.
					cost, err := rCalculator.CalculateCostUnified(billing.CostInput{
						Ctx: context.Background(), Model: "custom-priced", GroupID: &group.ID,
						Tokens: pricing.UsageTokens{InputTokens: 50}, RateMultiplier: group.RateMultiplier, ServiceTier: "priority", Resolver: r,
					})
					require.NoError(t, err)
					require.InDelta(t, 50*0.001*3, cost.TotalCost, 1e-12)
				})
			}
		}
	}
}

// TestTimeOnlyPricingGroupPricingConfigParity checks a shared card that supplies only time-based multipliers.
func TestTimeOnlyPricingGroupPricingConfigParity(t *testing.T) {
	card := routing.ModelPricingEntry{
		Models: []string{"claude-sonnet-4"}, BillingMode: routing.BillingModeToken,
		TimePricing: &routing.TimePricingConfig{Timezone: "UTC", Periods: []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "10:00", Multiplier: 2}}},
	}
	require.NoError(t, (routing.PricingConfigValidation{LoadLocation: pricingprovider.LoadPricingLocation}).PricingEntries([]routing.ModelPricingEntry{card}))
	var costs []float64
	for _, source := range []string{"channel"} {
		group := &routing.Group{ID: 100}
		rCalculator := billingtestkit.ResolverCalculator()
		r := billingtestkit.ResolverWithCards(t, rCalculator, nil)
		if source == "channel" {
			rCalculator = billingtestkit.ResolverCalculator()
			r = billingtestkit.ResolverWithCards(t, rCalculator, []routing.ModelPricingEntry{card})
		}
		cost, err := rCalculator.CalculateCostUnified(billing.CostInput{
			Ctx: context.Background(), Model: "claude-sonnet-4", GroupID: &group.ID,
			Tokens: pricing.UsageTokens{InputTokens: 100}, RateMultiplier: 1, PricingAt: time.Date(2026, 9, 9, 9, 30, 0, 0, time.UTC), Resolver: r,
		})
		require.NoError(t, err)
		costs = append(costs, cost.ActualCost)
	}

	require.InDelta(t, 0.0006, costs[0], 1e-12)
}

// TestTierOnlyPricingPreservesImagePricesEqually checks that Fast multipliers retain image input and output prices.
func TestTierOnlyPricingPreservesImagePricesEqually(t *testing.T) {
	card := routing.ModelPricingEntry{Models: []string{"claude-sonnet-4"}, BillingMode: routing.BillingModeToken, FastMultiplier: testPtrFloat64(2)}
	var costs []float64
	for _, source := range []string{"channel"} {
		prices := billingtestkit.ResolverFallbackPrices()
		prices["claude-sonnet-4"].InputPricePerToken = 0.001
		prices["claude-sonnet-4"].ImageInputPricePerToken = 0.003
		prices["claude-sonnet-4"].ImageOutputPricePerToken = 0.004
		bs := newCalculatorWithPrices(nil, prices)
		group := &routing.Group{ID: 100}
		r := billingtestkit.ResolverWithCards(t, bs, nil)
		if source == "channel" {
			r = billingtestkit.ResolverWithCards(t, bs, []routing.ModelPricingEntry{card})
		}
		cost, err := bs.CalculateCostUnified(billing.CostInput{
			Ctx: context.Background(), Model: "claude-sonnet-4", GroupID: &group.ID,
			Tokens: pricing.UsageTokens{InputTokens: 200, ImageInputTokens: 100, OutputTokens: 50, ImageOutputTokens: 50}, RateMultiplier: 1, ServiceTier: "priority", Resolver: r,
		})
		require.NoError(t, err)
		costs = append(costs, cost.ActualCost)
	}

	require.InDelta(t, 1.2, costs[0], 1e-12)
}

func TestQoderGroupPricingConfigBlankPricesParity(t *testing.T) {
	card := routing.ModelPricingEntry{Models: []string{"claude-opus-4-6"}, BillingMode: routing.BillingModeToken, InputPrice: testPtrFloat64(0.001)}
	var costs []float64
	for _, source := range []string{"channel"} {
		bs := newCalculator(nil)
		group := &routing.Group{ID: 100}
		r := billingtestkit.ResolverWithCards(t, bs, nil)
		if source == "channel" {
			r = billingtestkit.ResolverWithCards(t, bs, []routing.ModelPricingEntry{card})
		}
		gateway := completion.NewRecorder(completion.Dependencies{Prices: r, Calculator: bs}, completion.RecorderOptions{DefaultMultiplier: 1})

		resolved, model := gateway.ResolveConfigPricing(context.Background(), "claude-opus-4-6", gatewaycapture.ProjectCompletionKey(&apikey.APIKey{Group: group, GroupID: &group.ID})), "claude-opus-4-6"
		require.NotNil(t, resolved)
		cost, err := bs.CalculateCostUnified(billing.CostInput{Ctx: context.Background(), Model: model, GroupID: &group.ID, Tokens: pricing.UsageTokens{OutputTokens: 100}, RateMultiplier: 1, Resolver: r, Resolved: resolved})
		require.NoError(t, err)
		costs = append(costs, cost.ActualCost)
	}

	require.InDelta(t, 0.0025, costs[0], 1e-12)
}

// TestModifierCardsPreserveBuiltinPricingPolicy checks catalog prices with the card's time-based multipliers.
func TestModifierCardsPreserveBuiltinPricingPolicy(t *testing.T) {
	model := "deepseek-v4-flash"
	card := routing.ModelPricingEntry{
		Models: []string{model}, FastMultiplier: testPtrFloat64(3),
		TimePricing: &routing.TimePricingConfig{Timezone: "UTC", Periods: []routing.TimePricingPeriod{{StartTime: "01:00", EndTime: "04:00", Multiplier: 2}}},
	}
	for _, scope := range []string{"channel"} {
		t.Run(scope, func(t *testing.T) {
			bs := newCalculator(nil)
			group := &routing.Group{ID: 100}
			pricingConfigCards := []routing.ModelPricingEntry{card}

			r := billingtestkit.ResolverWithCards(t, bs, pricingConfigCards)
			for _, hour := range []int{0, 1, 3, 4} {
				at := time.Date(2026, 9, 9, hour, 0, 0, 0, time.UTC)
				resolved := r.Resolve(context.Background(), billing.PricingInput{Model: model, GroupID: &group.ID})
				require.Equal(t, pricing.PricingSourceCatalog, resolved.Source)
				cost, err := bs.CalculateCostUnified(billing.CostInput{
					Model: model, GroupID: &group.ID, Resolver: r,
					Tokens: pricing.UsageTokens{InputTokens: 100}, RateMultiplier: 1, PricingAt: at, ServiceTier: "priority",
				})
				require.NoError(t, err)
				expected := 100 * 2.2e-7 * 3

				if hour >= 1 && hour < 4 {
					expected *= 2 // Time multiplier from the configured card.
				}
				require.InDelta(t, expected, cost.TotalCost, 1e-12)
			}
		})
	}
}

// TestQoderPricingMatchesOtherPlatforms compares tier, time-based, zero, and default image prices through shared billing and display entry points.
func TestQoderPricingMatchesOtherPlatforms(t *testing.T) {
	for _, model := range []string{"claude-opus-4-6", "gpt-image-1", "custom-image", "qmodel"} {
		for _, kind := range []string{"default", "modifiers", "free"} {
			t.Run(model+"/"+kind, func(t *testing.T) {
				var prices []pricing.ModelDisplayPricing
				var costs []*pricing.CostBreakdown
				for _, platform := range []string{capability.PlatformQoder, capability.PlatformOpenAI} {
					group := &routing.Group{ID: 100, RateMultiplier: 1}
					card := routing.ModelPricingEntry{Models: []string{model}}
					switch kind {
					case "modifiers":
						card.FastMultiplier = testPtrFloat64(2)
						card.TimePricing = &routing.TimePricingConfig{Timezone: "UTC", Periods: []routing.TimePricingPeriod{{StartTime: "00:00", EndTime: "12:00", Multiplier: 2}}}
					case "free":
						card.InputPrice, card.OutputPrice = testPtrFloat64(0), testPtrFloat64(0)
					}
					bs := newCalculator(nil)
					r := billingtestkit.ResolverWithCards(t, bs, []routing.ModelPricingEntry{card})
					gateway := completion.NewRecorder(completion.Dependencies{Prices: r, Calculator: bs}, completion.RecorderOptions{DefaultMultiplier: 1})

					market := newPricingMarketplaceFixture(nil, nil, r, bs, nil, nil, nil)
					prices = append(prices, market.RequestableModelPricing(context.Background(), group, routing.MarketplaceModelDef{ID: model, PricingModel: model}))
					result := &forwardcore.MessagesResult{Usage: upstream.TokenUsage{InputTokens: 100, OutputTokens: 10}, ServiceTier: testPtrString("priority")}
					if pricing.LooksLikeImageModel(model) {
						result.ImageCount = 1
					}
					costs = append(costs, gateway.CalculateRecordUsageCost(context.Background(), gatewaycapture.ProjectMessagesCompletionResult(result, gatewaycapture.ExecutionCompletionRecord(&gatewaycapture.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: platform}})), gatewaycapture.ProjectCompletionKey(&apikey.APIKey{Group: group}), gatewaycapture.ProjectCompletionProvider(gatewaycapture.ExecutionCompletionRecord(&gatewaycapture.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: platform}})), model, model, routing.BillingModelSourceRequested, model, 1, 1, &completion.PricingOptions{PricingAt: time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC)}))
				}
				require.Equal(t, prices[0], prices[1])

				if model == "claude-opus-4-6" && kind != "free" {
					require.Positive(t, costs[0].TotalCost)
					require.Equal(t, "priced", prices[0].PriceStatus)
				}
				if kind == "free" {
					require.Zero(t, costs[0].TotalCost)
					require.Equal(t, "priced", prices[0].PriceStatus)
				}
			})
		}
	}
}

func requireOpenAIRecordUsageBillingRepoStub(t *testing.T, svc *testkit.Recording) *testkit.SettlementStore {
	t.Helper()

	billingRepo, ok := svc.Dependencies.Funds.(*testkit.SettlementStore)
	require.True(t, ok)
	return billingRepo
}

// configureBillingGroup supplies billing settings and shared price cards to the recording fixture.
func configureBillingGroup(svc *testkit.Recording, group *routing.Group, settings pricing.BillingSettings, cards []routing.ModelPricingEntry) *routing.Group {
	source := configuredPrices{base: svc.Dependencies.Prices, groupID: group.ID, settings: settings, cards: cards}
	svc.Dependencies.Prices = billing.NewPriceResolver(source, svc.Dependencies.Calculator, nil, nil)
	return group
}

func (s configuredPrices) GetEffectiveBillingSettings(ctx context.Context, id int64) pricing.BillingSettings {
	if id == s.groupID {
		return s.settings.Clone()
	}
	return s.base.BillingSettings(ctx, &id)
}

func (s configuredPrices) GetEffectiveConfigModelPricing(ctx context.Context, id int64, model string) *pricing.ModelPricingEntry {
	if id == s.groupID {
		if card := pricing.MatchPriceCard(s.cards, model); card != nil {
			return card
		}
	}
	if s.base != nil {
		return s.base.LookupConfigPricingNormalized(ctx, id, model)
	}
	return nil
}

// testPtrString returns a pointer to v.
func testPtrString(v string) *string { return &v }
