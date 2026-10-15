//go:build wireinject

package app

import (
	"github.com/google/wire"

	creativeprovider "github.com/TokenFlux/TokenRouter/internal/creative/provider"
	routinghttp "github.com/TokenFlux/TokenRouter/internal/routing/httpapi"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
)

// assembly_routing_wire.go registers routing administration and catalog
// constructors and binds group storage for creative execution.
// Group administration, model attributes, and price configuration supply
// information consumed during routing. The application set also binds the
// persisted group store for creative execution, so creative jobs and route
// administration resolve the same group storage implementation.

// routingAssemblyProviders supplies group, pricing, and model-catalog
// administration and binds group storage for creative execution.
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
