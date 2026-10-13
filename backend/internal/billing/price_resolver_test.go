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

// Full request names select configured price cards before any required catalog
// base lookup. These resolver scenarios distinguish unavailable amounts from
// explicit zero, inherited buckets from configured image zeros, and manual
// context intervals from independently toggled built-in tiers.

// TestResolveCatalogAliasesPreserveConfigPricing compares custom-catalog and
// default-fixture variants for full model names. An unpriced request spelling
// must not borrow its base card, while its explicit zero card becomes
// effective and leaves the base amount intact.
func TestResolveCatalogAliasesPreserveConfigPricing(t *testing.T) {
	// Save and restore the shared Grok default around these sequential alias
	// scenarios. Changing it supplies fixture context while full-name price
	// selection remains the behavior asserted.
	previous := xai.RuntimeDefaultTextModel()
	t.Cleanup(func() { xai.SetRuntimeDefaultTextModel(previous) })
	xai.SetRuntimeDefaultTextModel("X-AI/GROK-4.6")
	for _, tc := range []struct {
		// platform supplies group metadata while base and alias are distinct full
		// model identities. The fixture prices base and requests alias to check card
		// selection inside that group.
		platform, // fixture group platform name
		base, // separately priced full model
		alias string // requested full model spelling
	}{
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

				// An explicit zero price on the full request name takes precedence
				// over the base name's price.
				zero := 0.0
				configPricing.ModelPricing = append(configPricing.ModelPricing, routing.ModelPricingEntry{
					Models: []string{tc.alias}, BillingMode: routing.BillingModeToken, InputPrice: &zero,
				})
				repository.Values = []routingtestkit.Configuration{configPricing}
				// Invalidate after replacing the fixture cards so the next resolution
				// reads that replacement, rather than the previously loaded card selection
				// for this group.
				pricingConfigs.InvalidateCache()
				resolved = resolver.Resolve(context.Background(), input)
				require.True(t, resolved.HasEffectivePricing())
				require.Zero(t, resolved.BasePricing.InputPricePerToken)
				base := resolver.Resolve(context.Background(), billing.PricingInput{Model: tc.base, GroupID: &groupID})
				// Allow binary representation residue in these per-token rate comparisons.
				require.InDelta(t, pricingConfigPrice, base.BasePricing.InputPricePerToken, 1e-12)
			}
		})
	}
}

// TestResolveCatalogAliasesUseUnifiedPricingConfig configures two Gemini
// base-name cards and requests one suffixed model. Resolution must remain
// unpriced, even though the fixture catalog contains the unsuffixed model's
// token amount.
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

// TestGroupAndPricingCatalogAliasPrecedence requests full aliases against a
// base card, a blank exact card and explicit-zero exact/wildcard cards. The
// first two stay unpriced, while matching zero cards return zero input price.
func TestGroupAndPricingCatalogAliasPrecedence(t *testing.T) {
	for _, tc := range []struct {
		// platform supplies group metadata while base and alias are distinct full
		// model identities. The fixture prices base and requests alias to check card
		// selection inside that group.
		platform, // fixture group platform name
		base, // separately priced full model
		alias string // requested full model spelling
	}{
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
					// Invalidate after replacing the fixture cards so the next resolution
					// reads that replacement, rather than the previously loaded card
					// selection for this group.
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

// TestResolve_NoGroupID omits configuration group selection for a catalogued
// Claude model. Resolution must return its fixture input rate, token mode and
// catalog source from the base lookup.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 3e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	// The calculator obtains these amounts from the fixture catalog. Successful
	// base lookup therefore reports catalog source even though the prices
	// originate in test data.
	require.Equal(t, "catalog", resolved.Source)
}

// TestResolve_UnknownModel requests an unregistered full model with no group
// selection. Resolution must return a nonnil unpriced record with no base
// pricing, keeping absence visible to consumers.
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
	require.Equal(t, "unpriced", resolved.Source)
}

// TestGetIntervalPricing_NoIntervals supplies base pricing with no context
// intervals. The helper must return that base value for the supplied token
// count, keeping ordinary amount availability independent of interval
// presence.
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

// TestGetIntervalPricing_MatchesInterval selects short and long supplied
// context ranges with distinct input/output rates. The returned short price
// retains cache-breakdown support, and the long query returns its configured
// higher input amount.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 1e-6, result.InputPricePerToken, 1e-12)
	require.InDelta(t, 2e-6, result.OutputPricePerToken, 1e-12)
	require.True(t, result.SupportsCacheBreakdown)

	result2 := billingpricing.GetIntervalPricing(resolved, 200000)
	require.NotNil(t, result2)
	require.InDelta(t, 3e-6, result2.InputPricePerToken, 1e-12)
}

// TestGetIntervalPricing_NoMatch_FallsBackToBase supplies one bounded interval
// beginning above the requested context count. The helper must return the
// available base pricing when that interval does not match.
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

// TestGPT56ExplicitZeroCacheWritePriceIsPreserved constructs explicit-zero
// cache-write rates through flat and interval overrides. Both paths must
// retain the explicit marker and calculate zero cache-creation cost for the
// supplied GPT-5.6 usage.
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

// TestGetRequestTierPrice resolves two labeled per-request tiers, including a
// case-folded label. An unmatched label returns zero, and the value-bearing
// lookup must separately report false for that missing tier.
func TestGetRequestTierPrice(t *testing.T) {
	resolved := &billingpricing.ResolvedPricing{
		Mode: routing.BillingModePerRequest,
		RequestTiers: []routing.PricingInterval{
			{TierLabel: "1K", PerRequestPrice: testPtrFloat64(0.04)},
			{TierLabel: "2K", PerRequestPrice: testPtrFloat64(0.08)},
		},
	}

	// Allow binary representation residue in these USD unit-price comparisons.
	require.InDelta(t, 0.04, billingpricing.GetRequestTierPrice(resolved, "1K"), 1e-12)
	require.InDelta(t, 0.04, billingpricing.GetRequestTierPrice(resolved, "1k"), 1e-12)
	require.InDelta(t, 0.08, billingpricing.GetRequestTierPrice(resolved, "2K"), 1e-12)
	require.InDelta(t, 0.0, billingpricing.GetRequestTierPrice(resolved, "4K"), 1e-12)

	price, ok := billingpricing.GetRequestTierPriceValue(resolved, "4K")
	require.False(t, ok)
	require.Zero(t, price)
}

// TestGetRequestTierPriceByContext selects supplied short and long unlabeled
// request-price ranges. Positive matching counts return their respective
// amounts, while zero context returns zero with an absent-match flag.
func TestGetRequestTierPriceByContext(t *testing.T) {
	resolved := &billingpricing.ResolvedPricing{
		Mode: routing.BillingModePerRequest,
		RequestTiers: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(128000), PerRequestPrice: testPtrFloat64(0.05)},
			{MinTokens: 128000, MaxTokens: nil, PerRequestPrice: testPtrFloat64(0.10)},
		},
	}

	// Allow binary representation residue in these USD unit-price comparisons.
	require.InDelta(t, 0.05, billingpricing.GetRequestTierPriceByContext(resolved, 50000), 1e-12)
	require.InDelta(t, 0.10, billingpricing.GetRequestTierPriceByContext(resolved, 200000), 1e-12)

	price, ok := billingpricing.GetRequestTierPriceByContextValue(resolved, 0)
	require.False(t, ok)
	require.Zero(t, price)
}

// TestGetRequestTierPrice_NilPerRequestPrice supplies a matching label whose
// amount pointer is absent. The amount-only helper must return zero for that
// missing price, without establishing an explicit free tier.
func TestGetRequestTierPrice_NilPerRequestPrice(t *testing.T) {
	resolved := &billingpricing.ResolvedPricing{
		Mode: routing.BillingModePerRequest,
		RequestTiers: []routing.PricingInterval{
			{TierLabel: "1K", PerRequestPrice: nil},
		},
	}

	// Allow binary representation residue in these USD unit-price comparisons.
	require.InDelta(t, 0.0, billingpricing.GetRequestTierPrice(resolved, "1K"), 1e-12)
}

// TestResolve_WithPricingConfigOverride_TokenFlat supplies manual input and
// output amounts for a known model. Resolution must choose configuration
// source, keep those rates and leave absent catalog priority prices unset.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 10e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	// A standard-price override cannot invent a priority price missing from the catalog.
	require.Zero(t, resolved.BasePricing.InputPricePerTokenPriority)
	require.InDelta(t, 50e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
	require.Zero(t, resolved.BasePricing.OutputPricePerTokenPriority)
}

// TestResolve_WithPricingConfigOverride_TokenFlatPreservesNativeTierRatio
// overrides three buckets whose fixture priority rates are twice standard.
// Each resolved priority amount must retain that ratio after the standard
// override.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 14e-6, resolved.BasePricing.InputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 60e-6, resolved.BasePricing.OutputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 4e-6, resolved.BasePricing.CacheReadPricePerTokenPriority, 1e-12)
}

// TestResolve_WithPricingConfigOverride_TokenPartialOverride supplies only an
// input amount on a known catalog model. The input changes to its manual rate,
// while output inherits the available fixture amount.
func TestResolve_WithPricingConfigOverride_TokenPartialOverride(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		InputPrice:  testPtrFloat64(20e-6),
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	require.NotNil(t, resolved)
	require.Equal(t, "pricing_config", resolved.Source)
	require.NotNil(t, resolved.BasePricing)
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 20e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 15e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
}

// TestResolve_WithPricingConfigOverride_PriceMultiplierOnlyIsIgnored stores a
// card containing only the generic price factor. Resolution must retain
// catalog source and fixture rates, with no effective selected configuration
// price.
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

	require.NotNil(t, resolved)
	require.Equal(t, billingpricing.PricingSourceCatalog, resolved.Source)
	require.False(t, resolved.HasEffectivePricing())
	require.NotNil(t, resolved.BasePricing)
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 3e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 15e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
}

// TestResolve_BlankConfigPricingIsIgnoredForNonQoder supplies a model card
// with no amount fields. Resolution must keep catalog source and rates, report
// no effective configuration price and leave the image explicit-price marker
// false.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 3e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 15e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
	require.False(t, resolved.BasePricing.ImageOutputPriceExplicit)
}

// TestResolve_QoderCustomAliasMappedToRouteKeyZerosMissingPartialConfigPricing
// supplies an input card plus unrelated route-key metadata for an uncatalogued
// full model. Its input rate resolves from configuration and its absent output
// remains zero.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, inputPrice, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.Zero(t, resolved.BasePricing.OutputPricePerToken)
}

// TestResolve_QoderStandardModelMappedToRouteKeyKeepsBaseForPartialConfigPricing
// supplies a manual input card and route-key metadata for a catalogued GPT
// model. Input must use the card while output retains that full model's
// catalog rate.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, inputPrice, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, basePricing.OutputPricePerToken, resolved.BasePricing.OutputPricePerToken, 1e-12)
}

// TestResolve_QoderStandardModelMappedToRouteKeyKeepsBaseForPartialIntervalPricing
// supplies a partial context interval and route-key metadata for a catalogued
// GPT model. Matching interval input changes, while its omitted output
// inherits the model's available base rate.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, inputPrice, intervalPricing.InputPricePerToken, 1e-12)
	require.InDelta(t, basePricing.OutputPricePerToken, intervalPricing.OutputPricePerToken, 1e-12)
}

// TestResolve_QoderCustomAliasUnknownBaseZerosMissingPartialConfigPricing
// configures an input amount for an uncatalogued full Qoder model. Resolution
// must select configuration pricing with that input rate and zero for the
// absent output bucket.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, inputPrice, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.Zero(t, resolved.BasePricing.OutputPricePerToken)
}

// TestResolve_QoderCustomAliasUnknownBaseZerosMissingPartialIntervalPricing
// prices one input bucket in a matching interval without catalog base data.
// Interval lookup must return the configured input amount while omitted output
// stays zero.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, inputPrice, intervalPricing.InputPricePerToken, 1e-12)
	require.Zero(t, intervalPricing.OutputPricePerToken)
}

// TestResolve_QoderBlankRouteKeyPricingIsUnpricedButAliasManualPricingWorks
// queries a blank route-label card and a separately priced full model. The
// label remains unpriced with no base, while the model card resolves its
// explicit input amount.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, inputPrice, aliasResolved.BasePricing.InputPricePerToken, 1e-12)
	require.Zero(t, aliasResolved.BasePricing.OutputPricePerToken)
}

// TestResolve_BlankWildcardPricingDoesNotMaskLaterEffectiveWildcard supplies
// an earlier blank prefix card before a matching priced prefix. Resolution
// must reach the effective input card and keep its uncatalogued output bucket
// zero.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, inputPrice, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.Zero(t, resolved.BasePricing.OutputPricePerToken)
}

// TestResolve_QoderPerRequestRouteKeyTokenOnlyIntervalIsUnpriced supplies an
// invalid request-mode card containing a token-only interval. That route label
// stays unpriced, while the separately configured token model retains its
// effective manual input rate.
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
	// Allow binary representation residue in these USD unit-price comparisons.
	require.InDelta(t, inputPrice, aliasResolved.BasePricing.InputPricePerToken, 1e-12)
}

// TestResolve_WithPricingConfigOverride_TokenWithIntervals configures two
// token ranges for a known model. Resolution must retain both ranges, and
// interval lookups must return each range's supplied input/output amounts.
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

	iv := billingpricing.GetIntervalPricing(resolved, 50000)
	require.NotNil(t, iv)
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 2e-6, iv.InputPricePerToken, 1e-12)
	require.InDelta(t, 8e-6, iv.OutputPricePerToken, 1e-12)

	iv2 := billingpricing.GetIntervalPricing(resolved, 200000)
	require.NotNil(t, iv2)
	require.InDelta(t, 4e-6, iv2.InputPricePerToken, 1e-12)
	require.InDelta(t, 16e-6, iv2.OutputPricePerToken, 1e-12)
}

// TestResolve_WithPricingConfigOverride_PriceMultiplierScalesIntervalsAndFallbackFields
// supplies a doubled partial interval on a known model. Matching input and
// inherited output must both receive the card factor after their amount
// selection.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 4e-6, pricing.InputPricePerToken, 1e-12)
	// An omitted interval output price inherits the model default before
	// applying the multiplier.
	require.InDelta(t, 30e-6, pricing.OutputPricePerToken, 1e-12)
}

// TestResolve_WithPricingConfigOverride_FastModeMultiplierAppliesToIntervals
// combines a price factor and Fast factor on a token interval. Interval
// pricing retains the Fast factor, and calculated priority total must double
// the selected standard total.
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
	// The price multiplier applies first; the Fast multiplier scales the
	// resulting standard price.
	// Allow float multiplication residue when comparing the Fast charge ratio.
	require.InDelta(t, standard.TotalCost*2, fast.TotalCost, 1e-12)
}

// TestResolve_WithPricingConfigOverride_TokenNilBasePricing supplies both text
// amounts for an uncatalogued full model. Resolution must create usable base
// pricing from those manual buckets and mark configuration as its source.
func TestResolve_WithPricingConfigOverride_TokenNilBasePricing(t *testing.T) {
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
	require.NotNil(t, resolved.BasePricing)
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 7e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 21e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
}

// TestResolve_WithPricingConfigOverride_PerRequest supplies a default request
// amount and two context tiers. Resolution must preserve request mode,
// configuration source, the default amount and both selected context prices.
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
	// Allow binary representation residue in these USD unit-price comparisons.
	require.InDelta(t, 0.05, resolved.DefaultPerRequestPrice, 1e-12)
	require.Len(t, resolved.RequestTiers, 2)

	require.InDelta(t, 0.03, billingpricing.GetRequestTierPriceByContext(resolved, 50000), 1e-12)
	require.InDelta(t, 0.10, billingpricing.GetRequestTierPriceByContext(resolved, 200000), 1e-12)
}

// TestResolve_WithPricingConfigOverride_PriceMultiplierScalesPerRequestPrices
// supplies a doubled default request amount and context tier. Resolution must
// scale both the default and the matching tier price by that factor.
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

	// Allow binary representation residue in these USD unit-price comparisons.
	require.InDelta(t, 0.10, resolved.DefaultPerRequestPrice, 1e-12)
	require.InDelta(t, 0.06, billingpricing.GetRequestTierPriceByContext(resolved, 50000), 1e-12)
}

// TestResolve_WithPricingConfigOverride_PerRequestNilPrice omits the default
// request amount while supplying a context-priced tier. Resolution must retain
// request mode, a numeric zero default slot and the configured tier.
func TestResolve_WithPricingConfigOverride_PerRequestNilPrice(t *testing.T) {
	// Omit the default amount while retaining an effective context tier. The
	// numeric zero default slot here does not establish a separately configured
	// free request price.
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
	// Allow binary representation residue in these USD unit-price comparisons.
	require.InDelta(t, 0.0, resolved.DefaultPerRequestPrice, 1e-12)
	require.Len(t, resolved.RequestTiers, 1)
}

// TestResolve_WithPricingConfigOverride_Image supplies image mode, a default
// amount and three size tiers. Resolution must preserve image mode,
// configuration source, that default amount and the complete tier count.
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
	// Allow binary representation residue in these USD unit-price comparisons.
	require.InDelta(t, 0.08, resolved.DefaultPerRequestPrice, 1e-12)
	require.Len(t, resolved.RequestTiers, 3)
}

// TestResolve_WithPricingConfigOverride_ImageTierLabels supplies explicit
// image amounts for three size labels. Label lookup must return each
// configured rate and zero when the requested size has no matching tier.
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

	// Allow binary representation residue in these USD unit-price comparisons.
	require.InDelta(t, 0.04, billingpricing.GetRequestTierPrice(resolved, "1K"), 1e-12)
	require.InDelta(t, 0.08, billingpricing.GetRequestTierPrice(resolved, "2K"), 1e-12)
	require.InDelta(t, 0.16, billingpricing.GetRequestTierPrice(resolved, "4K"), 1e-12)
	require.InDelta(t, 0.0, billingpricing.GetRequestTierPrice(resolved, "8K"), 1e-12) // not found
}

// TestResolve_WithPricingConfigOverride_SourceIsPricingConfig supplies one
// input-rate card for the configured group and catalog model. Resolution must
// report pricing_config as the source selected for that request.
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

// TestResolve_WithPricingConfigOverride_DefaultMode supplies an empty billing
// mode with a manual input amount. Resolution must choose token mode,
// configuration source and the provided input rate for the known model.
func TestResolve_WithPricingConfigOverride_DefaultMode(t *testing.T) {
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 5e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
}

// TestGetIntervalPricing_WithPricingConfigIntervals resolves two manual token
// ranges over a catalog model. Lookups in each range must use the supplied
// input and output amounts from the selected configuration interval.
func TestGetIntervalPricing_WithPricingConfigIntervals(t *testing.T) {
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

	pricing := billingpricing.GetIntervalPricing(resolved, 50000)
	require.NotNil(t, pricing)
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 1e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 5e-6, pricing.OutputPricePerToken, 1e-12)

	pricing2 := billingpricing.GetIntervalPricing(resolved, 150000)
	require.NotNil(t, pricing2)
	require.InDelta(t, 2e-6, pricing2.InputPricePerToken, 1e-12)
	require.InDelta(t, 10e-6, pricing2.OutputPricePerToken, 1e-12)
}

// TestGetIntervalPricing_PricingConfigIntervalsNoMatch queries below the
// configured interval's lower bound. The helper must return the resolution's
// base pricing and the available fixture input rate for that request.
func TestGetIntervalPricing_PricingConfigIntervalsNoMatch(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models:      []string{"claude-sonnet-4"},
		BillingMode: routing.BillingModeToken,
		Intervals: []routing.PricingInterval{
			// Keep this lower bound above the requested context, making the result
			// exercise base lookup instead of interval amount selection.
			{MinTokens: 50000, MaxTokens: testPtrInt(200000), InputPrice: testPtrFloat64(9e-6)},
		},
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{
		Model:   "claude-sonnet-4",
		GroupID: billingtestkit.GroupID(),
	})

	pricing := billingpricing.GetIntervalPricing(resolved, 1000)
	require.NotNil(t, pricing)
	require.Equal(t, resolved.BasePricing, pricing)
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 3e-6, pricing.InputPricePerToken, 1e-12)
}

// TestResolve_WithPricingConfigOverride_CacheError injects a repository
// failure while building the configuration cache. Resolution must retain
// usable fixture base pricing and avoid marking the failed lookup as
// configuration-source amounts.
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
	require.NotEqual(t, "pricing_config", resolved.Source)
	require.NotNil(t, resolved.BasePricing)
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 3e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
}

// TestGetRequestTierPriceByContext_EmptyTiers checks both nil and
// allocated-empty request tier collections. The amount-only context lookup
// must return zero for each, preserving their common missing-tier result.
func TestGetRequestTierPriceByContext_EmptyTiers(t *testing.T) {
	resolved := &billingpricing.ResolvedPricing{
		Mode:         routing.BillingModePerRequest,
		RequestTiers: nil, // empty
	}

	price := billingpricing.GetRequestTierPriceByContext(resolved, 50000)
	// Allow binary representation residue in these USD unit-price comparisons.
	require.InDelta(t, 0.0, price, 1e-12)

	resolved2 := &billingpricing.ResolvedPricing{
		Mode:         routing.BillingModePerRequest,
		RequestTiers: []routing.PricingInterval{},
	}

	price2 := billingpricing.GetRequestTierPriceByContext(resolved2, 50000)
	require.InDelta(t, 0.0, price2, 1e-12)
}

// TestGetRequestTierPriceByContext_ExactBoundary queries 128000 and the
// immediately following token count. The upper-inclusive first tier must
// charge the first amount, and the next count must select the second tier.
func TestGetRequestTierPriceByContext_ExactBoundary(t *testing.T) {
	resolved := &billingpricing.ResolvedPricing{
		Mode: routing.BillingModePerRequest,
		RequestTiers: []routing.PricingInterval{
			{MinTokens: 0, MaxTokens: testPtrInt(128000), PerRequestPrice: testPtrFloat64(0.05)},
			{MinTokens: 128000, MaxTokens: nil, PerRequestPrice: testPtrFloat64(0.10)},
		},
	}

	// The shared matcher uses lower-exclusive and upper-inclusive ranges, so the
	// exact upper endpoint still belongs to the short-context request tier.
	price := billingpricing.GetRequestTierPriceByContext(resolved, 128000)
	// Allow binary representation residue in these USD unit-price comparisons.
	require.InDelta(t, 0.05, price, 1e-12)

	// Advance one token beyond that endpoint to exercise the next range; this
	// distinguishes threshold comparison from a price inherited through the
	// default path.
	price2 := billingpricing.GetRequestTierPriceByContext(resolved, 128001)
	require.InDelta(t, 0.10, price2, 1e-12)
}

// TestFilterValidTokenIntervals supplies empty, token-bearing,
// multiplier-bearing and request-only interval shapes. It checks retained
// lengths, including dropping empty or request-only entries; this table does
// not assert returned ownership or values.
func TestFilterValidTokenIntervals(t *testing.T) {
	tests := []struct {
		name      string                    // interval shape case label
		intervals []routing.PricingInterval // supplied interval shape collection
		wantLen   int                       // expected retained interval count
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

// TestFilterValidRequestIntervals distinguishes request amount presence from
// token-only or empty intervals. Retained lengths must include an explicit
// zero request price while dropping shapes without a request amount pointer.
func TestFilterValidRequestIntervals(t *testing.T) {
	tests := []struct {
		name      string                    // interval shape case label
		intervals []routing.PricingInterval // supplied interval shape collection
		wantLen   int                       // expected retained interval count
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

// TestModelPricingEntryHasEffectivePricingIsModeAware tests amount and
// modifier presence across declared billing modes. Wrong-mode interval fields
// are ineffective, while explicit zero amounts and the supplied token Fast
// modifier qualify.
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

// TestApplyTokenOverrides_FlatSetsImageOutputPriceExplicit supplies manual
// text prices while omitting image output. Resolution must mark image output
// explicit with zero, retaining configuration absence as the selected zero
// image rate.
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

// TestApplyTokenOverrides_FlatWithImageOutputPriceSetsExplicit supplies a
// manual image-output amount alongside text rates. Resolution must retain the
// explicit image marker and the supplied image price for subsequent token
// calculation.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 50e-6, resolved.BasePricing.ImageOutputPricePerToken, 1e-12)
}

// TestApplyTokenOverrides_FlatUsesPricingConfigImageInputPrice supplies a
// manual image-input bucket with text input pricing. Resolution must select
// configuration source and expose the configured image-input amount in its
// base pricing.
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
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 8e-6, resolved.BasePricing.ImageInputPricePerToken, 1e-12)
}

// TestIntervalToModelPricingWithBaseClearsUnsetPricingConfigImageInputPrice
// supplies a base image-input amount but leaves that card bucket absent.
// Interval conversion must clear the inherited image-input rate for
// configuration-controlled image pricing.
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

// TestIntervalToModelPricingWithBaseAppliesMultipliersAndPreservesTierRatio
// applies separate text and cache factors to base rates with doubled priority
// buckets. Standard and priority results must retain each bucket's factor,
// with supplied Fast/Flex metadata present.
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

	// Allow float multiplication residue in bucket rates and retained modifier
	// factors.
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

// TestApplyTokenOverrides_IntervalSetsImageOutputPriceExplicit configures text
// intervals with omitted image output. Both the resolution's base pricing and
// its selected interval must retain an explicit zero image-output bucket.
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

	// Keep explicit image zero in both the default and interval views; otherwise
	// an absent image amount could borrow text-output pricing during calculation.
	require.True(t, resolved.BasePricing.ImageOutputPriceExplicit)
	require.Equal(t, 0.0, resolved.BasePricing.ImageOutputPricePerToken)

	pricing := billingpricing.GetIntervalPricing(resolved, 50000)
	require.True(t, pricing.ImageOutputPriceExplicit)
	require.Equal(t, 0.0, pricing.ImageOutputPricePerToken)
}

// TestIntervalToModelPricingWithBaseClearsImageOutputWhenPricingConfigUnset
// supplies a nonzero base image-output amount and no card replacement.
// Interval conversion must produce explicit zero image output, making card
// control visible.
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

// TestApplyTokenOverrides_FlatDoesNotPolluteFallbackPrices resolves manual
// text overrides over a supplied base-price map. The resolved rates change
// while that map's text amounts and image explicit marker retain their initial
// values.
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

	require.NotNil(t, resolved)
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 10e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 50e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)

	fp := prices["claude-sonnet-4"]
	require.InDelta(t, 3e-6, fp.InputPricePerToken, 1e-12, "fallback InputPricePerToken polluted")
	require.InDelta(t, 15e-6, fp.OutputPricePerToken, 1e-12, "fallback OutputPricePerToken polluted")
	require.False(t, fp.ImageOutputPriceExplicit, "fallback ImageOutputPriceExplicit polluted")
}

// TestApplyTokenOverrides_IntervalDoesNotPolluteFallbackPrices resolves a card
// containing token intervals over a supplied base map. It checks the resolved
// base image marker and unchanged stored text rates/marker, without querying
// interval pricing.
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

	fp := prices["claude-sonnet-4"]
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 3e-6, fp.InputPricePerToken, 1e-12, "fallback InputPricePerToken polluted")
	require.InDelta(t, 15e-6, fp.OutputPricePerToken, 1e-12, "fallback OutputPricePerToken polluted")
	require.False(t, fp.ImageOutputPriceExplicit, "fallback ImageOutputPriceExplicit polluted")
}

// TestResolve_SharedPricingIsAuthoritative supplies explicit shared input and
// output amounts for a known model. Resolution must report configuration
// source and return both manual rates for that group request.
func TestResolve_SharedPricingIsAuthoritative(t *testing.T) {
	r := newResolverWithPricingConfig(t, []routing.ModelPricingEntry{{
		Models: []string{"claude-sonnet-4"}, BillingMode: routing.BillingModeToken,
		InputPrice: testPtrFloat64(10e-6), OutputPrice: testPtrFloat64(20e-6),
	}})

	resolved := r.Resolve(context.Background(), billing.PricingInput{Model: "claude-sonnet-4", GroupID: billingtestkit.GroupID()})

	require.Equal(t, billingpricing.PricingSourceConfig, resolved.Source)
	// Allow binary representation residue in these per-token rate comparisons.
	require.InDelta(t, 10e-6, resolved.BasePricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 20e-6, resolved.BasePricing.OutputPricePerToken, 1e-12)
}

// TestResolve_ConfigIntervalsOverridePresetRegardlessOfToggle supplies
// built-in tier metadata plus manual intervals and flips the long-context
// setting. The same configured long interval must remain selected while the
// base threshold and input multiplier stay available.
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
	// Allow float residue when comparing selected rates and the retained input
	// factor.
	require.InDelta(t, 18e-6, billingpricing.GetIntervalPricing(resolved, 300000).InputPricePerToken, 1e-12)
	require.Equal(t, 200000, resolved.BasePricing.LongContextInputThreshold)

	// Change only the setting between resolutions, leaving manual intervals and
	// catalog tier metadata fixed. The repeated quote therefore isolates
	// enablement from configured interval precedence.
	source.settings.LongContextPricingEnabled = true
	resolved = r.Resolve(context.Background(), billing.PricingInput{Model: "claude-sonnet-4", GroupID: billingtestkit.GroupID()})
	require.True(t, resolved.LongContextPricingEnabled)
	require.Len(t, resolved.Intervals, 2)
	require.InDelta(t, 18e-6, billingpricing.GetIntervalPricing(resolved, 300000).InputPricePerToken, 1e-12)
	require.Equal(t, 200000, resolved.BasePricing.LongContextInputThreshold)
	require.InDelta(t, 2.0, resolved.BasePricing.LongContextInputMultiplier, 1e-12)
}

// testPtrInt returns independently addressed integer storage for context
// interval endpoint fixtures.
func testPtrInt(v int) *int { return &v }

// newResolverWithPricingConfig constructs the active group-100 card fixture
// with a fresh Claude base calculator. It delegates to testkit and marks its
// caller as a test helper; supplied cards remain stable while the repository
// reads them.
func newResolverWithPricingConfig(t *testing.T, pricing []routing.ModelPricingEntry) *billing.PriceResolver {
	t.Helper()
	return billingtestkit.ResolverWithCards(t, billingtestkit.ResolverCalculator(), pricing)
}
