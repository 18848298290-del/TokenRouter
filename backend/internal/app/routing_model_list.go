package app

import (
	"context"
	"log/slog"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// routing_model_list.go builds requestable model catalogues from provider
// candidates and shared group policies. Requestable model queries read provider
// candidates for one requested group or the full schedulable set. The catalogue
// reader converts those records into routing data and pairs them with the
// shared group-policy resolver for later model admission and display.

// catalogueReader returns provider catalogue reads bound to shared storage. A
// nonnil group ID selects its schedulable providers; nil selects all
// schedulable providers, and repository errors stop conversion before routing
// data is returned.
func catalogueReader(store *providerpostgres.ProviderStore) func(context.Context, *int64) ([]routing.CatalogueProvider, error) {
	return func(ctx context.Context, id *int64) ([]routing.CatalogueProvider, error) {
		var values []provider.Record
		var err error
		if id != nil {
			values, err = store.ListSchedulableByGroupID(ctx, *id)
		} else {
			values, err = store.ListSchedulable(ctx)
		}
		if err != nil {
			return nil, err
		}
		return gatewayprovider.CatalogueProviders(values), nil
	}
}

// provideRequestableCatalogue combines provider candidate reads with shared
// group-policy model resolution.
func provideRequestableCatalogue(store *providerpostgres.ProviderStore, modelConfigs *routing.PricingConfigService, attributes *routing.ModelAttributeService) *routing.RequestableCatalogue {
	return &routing.RequestableCatalogue{Read: catalogueReader(store), Resolver: routing.RequestableResolver{Attributes: attributes, GroupPolicies: modelConfigs, Warn: slog.Warn}, Warn: slog.Warn}
}
