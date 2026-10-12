//go:build wireinject

package app

import (
	"github.com/google/wire"

	creativeprovider "github.com/TokenFlux/TokenRouter/internal/creative/provider"
	routinghttp "github.com/TokenFlux/TokenRouter/internal/routing/httpapi"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
)

// routingAssemblyProviders groups Wire providers for the routing module.
var routingAssemblyProviders = wire.NewSet(
	wire.Bind(new(creativeprovider.ExecutionGroups), new(*routingpostgres.GroupStore)),
	provideRoutingSettings,
	provideAdminModelCatalog,
	provideRequestableCatalogue,
	provideMarketplace,
	provideMarketplaceHTTP,
	provideGroupCapacity,
	provideGroupProbeRunner,
	provideRoutingGroupHTTP,
	provideRoutingGroupStore,
	provideGroupReader,
	provideRoutingGroupAdmin,
	providePricingConfigService,
	providePricingCatalog,
	provideModelAttributes,
	routingpostgres.NewModelAttributeStore,
	routinghttp.NewModelAttributeHandler,
	routingpostgres.NewPricingConfigStore,
	routinghttp.NewPricingHandler,
)
