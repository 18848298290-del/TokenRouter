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

// ResolverCalculator supplies independent base prices for interval tests.
func ResolverCalculator() *billing.Calculator {
	return Calculator(nil, ResolverFallbackPrices())
}

// ResolverFallbackPrices returns a fresh price map for each fixture.
func ResolverFallbackPrices() map[string]*billingpricing.ModelPricing {
	return map[string]*billingpricing.ModelPricing{"claude-sonnet-4": {
		InputPricePerToken:         3e-6,
		OutputPricePerToken:        15e-6,
		CacheCreationPricePerToken: 3.75e-6,
		CacheReadPricePerToken:     0.3e-6,
		SupportsCacheBreakdown:     false,
	}}
}

// ResolverWithCards constructs a shared pricing configuration resolver with the given base prices.
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

// GroupID returns the group ID used by ResolverWithCards.
func GroupID() *int64 { v := int64(100); return &v }

// PriceResolver wires test inputs into the production resolver.
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

// SharedPriceResolver supplies cards and billing settings for one group through the configuration service.
func SharedPriceResolver(calculator *billing.Calculator, groupID int64, settings billingpricing.BillingSettings, cards []routing.ModelPricingEntry) *billing.PriceResolver {
	rows := []routingtestkit.Configuration{{ID: groupID, Status: routing.StatusActive, GroupIDs: []int64{groupID}, BillingSettings: &settings, ModelPricing: cards}}
	configs := routingtestkit.ModelConfigFromData(routingtestkit.ModelConfigDataFromRows(rows, nil))
	return PriceResolver(configs, calculator)
}
