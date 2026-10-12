//go:build wireinject

package app

import "github.com/google/wire"

// Billing prices and routing model attributes read the same model catalog
// service. Its remote client follows the configured update proxy and direct
// connection policy, while lifecycle registration manages catalog
// initialization and periodic updates.

// modelCatalogAssemblyProviders supplies the model catalog and remote client
// used by application pricing and attribute readers.
var modelCatalogAssemblyProviders = wire.NewSet(
	provideModelCatalogService,
	provideModelCatalogRemoteClient,
)
