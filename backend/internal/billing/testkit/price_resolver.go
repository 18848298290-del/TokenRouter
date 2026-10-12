package testkit

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	pricingprovider "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
)

// Configured-card tests use group 100 and a fixed Claude base price to observe
// inheritance and interval selection. Resolver fixtures build the shared
// configuration service with production model-identity lookup, so manual
// cards, catalog base prices and provider statistics follow the same
// resolution entry points as application wiring.

// ResolverCalculator returns a production calculator using a fresh Claude
// fallback-price fixture.
func ResolverCalculator() *billing.Calculator {
	return Calculator(nil, ResolverFallbackPrices())
}

// ResolverFallbackPrices returns fresh normalized Claude amounts for
// card-inheritance tests. The map and price record are allocated for each
// call, allowing callers to modify those fixture values without affecting
// later fixtures.
func ResolverFallbackPrices() map[string]*billingpricing.ModelPricing {
	return map[string]*billingpricing.ModelPricing{"claude-sonnet-4": {
		InputPricePerToken:         3e-6,
		OutputPricePerToken:        15e-6,
		CacheCreationPricePerToken: 3.75e-6,
		CacheReadPricePerToken:     0.3e-6,
		SupportsCacheBreakdown:     false,
	}}
}

// ResolverWithCards constructs a resolver for group 100 with the supplied
// configured cards and base calculator. The repository fixture exposes one
// active configuration; production identity lookup and pricing location
// loading are retained for these card-resolution tests.
func ResolverWithCards(t *testing.T, bs *billing.Calculator, pricing []routing.ModelPricingEntry) *billing.PriceResolver {
	t.Helper()
	const groupID = 100
	platform := capability.PlatformAnthropic
	repo := &routingtestkit.PricingConfigRepositoryStub{
		ListAllFn: func(_ context.Context) ([]routingtestkit.Configuration, error) {
			return []routingtestkit.Configuration{{
				ID:           1,
				Name:         "test-price-config",
				Status:       billing.StatusActive,
				GroupIDs:     []int64{groupID},
				ModelPricing: pricing,
			}}, nil
		},
		GetGroupPlatformsFn: func(_ context.Context, _ []int64) (map[int64]string, error) {
			return map[int64]string{groupID: platform}, nil
		},
	}
	cs := routingtestkit.NewPricingConfigService(repo, nil, routing.PricingConfigOptions{
		Warn: slog.
			Warn,
		Now: time.
			Now, LoadLocation: pricingprovider.
			LoadPricingLocation,
	},
	)
	return PriceResolver(cs, bs)
}

// GroupID returns a fresh pointer to the resolver fixture's group identifier.
func GroupID() *int64 { v := int64(100); return &v }

// PriceResolver binds the supplied configuration service and calculator into a
// production resolver. Nil configuration omits both card and statistics
// sources; model identity lookup and unavailable-base observation remain wired
// through production adapters.
func PriceResolver(pricingConfigs *routing.PricingConfigService, calculator *billing.Calculator) *billing.PriceResolver {
	var source billing.ConfigPrices
	var stats billing.ProviderStatsSource
	if pricingConfigs != nil {
		source = pricingConfigs
		stats = gatewayprovider.ProviderStatsSource{Service: pricingConfigs}
	}
	return billing.NewPriceResolver(source, calculator, modelidentity.Identity, func(model string, err error) {
		slog.Debug("failed to get model pricing from model catalog, using fallback", "model", model, "error", err)
	}, stats)
}

// SharedPriceResolver constructs an active configuration containing the
// supplied group, settings and cards. Its configuration service supplies both
// effective user cards and provider statistics, while the caller-provided
// calculator remains the catalog base source.
func SharedPriceResolver(calculator *billing.Calculator, groupID int64, settings billingpricing.BillingSettings, cards []routing.ModelPricingEntry) *billing.PriceResolver {
	rows := []routingtestkit.Configuration{{ID: groupID, Status: routing.StatusActive, GroupIDs: []int64{groupID}, BillingSettings: &settings, ModelPricing: cards}}
	configs := routingtestkit.ModelConfigFromData(routingtestkit.ModelConfigDataFromRows(rows, nil))
	return PriceResolver(configs, calculator)
}
