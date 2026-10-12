package app

import (
	"context"
	"log/slog"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// catalogueReader reads schedulable providers for one group or all groups and converts their model data.
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

// provideRequestableCatalogue binds provider reads and group policies for model resolution.
func provideRequestableCatalogue(store *providerpostgres.ProviderStore, modelConfigs *routing.PricingConfigService) *routing.RequestableCatalogue {
	return &routing.RequestableCatalogue{Read: catalogueReader(store), Resolver: routing.RequestableResolver{GroupPolicies: modelConfigs, Warn: slog.Warn}, Warn: slog.Warn}
}
