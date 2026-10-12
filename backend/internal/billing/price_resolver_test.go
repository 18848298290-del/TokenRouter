package billing_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/billing/provider"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	catalogprovider "github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// TestResolveCatalogAliasesPreserveConfigPricing checks that an unpriced alias can have an independent zero-price card.
func TestResolveCatalogAliasesPreserveConfigPricing(t *testing.T) {
	previous := xai.RuntimeDefaultTextModel()
	t.Cleanup(func() { xai.SetRuntimeDefaultTextModel(previous) })
	xai.SetRuntimeDefaultTextModel("X-AI/GROK-4.6")
	for _, tc := range []struct{ platform, base, alias string }{
		{capability.PlatformGemini, "gemini-3.8-flash", "gemini-3.8-flash-tiered"},
		{capability.PlatformGemini, "gemini-3.7-flash", "models/gemini-3.7-flash-medium"},
		{capability.PlatformGrok, "grok-4.6", "grok-4.6-latest"},
		{capability.PlatformGrok, "grok-4.6", "grok-latest"},
		{capability.PlatformOpenAI, "gpt-5.6-luna", "gpt-5.6-luna-high"},
		{capability.PlatformAnthropic, "claude-opus-4-6", "claude-opus-4.6"},
	} {
		t.Run(tc.alias, func(t *testing.T) {
			for _, hasCatalog := range []bool{true, false} {
				groupID := int64(998)
				pricingConfigPrice := 9e-6
				configPricing := routingtestkit.Configuration{ID: 998, Status: billing.StatusActive, GroupIDs: []int64{groupID}, ModelPricing: []routing.ModelPricingEntry{{
					Models: []string{tc.base}, BillingMode: routing.BillingModeToken, InputPrice: &pricingConfigPrice,
				}}}
				repository := &routingtestkit.ConfigRows{Values: []routingtestkit.Configuration{configPricing}, Platforms: map[int64]string{groupID: tc.platform}}
				pricingConfigs := routingtestkit.NewPricingConfigService(repository, nil, routing.PricingConfigOptions{Now: time.Now, LoadLocation: provider.LoadPricingLocation})
				var catalog *catalogprovider.Service
				if hasCatalog {
					catalog = newCatalogFixture(catalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{
						tc.base: {Mode: "chat", InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
					}})
				}
				resolver := billingtestkit.PriceResolver(pricingConfigs, newCalculator(catalog))
				input := billing.PricingInput{Model: tc.alias, GroupID: &groupID}
				resolved := resolver.Resolve(context.Background(), input)
				require.True(t, resolved.IsUnpriced(), "catalog=%v", hasCatalog)
				// Distinct full model names can have independent cards.
				if billingpricing.NormalizePriceModelName(tc.base) == billingpricing.NormalizePriceModelName(tc.alias) {
					continue
				}

				// An explicit zero price on the full request name takes precedence over the base name's price.
				zero := 0.0
				configPricing.ModelPricing = append(configPricing.ModelPricing, routing.ModelPricingEntry{
					Models: []string{tc.alias}, BillingMode: routing.BillingModeToken, InputPrice: &zero,
				})
				repository.Values = []routingtestkit.Configuration{configPricing}
				pricingConfigs.InvalidateCache()
				resolved = resolver.Resolve(context.Background(), input)
				require.True(t, resolved.HasEffectivePricing())
				require.Zero(t, resolved.BasePricing.InputPricePerToken)
				base := resolver.Resolve(context.Background(), billing.PricingInput{Model: tc.base, GroupID: &groupID})
				require.InDelta(t, pricingConfigPrice, base.BasePricing.InputPricePerToken, 1e-12)
			}
		})
	}
}

// TestResolveCatalogAliasesUseUnifiedPricingConfig checks that base-name prices do not apply to suffixed models.
func TestResolveCatalogAliasesUseUnifiedPricingConfig(t *testing.T) {
	groupID := int64(999)
	price := 9e-6

	pricingConfigs := routingtestkit.ModelConfigFromData(routingtestkit.ModelConfigDataFromRows([]routingtestkit.Configuration{{
		ID: 999, Status: billing.StatusActive, GroupIDs: []int64{groupID}, ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"gemini-3.8-flash"}, BillingMode: routing.BillingModeToken, InputPrice: &price},
			{Models: []string{"gemini-3.7-flash"}, BillingMode: routing.BillingModeToken, InputPrice: &price},
		},
	}}, map[int64]string{groupID: capability.PlatformGemini}))
	catalog := newCatalogFixture(catalogFixture{pricingData: map[string]*billingpricing.CatalogModelPricing{
		"gemini-3.8-flash": {Mode: "chat", InputCostPerToken: 1e-6},
	}})
	resolver := billingtestkit.PriceResolver(pricingConfigs, newCalculator(catalog))
	resolved := resolver.Resolve(context.Background(), billing.PricingInput{Model: "gemini-3.8-flash-tiered", GroupID: &groupID})
	require.True(t, resolved.IsUnpriced())
}

// TestGroupAndPricingCatalogAliasPrecedence checks full-name and wildcard cards and rejects base-name borrowing.
func TestGroupAndPricingCatalogAliasPrecedence(t *testing.T) {
	for _, tc := range []struct{ platform, base, alias string }{
		{capability.PlatformOpenAI, "gpt-5.6-luna", "gpt-5.6-luna-high"},
		{capability.PlatformGemini, "gemini-3.8-flash", "models/gemini-3.8-flash-tiered"},
		{capability.PlatformGrok, "grok-4.6", "grok-4.6-latest"},
		{capability.PlatformAnthropic, "claude-opus-4-6", "claude-opus-4.6"},
	} {
		t.Run(tc.alias, func(t *testing.T) {
			price, zero := 9e-6, 0.0
			card := routing.ModelPricingEntry{Models: []string{tc.base}, InputPrice: &price}
			for range []string{billingpricing.PricingSourceConfig} {
				group := &routing.Group{ID: 990}

				repository := &routingtestkit.ConfigRows{Platforms: map[int64]string{group.ID: tc.platform}}
				pricingConfigs := routingtestkit.NewPricingConfigService(repository, nil, routing.PricingConfigOptions{Now: time.Now, LoadLocation: provider.LoadPricingLocation})
				resolver := billingtestkit.PriceResolver(pricingConfigs, newCalculator(nil))
				resolve := func(cards []routing.ModelPricingEntry) *billingpricing.ResolvedPricing {
					configPricing := routingtestkit.Configuration{ID: 990, Status: billing.StatusActive, GroupIDs: []int64{group.ID}}
					configPricing.ModelPricing = cards
					repository.Values = []routingtestkit.Configuration{configPricing}
					pricingConfigs.InvalidateCache()
					return resolver.Resolve(context.Background(), billing.PricingInput{Model: tc.alias, GroupID: &group.ID})
				}
				base := resolve([]routing.ModelPricingEntry{card})
				require.True(t, base.IsUnpriced())
				if billingpricing.NormalizePriceModelName(tc.base) == billingpricing.NormalizePriceModelName(tc.alias) {
					continue
				}
				exact := routing.ModelPricingEntry{Models: []string{tc.alias}}
				require.True(t, resolve([]routing.ModelPricingEntry{exact, card}).IsUnpriced())
				exact.InputPrice = &zero
				require.Zero(t, resolve([]routing.ModelPricingEntry{card, exact}).BasePricing.InputPricePerToken)
				wildcard := routing.ModelPricingEntry{Models: []string{tc.alias + "*"}, InputPrice: &zero}
				require.Zero(t, resolve([]routing.ModelPricingEntry{card, wildcard}).BasePricing.InputPricePerToken)
			}
		})
	}
}

func TestResolve_NoGroupID(t *testing.T) {
	bs := billingtestkit.ResolverCalculator()
	r := billingtestkit.PriceResolver(routingtestkit.NewPricingConfigService(nil, nil, routing.PricingConfigOptions{
		Warn: slog.
			Warn,
		Now: time.
			Now, LoadLocation: provider.
			LoadPricingLocation,
	}),

		bs)

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: nil,
	})

	require.NotNil(t, resolved)
	require.Equal(t, routing.BillingModeToken, resolved.Mode)
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, 3e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	// ResolveBasePricing labels successful Calculator.GetModelPricing lookups as catalog prices,
	// including prices supplied by the test catalog.
	require.Equal(t, "catalog", resolved.Source)
}

func TestResolve_UnknownModel(t *testing.T) {
	bs := billingtestkit.ResolverCalculator()
	r := billingtestkit.PriceResolver(routingtestkit.NewPricingConfigService(nil, nil, routing.PricingConfigOptions{
		Warn: slog.
			Warn,
		Now: time.
			Now, LoadLocation: provider.
			LoadPricingLocation,
	}),

		bs)

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "unknown-model-xyz",
		GroupID: nil,
	})

	require.NotNil(t, resolved)
	require.Nil(t, resolved.BasePricing)
	// Unknown models remain unpriced.
	require.Equal(t, "unpriced", resolved.Source)
}

func TestGetIntervalPricing_NoIntervals(t *testing.T) {
	basePricing := &billingpricing.ModelPricing{InputPricePerToken: 5e-6}
	resolved := &billingpricing.ResolvedPricing{
		Mode:        routing.BillingModeToken,
		BasePricing: basePricing,
		Intervals:   nil,
	}

	result := billingpricing.GetIntervalPricing(resolved, 50000)
	require.Equal(t, basePricing, result)
}

func TestGetIntervalPricing_MatchesInterval(t *testing.T) {
	resolved := &billingpricing.ResolvedPricing{
		Mode:                   routing.BillingModeToken,
		BasePricing:            &billingpricing.ModelPricing{InputPricePerToken: 5e-6},
		SupportsCacheBreakdown: true,
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(128000), InputPrice: testPtrFloat64(1e-6), OutputPrice: testPtrFloat64(2e-6)},
			{MinTokens: 128000, MaxTokens: nil, InputPrice: testPtrFloat64(3e-6), OutputPrice: testPtrFloat64(6e-6)},
		},
	}

	result := billingpricing.GetIntervalPricing(resolved, 50000)
	require.NotNil(t, result)
	require.InDelta(t, 1e-6, result.InputPricePerToken, 1e-12)
	require.InDelta(t, 2e-6, result.OutputPricePerToken, 1e-12)
	require.True(t, result.SupportsCacheBreakdown)

	result2 := billingpricing.GetIntervalPricing(resolved, 200000)
	require.NotNil(t, result2)
	require.InDelta(t, 3e-6, result2.InputPricePerToken, 1e-12)
}

func TestGetIntervalPricing_NoMatch_FallsBackToBase(t *testing.T) {
	basePricing := &billingpricing.ModelPricing{InputPricePerToken: 99e-6}
	resolved := &billingpricing.ResolvedPricing{
		Mode:        routing.BillingModeToken,
		BasePricing: basePricing,
		Intervals: []routing.PricingInterval{
			{MinTokens: 10000, MaxTokens: testPtrInt(50000), InputPrice: testPtrFloat64(1e-6)},
		},
	}

	result := billingpricing.GetIntervalPricing(resolved, 5000)
	require.Equal(t, basePricing, result)
}

func TestGPT56ExplicitZeroCacheWritePriceIsPreserved(t *testing.T) {
	bs := newCalculatorWithPrices(nil, map[string]*billingpricing.ModelPricing{})
	resolver := billingtestkit.PriceResolver(nil, bs)
	zero := 0.0

	t.Run("flat channel price", func(t *testing.T) {
		resolved := &billingpricing.ResolvedPricing{
			Mode: routing.BillingModeToken,
			BasePricing: &billingpricing.ModelPricing{
				InputPricePerToken:  5e-6,
				OutputPricePerToken: 30e-6,
			},
		}
		billingpricing.ApplyTokenOverrides(&routing.ModelPricingEntry{CacheWritePrice: &zero}, resolved)

		require.True(t, resolved.BasePricing.CacheCreationPriceExplicit)
		cost, err := bs.CalculateCostUnified(billing.CostInput{
			Model:          "gpt-5.6-sol",
			Tokens:         billingpricing.UsageTokens{CacheCreationTokens: 100},
			RateMultiplier: 1,
			Resolver:       resolver,
			Resolved:       resolved,
		})
		require.NoError(t, err)
		require.Zero(t, cost.CacheCreationCost)
	})

	t.Run("interval price", func(t *testing.T) {
		pricing := billingpricing.IntervalToModelPricingWithBase(&routing.PricingInterval{CacheWritePrice: &zero}, false, nil, nil)
		require.True(t, pricing.CacheCreationPriceExplicit)

		cost, err := bs.CalculateCostUnified(billing.CostInput{
			Model:          "gpt-5.6-sol",
			Tokens:         billingpricing.UsageTokens{CacheCreationTokens: 100},
			RateMultiplier: 1,
			Resolver:       resolver,
			Resolved: &billingpricing.ResolvedPricing{
				Mode:        routing.BillingModeToken,
				BasePricing: pricing,
			},
		})
		require.NoError(t, err)
		require.Zero(t, cost.CacheCreationCost)
	})
}

func TestGetRequestTierPrice(t *testing.T) {
	resolved := &billingpricing.ResolvedPricing{
		Mode: routing.BillingModePerRequest,
		RequestTiers: []routing.PricingInterval{
			{TierLabel: "1K", PerRequestPrice: testPtrFloat64(0.04)},
			{TierLabel: "2K", PerRequestPrice: testPtrFloat64(0.08)},
		},
	}

	require.InDelta(t, 0.04, billingpricing.GetRequestTierPrice(resolved, "1K"), 1e-12)
	require.InDelta(t, 0.04, billingpricing.GetRequestTierPrice(resolved, "1k"), 1e-12)
	require.InDelta(t, 0.08, billingpricing.GetRequestTierPrice(resolved, "2K"), 1e-12)
	require.InDelta(t, 0.0, billingpricing.GetRequestTierPrice(resolved, "4K"), 1e-12)

	price, ok := billingpricing.GetRequestTierPriceValue(resolved, "4K")
	require.False(t, ok)
	require.Zero(t, price)
}

func TestGetRequestTierPriceByContext(t *testing.T) {
	resolved := &billingpricing.ResolvedPricing{
		Mode: routing.BillingModePerRequest,
		RequestTiers: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(128000), PerRequestPrice: testPtrFloat64(0.05)},
			{MinTokens: 128000, MaxTokens: nil, PerRequestPrice: testPtrFloat64(0.10)},
		},
	}

	require.InDelta(t, 0.05, billingpricing.GetRequestTierPriceByContext(resolved, 50000), 1e-12)
	require.InDelta(t, 0.10, billingpricing.GetRequestTierPriceByContext(resolved, 200000), 1e-12)

	price, ok := billingpricing.GetRequestTierPriceByContextValue(resolved, 0)
	require.False(t, ok)
	require.Zero(t, price)
}

func TestGetRequestTierPrice_NilPerRequestPrice(t *testing.T) {
	resolved := &billingpricing.ResolvedPricing{
		Mode: routing.BillingModePerRequest,
		RequestTiers: []routing.PricingInterval{
			{TierLabel: "1K", PerRequestPrice: nil},
		},
	}

	require.InDelta(t, 0.0, billingpricing.GetRequestTierPrice(resolved, "1K"), 1e-12)
}

func TestResolve_WithPricingConfigOverride_TokenFlat(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  testPtrFloat64(10e-6),
		OutputPrice: testPtrFloat64(50e-6),
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.NotNil(t, resolved)
	require.Equal(t, routing.BillingModeToken, resolved.Mode)
	require.Equal(t, "pricing_config", resolved.Source)
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, 10e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	// A standard-price override cannot invent a priority price missing from the catalog.
	require.Zero(t, resolved.BasePricing.InputPricePerTokenPriority)
	require.InDelta(t, 50e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
	require.Zero(t, resolved.BasePricing.OutputPricePerTokenPriority)
}

func TestResolve_WithPricingConfigOverride_TokenFlatPreservesNativeTierRatio(t *testing.T) {
	prices := billingtestkit.ResolverFallbackPrices()
	prices["gpt-5.4"] = &billingpricing.ModelPricing{
		InputPricePerToken:             2e-6,
		InputPricePerTokenPriority:     4e-6,
		OutputPricePerToken:            10e-6,
		OutputPricePerTokenPriority:    20e-6,
		CacheReadPricePerToken:         0.5e-6,
		CacheReadPricePerTokenPriority: 1e-6,
	}
	bs := newCalculatorWithPrices(nil, prices)
	r := billingtestkit.ResolverWithCards(t, bs, []routing.ModelPricingEntry{{
		Models:         []string{"gpt-5.4"},
		BillingMode:    routing.BillingModeToken,
		InputPrice:     testPtrFloat64(7e-6),
		OutputPrice:    testPtrFloat64(30e-6),
		CacheReadPrice: testPtrFloat64(2e-6),
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{Model: "gpt-5.4", GroupID: billingtestkit.GroupID()})
	require.NotNil(t, resolved)
	require.InDelta(t, 14e-6, resolved.BasePricing.InputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 60e-6, resolved.BasePricing.OutputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 4e-6, resolved.BasePricing.CacheReadPricePerTokenPriority, 1e-12)
}

func TestResolve_WithPricingConfigOverride_TokenPartialOverride(t *testing.T) {
	// The card overrides InputPrice; OutputPrice comes from the base pricing.
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  testPtrFloat64(20e-6),
		// OutputPrice intentionally nil
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.NotNil(t, resolved)
	require.Equal(t, "pricing_config", resolved.Source)
	require.NotNil(t, resolved.BasePricing)
	// InputPrice overridden by configPricing
	require.InDelta(t, 20e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	// OutputPrice kept from base (fallback: 15e-6)
	require.InDelta(t, 15e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
}

func TestResolve_WithPricingConfigOverride_PriceMultiplierOnlyIsIgnored(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:          []string{"claude-sonnet-4"},
		BillingMode:     routing.BillingModeToken,
		PriceMultiplier: testPtrFloat64(2),
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	// Invalid multiplier-only stored data leaves the default model price unchanged.
	require.NotNil(t, resolved)
	require.Equal(t, billingpricing.PricingSourceCatalog, resolved.Source)
	require.False(t, resolved.HasEffectivePricing())
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, 3e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 15e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
}

func TestResolve_BlankConfigPricingIsIgnoredForNonQoder(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.NotNil(t, resolved)
	require.Equal(t, billingpricing.PricingSourceCatalog, resolved.Source)
	require.False(t, resolved.HasEffectivePricing())
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, 3e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 15e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
	require.False(t, resolved.BasePricing.ImageOutputPriceExplicit)
}

func TestResolve_QoderCustomAliasMappedToRouteKeyZerosMissingPartialConfigPricing(t *testing.T) {
	groupID := int64(100)
	inputPrice := 20e-6
	cache := routingtestkit.NewModelConfigData()
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: 1, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "custom-qoder"}] = &routing.ModelPricingEntry{
		Models:      []string{"custom-qoder"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
	}
	cache.Models[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "custom-qoder"}] = "qmodel"
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)
	billingService := newCalculator(nil)
	r := billingtestkit.PriceResolver(pricingConfigService, billingService)

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "custom-qoder",
		GroupID: &groupID,
	})

	require.NotNil(t, resolved)
	require.Equal(t, billingpricing.PricingSourceConfig, resolved.Source)
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, inputPrice, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.Zero(t, resolved.BasePricing.OutputPricePerToken)
}

func TestResolve_QoderStandardModelMappedToRouteKeyKeepsBaseForPartialConfigPricing(t *testing.T) {
	groupID := int64(100)
	inputPrice := 20e-6
	cache := routingtestkit.NewModelConfigData()
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: 1, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "gpt-5.4"}] = &routing.ModelPricingEntry{
		Models:      []string{"gpt-5.4"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
	}
	cache.Models[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "gpt-5.4"}] = "qmodel"
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)
	billingService := newCalculator(nil)
	r := billingtestkit.PriceResolver(pricingConfigService, billingService)

	basePricing, err := billingService.GetModelPricing("gpt-5.4")
	require.NoError(t, err)
	require.NotZero(t, basePricing.OutputPricePerToken)

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "gpt-5.4",
		GroupID: &groupID,
	})

	require.NotNil(t, resolved)
	require.Equal(t, billingpricing.PricingSourceConfig, resolved.Source)
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, inputPrice, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, basePricing.OutputPricePerToken, resolved.BasePricing.OutputPricePerToken, 1e-12)
}

func TestResolve_QoderStandardModelMappedToRouteKeyKeepsBaseForPartialIntervalPricing(t *testing.T) {
	groupID := int64(100)
	inputPrice := 20e-6
	maxTokens := 1000
	cache := routingtestkit.NewModelConfigData()
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: 1, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "gpt-5.4"}] = &routing.ModelPricingEntry{
		Models:      []string{"gpt-5.4"},
		BillingMode: routing.BillingModeToken,
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: &maxTokens, InputPrice: &inputPrice},
		},
	}
	cache.Models[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "gpt-5.4"}] = "qmodel"
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)
	billingService := newCalculator(nil)
	r := billingtestkit.PriceResolver(pricingConfigService, billingService)

	basePricing, err := billingService.GetModelPricing("gpt-5.4")
	require.NoError(t, err)
	require.NotZero(t, basePricing.OutputPricePerToken)

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "gpt-5.4",
		GroupID: &groupID,
	})
	require.NotNil(t, resolved)
	require.Equal(t, billingpricing.PricingSourceConfig, resolved.Source)

	intervalPricing := billingpricing.GetIntervalPricing(resolved, 100)
	require.NotNil(t, intervalPricing)
	require.InDelta(t, inputPrice, intervalPricing.InputPricePerToken, 1e-12)
	require.InDelta(t, basePricing.OutputPricePerToken, intervalPricing.OutputPricePerToken, 1e-12)
}

func TestResolve_QoderCustomAliasUnknownBaseZerosMissingPartialConfigPricing(t *testing.T) {
	groupID := int64(100)
	inputPrice := 20e-6
	cache := routingtestkit.NewModelConfigData()
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: 1, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "custom-qoder"}] = &routing.ModelPricingEntry{
		Models:      []string{"custom-qoder"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
	}
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)
	billingService := newCalculator(nil)
	r := billingtestkit.PriceResolver(pricingConfigService, billingService)

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "custom-qoder",
		GroupID: &groupID,
	})

	require.NotNil(t, resolved)
	require.Equal(t, billingpricing.PricingSourceConfig, resolved.Source)
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, inputPrice, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.Zero(t, resolved.BasePricing.OutputPricePerToken)
}

func TestResolve_QoderCustomAliasUnknownBaseZerosMissingPartialIntervalPricing(t *testing.T) {
	groupID := int64(100)
	inputPrice := 20e-6
	maxTokens := 1000
	cache := routingtestkit.NewModelConfigData()
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: 1, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "custom-qoder"}] = &routing.ModelPricingEntry{
		Models:      []string{"custom-qoder"},
		BillingMode: routing.BillingModeToken,
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: &maxTokens, InputPrice: &inputPrice},
		},
	}
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)
	billingService := newCalculator(nil)
	r := billingtestkit.PriceResolver(pricingConfigService, billingService)

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "custom-qoder",
		GroupID: &groupID,
	})
	require.NotNil(t, resolved)
	require.Equal(t, billingpricing.PricingSourceConfig, resolved.Source)

	intervalPricing := billingpricing.GetIntervalPricing(resolved, 100)
	require.NotNil(t, intervalPricing)
	require.InDelta(t, inputPrice, intervalPricing.InputPricePerToken, 1e-12)
	require.Zero(t, intervalPricing.OutputPricePerToken)
}

func TestResolve_QoderBlankRouteKeyPricingIsUnpricedButAliasManualPricingWorks(t *testing.T) {
	groupID := int64(100)
	inputPrice := 20e-6
	cache := routingtestkit.NewModelConfigData()
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: 1, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qmodel"}] = &routing.ModelPricingEntry{
		Models:      []string{"qmodel"},
		BillingMode: routing.BillingModeToken,
	}
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qwen3.7-plus"}] = &routing.ModelPricingEntry{
		Models:      []string{"qwen3.7-plus"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
	}
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)
	billingService := newCalculator(nil)
	r := billingtestkit.PriceResolver(pricingConfigService, billingService)

	routeResolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "qmodel",
		GroupID: &groupID,
	})
	require.NotNil(t, routeResolved)
	require.Equal(t, billingpricing.PricingSourceUnpriced, routeResolved.Source)
	require.Nil(t, routeResolved.BasePricing)
	require.False(t, routeResolved.HasEffectivePricing())

	aliasResolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "qwen3.7-plus",
		GroupID: &groupID,
	})
	require.NotNil(t, aliasResolved)
	require.Equal(t, billingpricing.PricingSourceConfig, aliasResolved.Source)
	require.True(t, aliasResolved.HasEffectivePricing())
	require.InDelta(t, inputPrice, aliasResolved.BasePricing.InputPricePerToken, 1e-12)
	require.Zero(t, aliasResolved.BasePricing.OutputPricePerToken)
}

func TestResolve_BlankWildcardPricingDoesNotMaskLaterEffectiveWildcard(t *testing.T) {
	groupID := int64(100)
	inputPrice := 20e-6
	cache := routingtestkit.NewModelConfigData()
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: 1, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.PricePatterns[routingtestkit.GroupPlatform{GroupID: groupID, Platform: capability.PlatformQoder}] = []*routingtestkit.PricePattern{
		{
			Prefix: "qwen3.",
			Pricing: &routing.ModelPricingEntry{
				Models:      []string{"qwen3.*"},
				BillingMode: routing.BillingModeToken,
			},
		},
		{
			Prefix: "qwen3.7-",
			Pricing: &routing.ModelPricingEntry{
				Models:      []string{"qwen3.7-*"},
				BillingMode: routing.BillingModeToken,
				InputPrice:  &inputPrice,
			},
		},
	}
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)
	billingService := newCalculator(nil)
	r := billingtestkit.PriceResolver(pricingConfigService, billingService)

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "qwen3.7-plus",
		GroupID: &groupID,
	})

	require.NotNil(t, resolved)
	require.Equal(t, billingpricing.PricingSourceConfig, resolved.Source)
	require.True(t, resolved.HasEffectivePricing())
	require.InDelta(t, inputPrice, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.Zero(t, resolved.BasePricing.OutputPricePerToken)
}

func TestResolve_QoderPerRequestRouteKeyTokenOnlyIntervalIsUnpriced(t *testing.T) {
	groupID := int64(100)
	inputPrice := 20e-6
	staleTokenPrice := 99e-6
	cache := routingtestkit.NewModelConfigData()
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: 1, Status: billing.StatusActive}
	cache.Platforms[groupID] = capability.PlatformQoder
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qmodel"}] = &routing.ModelPricingEntry{
		Models:      []string{"qmodel"},
		BillingMode: routing.BillingModePerRequest,
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, InputPrice: &staleTokenPrice},
		},
	}
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Platform: capability.PlatformQoder, Model: "qwen3.7-plus"}] = &routing.ModelPricingEntry{
		Models:      []string{"qwen3.7-plus"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  &inputPrice,
	}
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)
	billingService := newCalculator(nil)
	r := billingtestkit.PriceResolver(pricingConfigService, billingService)

	routeResolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "qmodel",
		GroupID: &groupID,
	})
	require.NotNil(t, routeResolved)
	require.Equal(t, billingpricing.PricingSourceUnpriced, routeResolved.Source)
	require.Nil(t, routeResolved.BasePricing)
	require.False(t, routeResolved.HasEffectivePricing())

	aliasResolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "qwen3.7-plus",
		GroupID: &groupID,
	})
	require.NotNil(t, aliasResolved)
	require.Equal(t, billingpricing.PricingSourceConfig, aliasResolved.Source)
	require.True(t, aliasResolved.HasEffectivePricing())
	require.InDelta(t, inputPrice, aliasResolved.BasePricing.InputPricePerToken, 1e-12)
}

func TestResolve_WithPricingConfigOverride_TokenWithIntervals(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(128000), InputPrice: testPtrFloat64(2e-6), OutputPrice: testPtrFloat64(8e-6)},
			{MinTokens: 128000, MaxTokens: nil, InputPrice: testPtrFloat64(4e-6), OutputPrice: testPtrFloat64(16e-6)},
		},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.NotNil(t, resolved)
	require.Equal(t, "pricing_config", resolved.Source)
	require.Len(t, resolved.Intervals, 2)

	// GetIntervalPricing should use configPricing intervals
	iv := billingpricing.GetIntervalPricing(resolved, 50000)
	require.NotNil(t, iv)
	require.InDelta(t, 2e-6, iv.InputPricePerToken, 1e-12)
	require.InDelta(t, 8e-6, iv.OutputPricePerToken, 1e-12)

	iv2 := billingpricing.GetIntervalPricing(resolved, 200000)
	require.NotNil(t, iv2)
	require.InDelta(t, 4e-6, iv2.InputPricePerToken, 1e-12)
	require.InDelta(t, 16e-6, iv2.OutputPricePerToken, 1e-12)
}

func TestResolve_WithPricingConfigOverride_PriceMultiplierScalesIntervalsAndFallbackFields(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:          []string{"claude-sonnet-4"},
		BillingMode:     routing.BillingModeToken,
		PriceMultiplier: testPtrFloat64(2),
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(128000), InputPrice: testPtrFloat64(2e-6)},
		},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	pricing := billingpricing.GetIntervalPricing(resolved, 50000)
	require.NotNil(t, pricing)
	require.InDelta(t, 4e-6, pricing.InputPricePerToken, 1e-12)
	// An omitted interval output price inherits the model default before applying the multiplier.
	require.InDelta(t, 30e-6, pricing.OutputPricePerToken, 1e-12)
}

func TestResolve_WithPricingConfigOverride_FastModeMultiplierAppliesToIntervals(t *testing.T) {
	calculator := billingtestkit.ResolverCalculator()
	r := billingtestkit.ResolverWithCards(t, calculator, []routing.ModelPricingEntry{{
		Models:             []string{"claude-sonnet-4"},
		BillingMode:        routing.BillingModeToken,
		PriceMultiplier:    testPtrFloat64(1.25),
		FastModeMultiplier: testPtrFloat64(2),
		Intervals: []routing.PricingInterval{{
			MinTokens:   0,
			MaxTokens:   testPtrInt(128000),
			InputPrice:  testPtrFloat64(2e-6),
			OutputPrice: testPtrFloat64(8e-6),
		}},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})
	pricing := billingpricing.GetIntervalPricing(resolved, 50000)
	require.NotNil(t, pricing)
	require.Equal(t, testPtrFloat64(2), pricing.FastModeMultiplier)

	tokens := billingpricing.UsageTokens{InputTokens: 100, OutputTokens: 10}
	standard, err := calculator.CalculateCostUnified(billing.CostInput{
		Ctx:         context.Background(),
		Model:       "claude-sonnet-4",
		GroupID:     billingtestkit.GroupID(),
		Tokens:      tokens,
		ServiceTier: "",
		Resolver:    r,
		Resolved:    resolved,
	})
	require.NoError(t, err)
	fast, err := calculator.CalculateCostUnified(billing.CostInput{
		Ctx:         context.Background(),
		Model:       "claude-sonnet-4",
		GroupID:     billingtestkit.GroupID(),
		Tokens:      tokens,
		ServiceTier: "priority",
		Resolver:    r,
		Resolved:    resolved,
	})
	require.NoError(t, err)
	// The price multiplier applies first; the Fast multiplier scales the resulting standard price.
	require.InDelta(t, standard.TotalCost*2, fast.TotalCost, 1e-12)
}

func TestResolve_WithPricingConfigOverride_TokenNilBasePricing(t *testing.T) {
	// For an unknown model, flat card prices supply the missing BasePricing.
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"unknown-model-xyz"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  testPtrFloat64(7e-6),
		OutputPrice: testPtrFloat64(21e-6),
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "unknown-model-xyz",
		GroupID: billingtestkit.GroupID(),
	})

	require.NotNil(t, resolved)
	require.Equal(t, "pricing_config", resolved.Source)
	// ApplyTokenOverrides creates BasePricing from the explicit card prices.
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, 7e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 21e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
}

func TestResolve_WithPricingConfigOverride_PerRequest(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:          []string{"claude-sonnet-4"},
		BillingMode:     routing.BillingModePerRequest,
		PerRequestPrice: testPtrFloat64(0.05),
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(128000), PerRequestPrice: testPtrFloat64(0.03)},
			{MinTokens: 128000, MaxTokens: nil, PerRequestPrice: testPtrFloat64(0.10)},
		},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.NotNil(t, resolved)
	require.Equal(t, routing.BillingModePerRequest, resolved.Mode)
	require.Equal(t, "pricing_config", resolved.Source)
	require.InDelta(t, 0.05, resolved.DefaultPerRequestPrice, 1e-12)
	require.Len(t, resolved.RequestTiers, 2)

	// Verify tier lookups
	require.InDelta(t, 0.03, billingpricing.GetRequestTierPriceByContext(resolved, 50000), 1e-12)
	require.InDelta(t, 0.10, billingpricing.GetRequestTierPriceByContext(resolved, 200000), 1e-12)
}

func TestResolve_WithPricingConfigOverride_PriceMultiplierScalesPerRequestPrices(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:          []string{"claude-sonnet-4"},
		BillingMode:     routing.BillingModePerRequest,
		PriceMultiplier: testPtrFloat64(2),
		PerRequestPrice: testPtrFloat64(0.05),
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(128000), PerRequestPrice: testPtrFloat64(0.03)},
		},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.InDelta(t, 0.10, resolved.DefaultPerRequestPrice, 1e-12)
	require.InDelta(t, 0.06, billingpricing.GetRequestTierPriceByContext(resolved, 50000), 1e-12)
}

func TestResolve_WithPricingConfigOverride_PerRequestNilPrice(t *testing.T) {
	// PerRequestPrice nil → DefaultPerRequestPrice stays 0.
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModePerRequest,
		// PerRequestPrice intentionally nil
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(128000), PerRequestPrice: testPtrFloat64(0.02)},
		},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.NotNil(t, resolved)
	require.Equal(t, routing.BillingModePerRequest, resolved.Mode)
	require.InDelta(t, 0.0, resolved.DefaultPerRequestPrice, 1e-12)
	require.Len(t, resolved.RequestTiers, 1)
}

func TestResolve_WithPricingConfigOverride_Image(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:          []string{"claude-sonnet-4"},
		BillingMode:     routing.BillingModeImage,
		PerRequestPrice: testPtrFloat64(0.08),
		Intervals: []routing.PricingInterval{
			{TierLabel: "1K", PerRequestPrice: testPtrFloat64(0.04)},
			{TierLabel: "2K", PerRequestPrice: testPtrFloat64(0.08)},
			{TierLabel: "4K", PerRequestPrice: testPtrFloat64(0.16)},
		},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.NotNil(t, resolved)
	require.Equal(t, routing.BillingModeImage, resolved.Mode)
	require.Equal(t, "pricing_config", resolved.Source)
	require.InDelta(t, 0.08, resolved.DefaultPerRequestPrice, 1e-12)
	require.Len(t, resolved.RequestTiers, 3)
}

func TestResolve_WithPricingConfigOverride_ImageTierLabels(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeImage,
		Intervals: []routing.PricingInterval{
			{TierLabel: "1K", PerRequestPrice: testPtrFloat64(0.04)},
			{TierLabel: "2K", PerRequestPrice: testPtrFloat64(0.08)},
			{TierLabel: "4K", PerRequestPrice: testPtrFloat64(0.16)},
		},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.InDelta(t, 0.04, billingpricing.GetRequestTierPrice(resolved, "1K"), 1e-12)
	require.InDelta(t, 0.08, billingpricing.GetRequestTierPrice(resolved, "2K"), 1e-12)
	require.InDelta(t, 0.16, billingpricing.GetRequestTierPrice(resolved, "4K"), 1e-12)
	require.InDelta(t, 0.0, billingpricing.GetRequestTierPrice(resolved, "8K"), 1e-12) // not found
}

func TestResolve_WithPricingConfigOverride_SourceIsPricingConfig(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  testPtrFloat64(1e-6),
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.Equal(t, "pricing_config", resolved.Source)
}

func TestResolve_WithPricingConfigOverride_DefaultMode(t *testing.T) {
	// PricingConfig pricing with empty BillingMode → defaults to BillingModeToken.
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: "", // intentionally empty
		InputPrice:  testPtrFloat64(5e-6),
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.Equal(t, "pricing_config", resolved.Source)
	require.Equal(t, routing.BillingModeToken, resolved.Mode)
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, 5e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
}

func TestGetIntervalPricing_WithPricingConfigIntervals(t *testing.T) {
	// PricingConfig provides intervals that override the base pricing path.
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(100000), InputPrice: testPtrFloat64(1e-6), OutputPrice: testPtrFloat64(5e-6)},
			{MinTokens: 100000, MaxTokens: nil, InputPrice: testPtrFloat64(2e-6), OutputPrice: testPtrFloat64(10e-6)},
		},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	// Token count 50000 matches first interval
	pricing := billingpricing.GetIntervalPricing(resolved, 50000)
	require.NotNil(t, pricing)
	require.InDelta(t, 1e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 5e-6, pricing.OutputPricePerToken, 1e-12)

	// Token count 150000 matches second interval
	pricing2 := billingpricing.GetIntervalPricing(resolved, 150000)
	require.NotNil(t, pricing2)
	require.InDelta(t, 2e-6, pricing2.InputPricePerToken, 1e-12)
	require.InDelta(t, 10e-6, pricing2.OutputPricePerToken, 1e-12)
}

func TestGetIntervalPricing_PricingConfigIntervalsNoMatch(t *testing.T) {
	// PricingConfig intervals don't match token count → falls back to BasePricing.
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		Intervals: []routing.PricingInterval{
			// Only covers tokens > 50000
			{MinTokens: 50000, MaxTokens: testPtrInt(200000), InputPrice: testPtrFloat64(9e-6)},
		},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	// Token count 1000 doesn't match any interval (1000 <= 50000 minTokens)
	pricing := billingpricing.GetIntervalPricing(resolved, 1000)
	// Should fall back to BasePricing (from the billing service fallback)
	require.NotNil(t, pricing)
	require.Equal(t, resolved.BasePricing, pricing)
	require.InDelta(t, 3e-6, pricing.InputPricePerToken, 1e-12) // original base price
}

func TestResolve_WithPricingConfigOverride_CacheError(t *testing.T) {
	// A ListAll failure prevents PricingConfigService from building its cache.
	// Resolve then uses the available base pricing.
	repo := &routingtestkit.PricingConfigRepositoryStub{
		ListAllFn: func(_ context.Context) ([]routingtestkit.Configuration, error) {
			return nil, errors.New("database unavailable")
		},
	}
	cs := routingtestkit.NewPricingConfigService(repo, nil, routing.PricingConfigOptions{
		Warn: slog.
			Warn,
		Now: time.
			Now, LoadLocation: provider.
			LoadPricingLocation,
	},
	)
	bs := billingtestkit.ResolverCalculator()
	r := billingtestkit.PriceResolver(cs, bs)

	gid := int64(100)
	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: &gid,
	})

	require.NotNil(t, resolved)
	// The failed configuration lookup must not select configuration pricing.
	require.NotEqual(t, "pricing_config", resolved.Source)
	// The test catalog's base pricing remains available.
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, 3e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
}

func TestGetRequestTierPriceByContext_EmptyTiers(t *testing.T) {
	resolved := &billingpricing.ResolvedPricing{
		Mode:         routing.BillingModePerRequest,
		RequestTiers: nil, // empty
	}

	price := billingpricing.GetRequestTierPriceByContext(resolved, 50000)
	require.InDelta(t, 0.0, price, 1e-12)

	// Also test with explicit empty slice
	resolved2 := &billingpricing.ResolvedPricing{
		Mode:         routing.BillingModePerRequest,
		RequestTiers: []routing.PricingInterval{},
	}

	price2 := billingpricing.GetRequestTierPriceByContext(resolved2, 50000)
	require.InDelta(t, 0.0, price2, 1e-12)
}

func TestGetRequestTierPriceByContext_ExactBoundary(t *testing.T) {
	resolved := &billingpricing.ResolvedPricing{
		Mode: routing.BillingModePerRequest,
		RequestTiers: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(128000), PerRequestPrice: testPtrFloat64(0.05)},
			{MinTokens: 128000, MaxTokens: nil, PerRequestPrice: testPtrFloat64(0.10)},
		},
	}

	// totalContextTokens = 128000 exactly:
	// FindMatchingInterval checks: totalTokens > MinTokens && totalTokens <= MaxTokens
	// For first interval: 128000 > 0 (true) && 128000 <= 128000 (true) → matches first interval
	price := billingpricing.GetRequestTierPriceByContext(resolved, 128000)
	require.InDelta(t, 0.05, price, 1e-12)

	// totalContextTokens = 128001 should match second interval
	// For first interval: 128001 > 0 (true) && 128001 <= 128000 (false) → no match
	// For second interval: 128001 > 128000 (true) && MaxTokens == nil → matches
	price2 := billingpricing.GetRequestTierPriceByContext(resolved, 128001)
	require.InDelta(t, 0.10, price2, 1e-12)
}

func TestFilterValidTokenIntervals(t *testing.T) {
	tests := []struct {
		name      string
		intervals []routing.PricingInterval
		wantLen   int
	}{
		{
			name:      "empty list",
			intervals: nil,
			wantLen:   0,
		},
		{
			name: "all-nil interval filtered out",
			intervals: []routing.PricingInterval{
				{MinTokens: 0, MaxTokens: testPtrInt(128000)},
			},
			wantLen: 0,
		},
		{
			name: "interval with only InputPrice kept",
			intervals: []routing.PricingInterval{
				{MinTokens: 0, MaxTokens: testPtrInt(128000), InputPrice: testPtrFloat64(1e-6)},
			},
			wantLen: 1,
		},
		{
			name: "interval with only OutputPrice kept",
			intervals: []routing.PricingInterval{
				{MinTokens: 0, MaxTokens: testPtrInt(128000), OutputPrice: testPtrFloat64(2e-6)},
			},
			wantLen: 1,
		},
		{
			name: "interval with only CacheWritePrice kept",
			intervals: []routing.PricingInterval{
				{MinTokens: 0, CacheWritePrice: testPtrFloat64(3e-6)},
			},
			wantLen: 1,
		},
		{
			name: "interval with only CacheReadPrice kept",
			intervals: []routing.PricingInterval{
				{MinTokens: 0, CacheReadPrice: testPtrFloat64(0.5e-6)},
			},
			wantLen: 1,
		},
		{
			name: "interval with only InputMultiplier kept",
			intervals: []routing.PricingInterval{
				{MinTokens: 0, InputMultiplier: testPtrFloat64(1.2)},
			},
			wantLen: 1,
		},
		{
			name: "interval with only PerRequestPrice filtered out for token mode",
			intervals: []routing.PricingInterval{
				{TierLabel: "1K", PerRequestPrice: testPtrFloat64(0.04)},
			},
			wantLen: 0,
		},
		{
			name: "mixed valid and invalid",
			intervals: []routing.PricingInterval{
				{MinTokens: 0, MaxTokens: testPtrInt(128000), InputPrice: testPtrFloat64(1e-6)},
				{MinTokens: 128000, MaxTokens: nil}, // all-nil → filtered out
				{MinTokens: 256000, OutputPrice: testPtrFloat64(5e-6)},
			},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := billingpricing.FilterValidTokenIntervals(tt.intervals)
			require.Len(t, result, tt.wantLen)
		})
	}
}

func TestFilterValidRequestIntervals(t *testing.T) {
	tests := []struct {
		name      string
		intervals []routing.PricingInterval
		wantLen   int
	}{
		{
			name: "all-nil interval filtered out",
			intervals: []routing.PricingInterval{
				{TierLabel: "1K"},
			},
			wantLen: 0,
		},
		{
			name: "token-only interval filtered out for request mode",
			intervals: []routing.PricingInterval{
				{MinTokens: 0, MaxTokens: testPtrInt(128000), InputPrice: testPtrFloat64(1e-6)},
			},
			wantLen: 0,
		},
		{
			name: "per-request interval kept",
			intervals: []routing.PricingInterval{
				{TierLabel: "1K", PerRequestPrice: testPtrFloat64(0.04)},
			},
			wantLen: 1,
		},
		{
			name: "explicit zero per-request interval kept",
			intervals: []routing.PricingInterval{
				{TierLabel: "1K", PerRequestPrice: testPtrFloat64(0)},
			},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := billingpricing.FilterValidRequestIntervals(tt.intervals)
			require.Len(t, result, tt.wantLen)
		})
	}
}

func TestModelPricingEntryHasEffectivePricingIsModeAware(t *testing.T) {
	require.False(t, (&routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		Intervals: []routing.PricingInterval{
			{TierLabel: "1K", PerRequestPrice: testPtrFloat64(0.04)},
		},
	}).HasEffectivePricing())

	require.False(t, (&routing.ModelPricingEntry{
		BillingMode: routing.BillingModePerRequest,
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, InputPrice: testPtrFloat64(1e-6)},
		},
	}).HasEffectivePricing())

	require.True(t, (&routing.ModelPricingEntry{
		BillingMode: routing.BillingModePerRequest,
		Intervals: []routing.PricingInterval{
			{TierLabel: "1K", PerRequestPrice: testPtrFloat64(0)},
		},
	}).HasEffectivePricing())

	require.True(t, (&routing.ModelPricingEntry{
		BillingMode:     routing.BillingModeVideo,
		PerRequestPrice: testPtrFloat64(0),
	}).HasEffectivePricing())

	require.True(t, (&routing.ModelPricingEntry{
		BillingMode: routing.BillingModeToken,
		InputPrice:  testPtrFloat64(0),
	}).HasEffectivePricing())

	require.True(t, (&routing.ModelPricingEntry{
		BillingMode:    routing.BillingModeToken,
		FastMultiplier: testPtrFloat64(1.5),
	}).HasEffectivePricing())
}

func TestApplyTokenOverrides_FlatSetsImageOutputPriceExplicit(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  testPtrFloat64(3e-6),
		OutputPrice: testPtrFloat64(15e-6),
		// Image output price intentionally omitted.
	}})
	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.Equal(t, billingpricing.PricingSourceConfig, resolved.Source)
	require.True(t, resolved.BasePricing.ImageOutputPriceExplicit)
	require.Equal(t, 0.0, resolved.BasePricing.ImageOutputPricePerToken)
}

func TestApplyTokenOverrides_FlatWithImageOutputPriceSetsExplicit(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:           []string{"claude-sonnet-4"},
		BillingMode:      routing.BillingModeToken,
		InputPrice:       testPtrFloat64(3e-6),
		OutputPrice:      testPtrFloat64(15e-6),
		ImageOutputPrice: testPtrFloat64(50e-6),
	}})
	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.True(t, resolved.BasePricing.ImageOutputPriceExplicit)
	require.InDelta(t, 50e-6, resolved.BasePricing.ImageOutputPricePerToken, 1e-12)
}

func TestApplyTokenOverrides_FlatUsesPricingConfigImageInputPrice(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:          []string{"claude-sonnet-4"},
		BillingMode:     routing.BillingModeToken,
		InputPrice:      testPtrFloat64(3e-6),
		ImageInputPrice: testPtrFloat64(8e-6),
	}})
	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.Equal(t, billingpricing.PricingSourceConfig, resolved.Source)
	require.InDelta(t, 8e-6, resolved.BasePricing.ImageInputPricePerToken, 1e-12)
}

func TestIntervalToModelPricingWithBaseClearsUnsetPricingConfigImageInputPrice(t *testing.T) {
	base := &billingpricing.ModelPricing{ImageInputPricePerToken: 99e-6}
	pricing := billingpricing.IntervalToModelPricingWithBase(
		&routing.PricingInterval{MinTokens: 0, InputPrice: testPtrFloat64(3e-6)},
		false,
		&routing.ModelPricingEntry{BillingMode: routing.BillingModeToken},
		base,
	)

	require.Zero(t, pricing.ImageInputPricePerToken)
}

func TestIntervalToModelPricingWithBaseAppliesMultipliersAndPreservesTierRatio(t *testing.T) {
	base := &billingpricing.ModelPricing{
		InputPricePerToken:                 2e-6,
		InputPricePerTokenPriority:         4e-6,
		OutputPricePerToken:                10e-6,
		OutputPricePerTokenPriority:        20e-6,
		CacheCreationPricePerToken:         3e-6,
		CacheCreationPricePerTokenPriority: 6e-6,
		CacheReadPricePerToken:             0.5e-6,
		CacheReadPricePerTokenPriority:     1e-6,
	}
	pricing := billingpricing.IntervalToModelPricingWithBase(
		&routing.PricingInterval{
			InputMultiplier:      testPtrFloat64(1.5),
			OutputMultiplier:     testPtrFloat64(0.5),
			CacheWriteMultiplier: testPtrFloat64(2),
			CacheReadMultiplier:  testPtrFloat64(0.25),
		},
		false,
		&routing.ModelPricingEntry{BillingMode: routing.BillingModeToken, FastMultiplier: testPtrFloat64(2), FlexMultiplier: testPtrFloat64(0.25)},
		base,
	)

	require.InDelta(t, 3e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 6e-6, pricing.InputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 5e-6, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, 10e-6, pricing.OutputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 6e-6, pricing.CacheCreationPricePerToken, 1e-12)
	require.InDelta(t, 12e-6, pricing.CacheCreationPricePerTokenPriority, 1e-12)
	require.InDelta(t, 0.125e-6, pricing.CacheReadPricePerToken, 1e-12)
	require.InDelta(t, 0.25e-6, pricing.CacheReadPricePerTokenPriority, 1e-12)
	require.NotNil(t, pricing.FastMultiplier)
	require.NotNil(t, pricing.FlexMultiplier)
	require.InDelta(t, 2, *pricing.FastMultiplier, 1e-12)
	require.InDelta(t, 0.25, *pricing.FlexMultiplier, 1e-12)
}

func TestApplyTokenOverrides_IntervalSetsImageOutputPriceExplicit(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		// Image output price intentionally omitted.
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(100000), InputPrice: testPtrFloat64(3e-6), OutputPrice: testPtrFloat64(15e-6)},
		},
	}})
	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	// The base price records explicit image pricing for requests outside configured intervals.
	require.True(t, resolved.BasePricing.ImageOutputPriceExplicit)
	require.Equal(t, 0.0, resolved.BasePricing.ImageOutputPricePerToken)

	// Interval pricing also records that the image output price is explicit.
	pricing := billingpricing.GetIntervalPricing(resolved, 50000)
	require.True(t, pricing.ImageOutputPriceExplicit)
	require.Equal(t, 0.0, pricing.ImageOutputPricePerToken)
}

func TestIntervalToModelPricingWithBaseClearsImageOutputWhenPricingConfigUnset(t *testing.T) {
	base := &billingpricing.ModelPricing{
		ImageOutputPricePerToken: 99e-6,
	}
	pricing := billingpricing.IntervalToModelPricingWithBase(
		&routing.PricingInterval{
			MinTokens:   0,
			InputPrice:  testPtrFloat64(3e-6),
			OutputPrice: testPtrFloat64(15e-6),
		},
		false,
		&routing.ModelPricingEntry{BillingMode: routing.BillingModeToken},
		base,
	)

	require.True(t, pricing.ImageOutputPriceExplicit)
	require.Equal(t, 0.0, pricing.ImageOutputPricePerToken)
}

// TestApplyTokenOverrides_FlatDoesNotPolluteFallbackPrices checks that overrides modify a BasePricing copy.
func TestApplyTokenOverrides_FlatDoesNotPolluteFallbackPrices(t *testing.T) {
	prices := billingtestkit.ResolverFallbackPrices()
	calculator := newCalculatorWithPrices(nil, prices)
	r := billingtestkit.ResolverWithCards(t, calculator, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  testPtrFloat64(10e-6), // Base input price: 3e-6.
		OutputPrice: testPtrFloat64(50e-6), // Base output price: 15e-6.
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	// The resolved price uses the shared configuration override.
	require.NotNil(t, resolved)
	require.InDelta(t, 10e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 50e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)

	// Shared base prices remain 3e-6 and 15e-6.
	fp := prices["claude-sonnet-4"]
	require.InDelta(t, 3e-6, fp.InputPricePerToken, 1e-12, "fallback InputPricePerToken polluted")
	require.InDelta(t, 15e-6, fp.OutputPricePerToken, 1e-12, "fallback OutputPricePerToken polluted")
	require.False(t, fp.ImageOutputPriceExplicit, "fallback ImageOutputPriceExplicit polluted")
}

// TestApplyTokenOverrides_IntervalDoesNotPolluteFallbackPrices checks that interval overrides copy base pricing.
func TestApplyTokenOverrides_IntervalDoesNotPolluteFallbackPrices(t *testing.T) {
	prices := billingtestkit.ResolverFallbackPrices()
	calculator := newCalculatorWithPrices(nil, prices)
	r := billingtestkit.ResolverWithCards(t, calculator, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(100000), InputPrice: testPtrFloat64(2e-6), OutputPrice: testPtrFloat64(8e-6)},
		},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.NotNil(t, resolved)
	require.True(t, resolved.BasePricing.ImageOutputPriceExplicit)

	// Shared base prices remain 3e-6 and 15e-6.
	fp := prices["claude-sonnet-4"]
	require.InDelta(t, 3e-6, fp.InputPricePerToken, 1e-12, "fallback InputPricePerToken polluted")
	require.InDelta(t, 15e-6, fp.OutputPricePerToken, 1e-12, "fallback OutputPricePerToken polluted")
	require.False(t, fp.ImageOutputPriceExplicit, "fallback ImageOutputPriceExplicit polluted")
}

func TestResolve_SharedPricingIsAuthoritative(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models: []string{"claude-sonnet-4"}, BillingMode: routing.BillingModeToken,
		InputPrice: testPtrFloat64(10e-6), OutputPrice: testPtrFloat64(20e-6),
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{Model: "claude-sonnet-4", GroupID: billingtestkit.GroupID()})

	require.Equal(t, billingpricing.PricingSourceConfig, resolved.Source)
	require.InDelta(t, 10e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 20e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
}

func TestResolve_ConfigIntervalsOverridePresetRegardlessOfToggle(t *testing.T) {
	prices := billingtestkit.ResolverFallbackPrices()
	prices["claude-sonnet-4"].LongContextInputThreshold = 200000
	prices["claude-sonnet-4"].LongContextThresholdInclusive = true
	prices["claude-sonnet-4"].LongContextInputMultiplier = 2
	prices["claude-sonnet-4"].LongContextOutputMultiplier = 2
	bs := newCalculatorWithPrices(nil, prices)
	settings := billingpricing.DefaultBillingSettings()
	settings.LongContextPricingEnabled = false
	cards := []routing.ModelPricingEntry{{
		Models: []string{"claude-sonnet-4"}, BillingMode: routing.BillingModeToken,
		InputPrice: testPtrFloat64(1e-6), OutputPrice: testPtrFloat64(2e-6),
		Intervals: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(200000), InputPrice: testPtrFloat64(9e-6)},
			{MinTokens: 200000, InputPrice: testPtrFloat64(18e-6)},
		},
	}}

	r, source := settingsResolver(bs, settings, cards)
	resolved := r.Resolve(context.Background(), billing.PricingInput{Model: "claude-sonnet-4", GroupID: billingtestkit.GroupID()})
	require.False(t, resolved.LongContextPricingEnabled)
	require.Len(t, resolved.Intervals, 2)
	require.InDelta(t, 18e-6, billingpricing.GetIntervalPricing(resolved, 300000).InputPricePerToken, 1e-12)
	require.Equal(t, 200000, resolved.BasePricing.LongContextInputThreshold)

	source.settings.LongContextPricingEnabled = true
	resolved = r.Resolve(context.Background(), billing.PricingInput{Model: "claude-sonnet-4", GroupID: billingtestkit.GroupID()})
	require.True(t, resolved.LongContextPricingEnabled)
	require.Len(t, resolved.Intervals, 2)
	require.InDelta(t, 18e-6, billingpricing.GetIntervalPricing(resolved, 300000).InputPricePerToken, 1e-12)
	require.Equal(t, 200000, resolved.BasePricing.LongContextInputThreshold)
	require.InDelta(t, 2.0, resolved.BasePricing.LongContextInputMultiplier, 1e-12)
}

// testPtrInt returns a pointer to an interval endpoint.
func testPtrInt(v int) *int { return &v }

// newResolverWithPricingConfig combines the supplied cards with the resolver test catalog.
func newResolverWithPricingConfig(t *testing.T, pricing []routing.ModelPricingEntry) *billing.PriceResolver {
	t.Helper()
	return billingtestkit.ResolverWithCards(t, billingtestkit.ResolverCalculator(), pricing)
}
