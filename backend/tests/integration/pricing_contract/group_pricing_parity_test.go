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

// A recording fixture can expose configured settings and cards for one
// selected group while keeping a base resolver for other lookups. Matching
// settings are cloned, and an unmatched card can still use that resolver's
// configuration. The helper makes group selection and card fallback visible
// while production billing calculates interval amounts, service-tier factors,
// and user funding.
//
// Free Fast requires the marketplace and user allocation to use Standard
// amounts even when the retained request cost uses Fast rates. The assertions
// inspect those distinct amounts, cache-TTL inheritance, and quote results
// using the same configured cards and selected billing instants.

// configuredPrices supplies one group's settings and cards with
// optional base-resolver lookup.
type configuredPrices struct {
	// base receives settings queries for other groups and unmatched card
	// lookups. A nil base supplies default settings and leaves
	// unmatched cards unavailable.
	base *billing.PriceResolver // underlying price resolver
	// groupID selects settings and cards as this fixture's inputs. Other group
	// lookups, and models lacking a local effective card,
	// continue through base.
	groupID int64 // configured override group identifier
	// settings is retained by value with shared optional price pointers. Clone
	// detaches those pointers on each read, so callers keep the supplied
	// amounts stable during lookups.
	settings pricing.BillingSettings // configured group billing settings
	// cards is retained by the fixture; callers leave its slice and nested
	// values unchanged while lookups are running.
	cards []routing.ModelPricingEntry // ordered shared model cards
}

// TestGroupPricingFreeFastWithIntervalsAndTurnTime records priority usage in a
// configured interval and discounted UTC period. It checks Free Fast user
// allocation, retained Fast total cost, public interval prices, and the
// resolver's Fast factor.
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
			// Model and funding USD totals allow rounding in
			// combined pricing factors.
			require.InDelta(t, standardTotal*3, usageRepo.LastLog.TotalCost, 1e-12)
			require.InDelta(t, wantBase*0.5, usageRepo.LastLog.ActualCost, 1e-12)
			cmd := requireOpenAIRecordUsageBillingRepoStub(t, svc).LastCmd
			// Model and funding USD totals allow rounding in
			// combined pricing factors.
			require.InDelta(t, wantBase, cmd.BaseAmountUSD, 1e-12)
			require.InDelta(t, wantBase*0.5, cmd.BillableAmountUSD, 1e-12)
			// Public quotes use the user's Free Fast view. Resolve the card
			// again afterward to check that quoting leaves its
			// Fast multiplier available.
			market := newPricingMarketplaceFixture(nil, nil, svc.Dependencies.Prices, svc.Dependencies.Calculator, nil, nil, nil)
			display := market.PublicModelPricing(context.Background(), group, "gpt-5.6-sol")
			require.Len(t, display.ContextIntervals, 2)
			ratio := 3.0
			if free {
				ratio = 1
			}
			// Displayed USD-per-token rates use an absolute tolerance
			// for multiplier rounding.
			require.InDelta(t, display.ContextIntervals[1].InputPricePerToken*ratio, display.ContextIntervals[1].FastInputPricePerToken, 1e-12)
			resolved := svc.Dependencies.Prices.Resolve(context.Background(), billing.PricingInput{Model: "gpt-5.6-sol", GroupID: &group.ID})
			require.Equal(t, 3.0, *resolved.BasePricing.FastMultiplier)
		})
	}
}

// TestConfigPricingFreeFastDisplayRespectsModelSupport quotes a custom model
// with absent or explicitly zero Fast-mode metadata. It checks Standard
// display pricing and whether Free Fast exposes a Fast price for each fixture.
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

// TestConfiguredIntervalsPreserveDefaultPrices supplies manual defaults with
// partial interval overrides and split cache-write usage. It checks inherited
// token buckets, zero output pricing, and PriceMultiplier/Fast/group-rate
// scaling at selected contexts.
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
				// Interval selection uses input plus cache creation and cache
				// reads. The expected dollar totals therefore account for the
				// extra forty cached tokens in each row.
				for _, tc := range []struct {
					input int // uncached input token count
					// unscaled expected bucket total USD
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
						// Model and funding USD totals allow rounding in
						// combined pricing factors.
						require.InDelta(t, expected, cost.TotalCost, 1e-10, "input=%d tier=%s", tc.input, tier)
						require.InDelta(t, expected*0.7, cost.ActualCost, 1e-10)
					}
				}
			})
		}
	}
}

// TestFreeFastIntervalOnlyDisplayMatchesStandard quotes one or two custom
// intervals with optional Free Fast. It checks displayed input/cache ratios
// and confirms that a subsequent priority calculation retains the configured
// Fast base amount.
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
						// Displayed USD-per-token rates use an absolute
						// tolerance for multiplier rounding.
						require.InDelta(t, 0.001*group.RateMultiplier, display.InputPricePerToken, 1e-12)
						require.InDelta(t, display.InputPricePerToken*ratio, display.FastInputPricePerToken, 1e-12)
						require.InDelta(t, display.CacheReadPricePerToken*ratio, display.FastCacheReadPricePerToken, 1e-12)
					} else {
						require.Len(t, display.ContextIntervals, 2)
						for i, interval := range display.ContextIntervals {
							// Displayed USD-per-token rates use an absolute
							// tolerance for multiplier rounding.
							require.InDelta(t, float64(i+1)*0.001*group.RateMultiplier, interval.InputPricePerToken, 1e-12)
							require.InDelta(t, interval.InputPricePerToken*ratio, interval.FastInputPricePerToken, 1e-12)
							require.InDelta(t, interval.CacheReadPricePerToken*ratio, interval.FastCacheReadPricePerToken, 1e-12)
						}
					}
					// Calculate after quoting so the public Free Fast view
					// cannot conceal an accidental mutation of the
					// card's priority-cost multiplier.
					cost, err := rCalculator.CalculateCostUnified(billing.CostInput{
						Ctx: context.Background(), Model: "custom-priced", GroupID: &group.ID,
						Tokens: pricing.UsageTokens{InputTokens: 50}, RateMultiplier: group.RateMultiplier, ServiceTier: "priority", Resolver: r,
					})
					require.NoError(t, err)
					// Model and funding USD totals allow rounding in
					// combined pricing factors.
					require.InDelta(t, 50*0.001*3, cost.TotalCost, 1e-12)
				})
			}
		}
	}
}

// TestTimeOnlyPricingGroupPricingConfigParity gives the shared card a daily
// multiplier with no base price. It checks the inherited Claude fixture input
// amount inside the configured UTC period.
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

	// Model and funding USD totals allow rounding in combined pricing factors.
	require.InDelta(t, 0.0006, costs[0], 1e-12)
}

// TestTierOnlyPricingPreservesImagePricesEqually adds a Fast factor to
// inherited text and image-token prices. It checks the combined user amount
// for the supplied shared-card priority request.
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

	// Model and funding USD totals allow rounding in combined pricing factors.
	require.InDelta(t, 1.2, costs[0], 1e-12)
}

// TestQoderGroupPricingConfigBlankPricesParity supplies a shared card
// overriding only the Claude model's input price. It checks that output-only
// usage still receives the historical catalog output amount.
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

	// Model and funding USD totals allow rounding in combined pricing factors.
	require.InDelta(t, 0.0025, costs[0], 1e-12)
}

// TestModifierCardsPreserveBuiltinPricingPolicy resolves a historical DeepSeek
// entry with card-provided Fast and time factors. It checks retained catalog
// source and token totals inside and outside the configured UTC period.
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
					// Time multiplier from the configured card.
					expected *= 2
				}
				// Model and funding USD totals allow rounding in
				// combined pricing factors.
				require.InDelta(t, expected, cost.TotalCost, 1e-12)
			}
		})
	}
}

// TestQoderPricingMatchesOtherPlatforms constructs Qoder and OpenAI recorder
// paths for default, modified, and free cards. It compares their public quotes
// and checks the Qoder cost for selected priced/free cases.
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
					if model == "gemini-2.5-flash-image" {
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

// requireOpenAIRecordUsageBillingRepoStub returns the recording fixture's
// settlement-store test double. It fails the calling test when the bound Funds
// dependency has a different implementation, so callers can inspect
// its captured command.
func requireOpenAIRecordUsageBillingRepoStub(t *testing.T, svc *testkit.Recording) *testkit.SettlementStore {
	t.Helper()

	billingRepo, ok := svc.Dependencies.Funds.(*testkit.SettlementStore)
	require.True(t, ok)
	return billingRepo
}

// configureBillingGroup binds group-specific settings and cards into a nonnil
// recording fixture's price resolver. Callers provide a nonnil group, which is
// returned unchanged. The card slice and optional settings amounts remain
// shared, so callers keep them stable during lookups.
func configureBillingGroup(svc *testkit.Recording, group *routing.Group, settings pricing.BillingSettings, cards []routing.ModelPricingEntry) *routing.Group {
	source := configuredPrices{base: svc.Dependencies.Prices, groupID: group.ID, settings: settings, cards: cards}
	svc.Dependencies.Prices = billing.NewPriceResolver(source, svc.Dependencies.Calculator, nil, nil)
	return group
}

// GetEffectiveBillingSettings returns cloned settings for the configured group
// and delegates other identifiers to the base resolver. A nil base is accepted
// by BillingSettings and yields unified defaults for those other groups.
func (s configuredPrices) GetEffectiveBillingSettings(ctx context.Context, id int64) pricing.BillingSettings {
	if id == s.groupID {
		return s.settings.Clone()
	}
	// Other group identifiers follow the base resolver's BillingSettings path.
	// Its nil-safe defaults keep the matching group's fixture settings
	// confined to that selected group.
	return s.base.BillingSettings(ctx, &id)
}

// GetEffectiveConfigModelPricing selects an effective card for the configured
// group before consulting the base resolver. A matching group alone does not
// select a blank or unmatched card: MatchPriceCard applies effective-price and
// model-pattern rules, and returns a clone of a selected card.
//
// If no local card matches, a nonnil base receives the same group identifier
// and model name through normalized lookup. A nil base then returns nil,
// allowing the caller's price resolver to continue its ordinary unavailable
// or catalog handling.
func (s configuredPrices) GetEffectiveConfigModelPricing(ctx context.Context, id int64, model string) *pricing.ModelPricingEntry {
	if id == s.groupID {
		if card := pricing.MatchPriceCard(s.cards, model); card != nil {
			return card
		}
	}
	// MatchPriceCard ignores unpriced local cards. Continue into the base
	// resolver on a miss so such entries cannot suppress a
	// matching configuration there.
	if s.base != nil {
		return s.base.LookupConfigPricingNormalized(ctx, id, model)
	}
	return nil
}

// testPtrString returns an allocated pointer containing the
// supplied string value.
func testPtrString(v string) *string { return &v }
