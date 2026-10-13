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

// Catalog fixtures retain the full model-name entries supplied by a test,
// while omitted calculator inputs load historical token and media amounts.
// Marketplace and recorder adapters bind these prices to controllable
// repositories so cross-module assertions can inspect quotes, captured
// settlement commands, and usage rows.

// marketplaceFixtureGroups adapts a group repository into copied
// marketplace group values.
type marketplaceFixtureGroups struct {
	// source is borrowed for ListActive calls; callers provide a nonnil
	// repository before using this adapter.
	source routing.GroupRepository // active group repository
}

// marketplaceFixturePrices connects calculator-backed pricing with optional
// configured marketplace quote resolution.
type marketplaceFixturePrices struct {
	// calculator and resolver connect marketplace quotes to billing inputs.
	// Modality lookup reads calculator, while Quote selects resolver or
	// constructs a calculator-backed resolver.
	calculator *billing.Calculator    // underlying billing calculator
	resolver   *billing.PriceResolver // configured quote resolver
}

// catalogFixture supplies full-name catalog price records for
// a snapshot-backed service.
type catalogFixture struct {
	// pricingData becomes the service's retained snapshot. Callers leave this
	// map and its referenced records unchanged while the catalog
	// fixture is queried.
	//
	// full-name catalog price entries
	pricingData map[string]*pricing.CatalogModelPricing
}

// newCalculator constructs a calculator from the supplied catalog service. A
// nil catalog selects the shared historical token and media fixtures because
// the normalized price-map argument is also nil.
func newCalculator(catalog *provider.Service) *billing.Calculator {
	return newCalculatorWithPrices(catalog, nil)
}

// newCalculatorWithPrices delegates calculator construction to the shared
// billing testkit. Both nil inputs select historical token and media fixtures;
// with a nil catalog, a nonnil empty map disables those defaults.
//
// Existing catalog names win over supplied prices, and nil price entries are
// skipped. Modifier pointers from newly converted entries remain shared, so
// callers keep those values stable during calculation. Embedded fixture
// decoding failures panic.
func newCalculatorWithPrices(catalog *provider.Service, prices map[string]*pricing.ModelPricing) *billing.Calculator {
	return testkit.Calculator(catalog, prices)
}

// contractProviderStatsCost resolves provider cost with an empty group-mapped
// model. It delegates custom and catalog selection to
// contractProviderStatsWithMapping; totalCost is accepted by the fixture
// signature and contributes no amount.
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

// contractProviderStatsWithMapping builds a provider-statistics resolver from
// the supplied configuration service and calculator. Qoder enables
// request-name preference and uses the first optional effort, or empty effort
// when omitted. totalCost is an unused fixture argument. An absent
// configuration service returns nil even with a calculator; a missing
// calculator still permits configured custom rules. A nil result leaves
// provider cost unset, while configured free pricing can return a nonnil zero
// pointer.
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

// newTestPricingConfigServiceForStats constructs a configuration service for
// one group and platform using testkit data. Callers supply a nonnil
// configuration; the routing testkit clones its pricing and supported policy
// collections before associating the requested group.
func newTestPricingConfigServiceForStats(t *testing.T, pricingConfig *routingtestkit.Configuration, groupID int64, platform string) *routing.PricingConfigService {
	t.Helper()
	cache := routingtestkit.NewModelConfigData()
	cache.ByGroup[groupID] = pricingConfig
	cache.Platforms[groupID] = platform

	cache.LoadedAt = time.Now()
	cs := routingtestkit.ModelConfigFromData(cache)
	return cs
}

// newPricingMarketplaceFixture constructs marketplace requestability and price
// adapters around the supplied services. A nil calculator omits prices even
// when a resolver is provided; nil configuration leaves the marketplace
// timezone option empty.
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

// ListActive reads active groups from a nonnil repository and returns
// routing.CloneGroup copies of each result. A nil repository slice stays nil,
// and the read error accompanies either nil or the copied groups. Nested
// feature values follow CloneGroup's supported-copy rules.
func (g marketplaceFixtureGroups) ListActive(ctx context.Context) ([]routing.Group, error) {
	v, err := g.source.ListActive(ctx)
	if v == nil {
		return nil, err
	}
	// CloneGroup detaches declared collections and supported nested feature
	// values before marketplace use. Custom dynamic feature values can remain
	// shared under the group copier's rules.
	out := make([]routing.Group, len(v))
	for i := range v {
		out[i] = *routing.CloneGroup(&v[i])
	}
	return out, err
}

// Quote resolves a marketplace model quote using the fixture's resolver and
// group rate. An absent resolver creates a calculator-backed resolver with
// production identity lookup; FreeFastApplicable is forwarded
// to public quoting.
func (p marketplaceFixturePrices) Quote(ctx context.Context, req routing.MarketplaceQuoteRequest) pricing.ModelDisplayPricing {
	resolver := p.resolver
	// An omitted quote resolver still uses the supplied calculator's catalog.
	// Bind production identity lookup here so this path follows
	// complete-model price resolution.
	if resolver == nil {
		resolver = billing.NewPriceResolver(nil, p.calculator, modelidentity.Identity, func(model string, err error) {
			slog.Debug("failed to get model pricing from model catalog, using fallback", "model", model, "error", err)
		})
	}
	return resolver.PublicQuote(ctx, billing.PublicQuoteInput{PricingInput: billing.PricingInput{Model: req.Model, GroupID: &req.GroupID}, RateMultiplier: req.RateMultiplier, FreeFastApplicable: req.FreeFastApplicable})
}

// GetModelModalities reads input and output modalities through the fixture
// calculator. A nil calculator or a catalog without modality support returns
// nil lists; other results follow the calculator's modality reader.
func (p marketplaceFixturePrices) GetModelModalities(model string) ([]string, []string) {
	return p.calculator.GetModelModalities(model)
}

// newCatalogFixture constructs a query service from full-name catalog entries.
// The service takes ownership of pricingData, so callers keep the map and
// referenced model records unchanged while its queries are used.
func newCatalogFixture(fixture catalogFixture) *provider.Service {
	return provider.NewServiceFromSnapshot(provider.Options{
		ModelLookupCandidates: modelidentity.CandidatesFactory,
	}, nil, provider.Snapshot{Data: fixture.pricingData})
}

// newOpenAIRecordUsageServiceForTest constructs a recording fixture with a
// fresh settlement store and default price resolver. The supplied user and
// subscription repositories are unused; rates feed the
// shared group-rate resolver.
func newOpenAIRecordUsageServiceForTest(logs usagecore.UsageLogRepository, _ identity.UserRepository, _ billing.UserSubscriptionRepository, rates billing.UserGroupRateRepository) *completiontestkit.Recording {
	return completiontestkit.NewRecording(logs, &completiontestkit.SettlementStore{}, rates, true)
}

// newOpenAIRecordUsageServiceWithBillingRepoForTest constructs a recording
// fixture using the supplied settlement store and rates. Price resolution
// remains unset for callers to configure; the user and subscription repository
// arguments are unused.
func newOpenAIRecordUsageServiceWithBillingRepoForTest(logs usagecore.UsageLogRepository, funds completion.Store, _ identity.UserRepository, _ billing.UserSubscriptionRepository, rates billing.UserGroupRateRepository) *completiontestkit.Recording {
	return completiontestkit.NewRecording(logs, funds, rates, false)
}

// newGatewayRecordUsageServiceWithBillingRepoForTest constructs a Messages
// recording fixture using the supplied settlement store. It supplies no
// user-group rates or price resolver, and the user/subscription repository
// arguments are unused.
func newGatewayRecordUsageServiceWithBillingRepoForTest(logs usagecore.UsageLogRepository, funds completion.Store, _ identity.UserRepository, _ billing.UserSubscriptionRepository) *completiontestkit.Recording {
	return completiontestkit.NewRecording(logs, funds, nil, false)
}

// testPtrFloat64 returns an allocated pointer containing the
// supplied floating-point value.
func testPtrFloat64(v float64) *float64 { return &v }

// testPtrInt returns an allocated pointer containing the
// supplied integer value.
func testPtrInt(v int) *int { return &v }
