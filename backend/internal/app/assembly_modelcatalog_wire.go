//go:build wireinject

package app

import "github.com/google/wire"

// modelCatalogAssemblyProviders supplies the catalog instance shared by pricing and model-attribute readers.
var modelCatalogAssemblyProviders = wire.NewSet(
	provideModelCatalogService,
	provideModelCatalogRemoteClient,
)
