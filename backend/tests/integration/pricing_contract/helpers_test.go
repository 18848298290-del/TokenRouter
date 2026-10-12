package pricingcontract

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	completiontestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	usagecore "github.com/TokenFlux/TokenRouter/internal/usage"
)

type marketplaceFixtureGroups struct{ source routing.GroupRepository }

type marketplaceFixturePrices struct {
	calculator *billing.Calculator
	resolver   *billing.PriceResolver
}

// catalogFixture holds model prices supplied by a test.
type catalogFixture struct {
	pricingData map[string]*pricing.CatalogModelPricing
}

// newCalculator constructs a calculator from the test catalog.
func newCalculator(catalog *provider.Service) *billing.Calculator {
	return newCalculatorWithPrices(catalog, nil)
}

func newCalculatorWithPrices(catalog *provider.Service, prices map[string]*pricing.ModelPricing) *billing.Calculator {
	return testkit.Calculator(catalog, prices)
}

// contractProviderStatsCost resolves custom provider rules before catalog prices.
// An unavailable cost returns nil so the log layer can choose its fallback.
// Qoder rules prefer the requested model; contractProviderStatsWithMapping also accepts the group-mapped model.
// totalCost is retained in the fixture signature but does not contribute to provider cost.
func contractProviderStatsCost(
	ctx context.Context,
	pricingConfigService *routing.PricingConfigService,
	billingService *billing.Calculator,
	actualPlatform string,
	providerID int64,
	groupID int64,
	upstreamModel string,
	requestedModel string,
	tokens pricing.UsageTokens,
	requestCount int,
	totalCost float64,
	serviceTier string,
	reasoningEfforts ...string,
) *float64 {
	return contractProviderStatsWithMapping(ctx, pricingConfigService, billingService, actualPlatform, providerID, groupID, upstreamModel, requestedModel, "", tokens, requestCount, totalCost, serviceTier, reasoningEfforts...)
}

// contractProviderStatsWithMapping resolves provider costs through billing.
func contractProviderStatsWithMapping(
	ctx context.Context,
	pricingConfigService *routing.PricingConfigService,
	billingService *billing.Calculator,
	actualPlatform string,
	providerID int64,
	groupID int64,
	upstreamModel string,
	requestedModel string,
	groupMappedModel string,
	tokens pricing.UsageTokens,
	requestCount int,
	totalCost float64,
	serviceTier string,
	reasoningEfforts ...string,
) *float64 {
	effort := ""
	if len(reasoningEfforts) > 0 {
		effort = reasoningEfforts[0]
	}
	var source billing.ProviderStatsSource
	if pricingConfigService != nil {
		source = gatewayprovider.ProviderStatsSource{Service: pricingConfigService}
	}
	var calculator *billing.Calculator
	if billingService != nil {
		calculator = billingService
	}
	resolver := billing.NewPriceResolver(nil, calculator, nil, nil, source)
	return resolver.ResolveProviderStats(ctx, billing.ProviderStatsCostInput{PreferRequestedModel: actualPlatform == "qoder", ProviderID: providerID, GroupID: groupID, UpstreamModel: upstreamModel, RequestedModel: requestedModel, MappedModel: groupMappedModel, Tokens: tokens, RequestCount: requestCount, ServiceTier: serviceTier, ReasoningEffort: effort})
}

// newTestPricingConfigServiceForStats wires one shared pricing configuration to the given group.
func newTestPricingConfigServiceForStats(t *testing.T, pricingConfig *routingtestkit.Configuration, groupID int64, platform string) *routing.PricingConfigService {
	t.Helper()
	cache := routingtestkit.NewModelConfigData()
	cache.ByGroup[groupID] = pricingConfig
	cache.Platforms[groupID] = platform

	cache.LoadedAt = time.Now()
	cs := routingtestkit.ModelConfigFromData(cache)
	return cs
}

// newPricingMarketplaceFixture wires the requestable catalog and billing quotes into the marketplace.
func newPricingMarketplaceFixture(groupRepo routing.GroupRepository, settingRepo settings.Repository, resolver *billing.PriceResolver, billingService *billing.Calculator, capacityService *routing.CapacityService, availabilityRepo routing.GroupAvailabilityProbeRepository, cfg *config.Config) *routing.Marketplace {
	projection := routing.RequestableResolver{Warn: slog.Warn}
	source := &routing.RequestableCatalogue{Resolver: projection, Warn: slog.Warn}
	var prices routing.MarketplacePrices
	if billingService != nil {
		prices = marketplaceFixturePrices{billingService, resolver}
	}
	timezone := ""
	if cfg != nil {
		timezone = cfg.Timezone
	}
	var groups routing.MarketplaceGroups
	if groupRepo != nil {
		groups = marketplaceFixtureGroups{groupRepo}
	}
	var capacity routing.MarketplaceCapacity
	if capacityService != nil {
		capacity = capacityService
	}
	return routing.NewMarketplace(groups, settingRepo, source, projection, prices, capacity, availabilityRepo, routing.MarketplaceOptions{Timezone: timezone, Now: time.Now, Warn: slog.Warn})
}

func (g marketplaceFixtureGroups) ListActive(ctx context.Context) ([]routing.Group, error) {
	v, err := g.source.ListActive(ctx)
	if v == nil {
		return nil, err
	}
	out := make([]routing.Group, len(v))
	for i := range v {
		out[i] = *routing.CloneGroup(&v[i])
	}
	return out, err
}

func (p marketplaceFixturePrices) Quote(ctx context.Context, req routing.MarketplaceQuoteRequest) pricing.ModelDisplayPricing {
	resolver := p.resolver
	if resolver == nil {
		resolver = billing.NewPriceResolver(nil, p.calculator, modelidentity.Identity, func(model string, err error) {
			slog.Debug("failed to get model pricing from model catalog, using fallback", "model", model, "error", err)
		})
	}
	return resolver.PublicQuote(ctx, billing.PublicQuoteInput{PricingInput: billing.PricingInput{Model: req.Model, GroupID: &req.GroupID}, RateMultiplier: req.RateMultiplier, FreeFastApplicable: req.FreeFastApplicable})
}

func (p marketplaceFixturePrices) GetModelModalities(model string) ([]string, []string) {
	return p.calculator.GetModelModalities(model)
}

func newCatalogFixture(fixture catalogFixture) *provider.Service {
	return provider.NewServiceFromSnapshot(provider.Options{
		ModelLookupCandidates: modelidentity.CandidatesFactory,
	}, nil, provider.Snapshot{Data: fixture.pricingData})
}

// newOpenAIRecordUsageServiceForTest constructs a completion recording fixture with the test stores.
func newOpenAIRecordUsageServiceForTest(logs usagecore.UsageLogRepository, _ identity.UserRepository, _ billing.UserSubscriptionRepository, rates billing.UserGroupRateRepository) *completiontestkit.Recording {
	return completiontestkit.NewRecording(logs, &completiontestkit.SettlementStore{}, rates, true)
}

func newOpenAIRecordUsageServiceWithBillingRepoForTest(logs usagecore.UsageLogRepository, funds completion.Store, _ identity.UserRepository, _ billing.UserSubscriptionRepository, rates billing.UserGroupRateRepository) *completiontestkit.Recording {
	return completiontestkit.NewRecording(logs, funds, rates, false)
}

func newGatewayRecordUsageServiceWithBillingRepoForTest(logs usagecore.UsageLogRepository, funds completion.Store, _ identity.UserRepository, _ billing.UserSubscriptionRepository) *completiontestkit.Recording {
	return completiontestkit.NewRecording(logs, funds, nil, false)
}

// testPtrFloat64 returns a pointer to v.
func testPtrFloat64(v float64) *float64 { return &v }

// testPtrInt returns a pointer to v.
func testPtrInt(v int) *int { return &v }
