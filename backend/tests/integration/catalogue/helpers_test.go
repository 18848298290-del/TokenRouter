package catalogue_test

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Catalogue scenarios prepare production routing rules from in-memory provider
// records and connect those rules to billing quotes. The repository substitutes
// copy provider slice storage while retaining nested references, so tests update
// their fixtures sequentially before the next read.

// catalogueRows supplies provider pools for catalogue resolution and marketplace prefetch.
type catalogueRows interface {
	// ListSchedulable returns the global provider pool used for catalogue prefetch.
	// The assembly prepares records from this pool; implementations return an
	// error when the query cannot supply its candidates.
	ListSchedulable(context.Context) ([]provider.Record, error)
	// ListSchedulableByGroupID returns the provider pool for one requested group.
	// The assembly prepares its catalogue rules from these records, and a query
	// error produces a restricted empty catalogue.
	ListSchedulableByGroupID(context.Context, int64) ([]provider.Record, error)
}

// modelsListProviderRepoStub supplies mutable provider pools and counts catalogue queries.
type modelsListProviderRepoStub struct {
	// byGroup and all back separate query paths. Returned slices get new storage,
	// but nested provider references remain shared; tests populate or replace
	// these rows before issuing the next query.
	byGroup map[int64][]provider.Record // group query fixture pools
	all     []provider.Record           // global query fixture pool
	// err takes priority over both row sources and is returned after recording
	// a query, allowing failure tests to assert attempted reads.
	err error
	// listByGroupCalls and listAllCalls count attempted reads of their respective
	// pools. Atomic counters protect the counts; fixture maps and records still
	// require sequential access by the tests.
	listByGroupCalls atomic.Int64 // attempted group query count
	listAllCalls     atomic.Int64 // attempted global query count
}

// catalogueFixture pairs requestable model resolution with optional marketplace billing lookup.
type catalogueFixture struct {
	*routing.RequestableCatalogue // prepared catalogue query service
	// prices is the optional resolver retained for marketplace quotes. It is
	// consulted by newCatalogueMarketplace when that helper receives a nonnil
	// calculator.
	prices *billing.PriceResolver
}

// providerStatsSource adapts routing price configurations to billing statistics lookup.
type providerStatsSource struct {
	// pricingConfigs is borrowed from catalogue assembly and must remain nonnil
	// when billing invokes the ProviderStatsGroup method on this adapter.
	pricingConfigs *routing.PricingConfigService
}

// cataloguePrices adapts billing quotes and model modalities to marketplace lookup.
type cataloguePrices struct {
	// resolver and calculator are borrowed from marketplace assembly. Quote uses
	// resolver's billing data, and GetModelModalities delegates metadata lookup
	// to calculator; constructing the adapter retains both supplied services.
	resolver   *billing.PriceResolver // public quote lookup service
	calculator *billing.Calculator    // catalogue modality lookup service
}

// ListSchedulableByGroupID counts the query and returns a shallow copy of the
// configured group pool. Missing groups return nil, injected errors take priority,
// and nested provider collections remain shared with the fixture.
func (s *modelsListProviderRepoStub) ListSchedulableByGroupID(_ context.Context, id int64) ([]provider.Record, error) {
	s.listByGroupCalls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	values, ok := s.byGroup[id]
	if !ok {
		return nil, nil
	}
	out := make([]provider.Record, len(values))
	copy(out, values)
	return out, nil
}

// ListSchedulable counts the query and returns a shallow copy of the global
// provider pool. An injected error suppresses rows, while returned records retain
// references to the fixture's nested collections.
func (s *modelsListProviderRepoStub) ListSchedulable(context.Context) ([]provider.Record, error) {
	s.listAllCalls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	out := make([]provider.Record, len(s.all))
	copy(out, s.all)
	return out, nil
}

// newCatalogueFixture connects provider queries to the production requestable
// resolver and retains an optional price resolver for marketplace quotes. Nil
// rows leave the catalogue reader unset; a nil pricing service leaves group
// policy lookup unset. The fixture supplies missing authentication types before
// preparing each provider's execution rules. Prepared rules retain the returned
// provider records, whose nested values can share fixture storage. Tests finish
// fixture mutations before querying and keep the injected services available
// for subsequent marketplace calls.
func newCatalogueFixture(rows catalogueRows, pricingConfigs *routing.PricingConfigService, prices *billing.PriceResolver) *catalogueFixture {
	var read func(context.Context, *int64) ([]routing.CatalogueProvider, error)
	if rows != nil {
		read = func(ctx context.Context, id *int64) ([]routing.CatalogueProvider, error) {
			var values []provider.Record
			var err error
			if id != nil {
				values, err = rows.ListSchedulableByGroupID(ctx, *id)
			} else {
				values, err = rows.ListSchedulable(ctx)
			}
			if err != nil {
				return nil, err
			}
			// Sparse fixtures omit authentication types. Supply Cosy for Qoder,
			// OAuth for Antigravity, and API-key for other platforms so production
			// eligibility rules receive the intended fixture provider kind.
			for index := range values {
				if values[index].Type == "" {
					switch values[index].Platform {
					case provider.PlatformQoder:
						values[index].Type = provider.ProviderTypeCosy
					case provider.PlatformAntigravity:
						values[index].Type = provider.ProviderTypeOAuth
					default:
						values[index].Type = provider.ProviderTypeAPIKey
					}
				}
			}
			return gatewayprovider.CatalogueProviders(values), nil
		}
	}
	var pricingConfigPort routing.CataloguePolicies
	if pricingConfigs != nil {
		pricingConfigPort = pricingConfigs
	}
	core := &routing.RequestableCatalogue{Read: read, Resolver: routing.RequestableResolver{GroupPolicies: pricingConfigPort, Warn: slog.Warn}, Warn: slog.Warn}
	return &catalogueFixture{RequestableCatalogue: core, prices: prices}
}

// ProviderStatsGroup exposes the selected configuration's provider-statistics
// rules to billing. The injected pricing service must be nonnil; a missing
// configuration or lookup failure returns no statistics configuration.
func (s providerStatsSource) ProviderStatsGroup(ctx context.Context, id int64) (*billing.ProviderStatsPricingConfig, error) {
	v, e := s.pricingConfigs.GetPricingConfigForGroup(ctx, id)
	if e != nil || v == nil {
		return nil, e
	}
	return &billing.ProviderStatsPricingConfig{Rules: v.ProviderStatsPricingRules}, nil
}

// cataloguePriceResolver assembles billing lookup with catalogue model identity
// and the supplied calculator. A nil pricing service omits configuration and
// provider-statistics sources; the diagnostic callback logs catalogue price
// lookup failures.
func cataloguePriceResolver(pricingConfigs *routing.PricingConfigService, calculator *billing.Calculator) *billing.PriceResolver {
	var source billing.ConfigPrices
	var stats billing.ProviderStatsSource
	if pricingConfigs != nil {
		source = pricingConfigs
		stats = providerStatsSource{pricingConfigs}
	}
	return billing.NewPriceResolver(source, calculator, modelidentity.Identity, func(model string, err error) {
		slog.Debug("failed to get model pricing from model catalog, using fallback", "model", model, "error", err)
	}, stats)
}

// Quote requests the public billing price for the marketplace's resolved model
// and group. Callers supply a resolver with a calculator; the adapter forwards
// the group's rate multiplier and Fast applicability to billing.
func (p cataloguePrices) Quote(ctx context.Context, request routing.MarketplaceQuoteRequest) pricing.ModelDisplayPricing {
	return p.resolver.PublicQuote(ctx, billing.PublicQuoteInput{PricingInput: billing.PricingInput{Model: request.Model, GroupID: &request.GroupID}, RateMultiplier: request.RateMultiplier, FreeFastApplicable: request.FreeFastApplicable})
}

// GetModelModalities returns the calculator's input and output modality lists
// for the requested model. A nil calculator or one lacking catalogue modality
// lookup yields nil lists through the delegated reader.
func (p cataloguePrices) GetModelModalities(model string) ([]string, []string) {
	return p.calculator.GetModelModalities(model)
}

// newCatalogueMarketplace assembles public model listing from optional groups,
// catalogue queries, and billing services. A supplied catalogue contributes model
// reads and requestable rules. Supplying a calculator adds modality and price
// lookup, reusing catalogue.prices when present or constructing a resolver from
// that calculator. The marketplace retains these services for subsequent calls.
func newCatalogueMarketplace(groups routing.MarketplaceGroups, catalogue *catalogueFixture, calculator *billing.Calculator) *routing.Marketplace {
	var source routing.MarketplaceModels
	resolver := routing.RequestableResolver{Warn: slog.Warn}
	var priceSource routing.MarketplacePrices
	if catalogue != nil {
		source = catalogue.RequestableCatalogue
		resolver = catalogue.Resolver
	}
	if calculator != nil {
		var prices *billing.PriceResolver
		if catalogue != nil {
			prices = catalogue.prices
		}
		// Construct from this marketplace's calculator when no resolver is retained,
		// so public quotes and displayed modalities use the same injected billing source.
		if prices == nil {
			prices = cataloguePriceResolver(nil, calculator)
		}
		priceSource = cataloguePrices{prices, calculator}
	}
	return routing.NewMarketplace(groups, nil, source, resolver, priceSource, nil, nil, routing.MarketplaceOptions{Now: time.Now, Warn: slog.Warn})
}
