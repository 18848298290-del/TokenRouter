//go:build wireinject

package app

import (
	"github.com/google/wire"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	keyredis "github.com/TokenFlux/TokenRouter/internal/apikey/rediscache"
)

// apikeyAssemblyProviders groups Wire providers for the apikey module.
var apikeyAssemblyProviders = wire.NewSet(
	provideKeyAdminHTTP,
	provideKeyHTTP,
	provideKeyAdmin,
	provideKeyStore,
	provideKeyRepository,
	provideKeys,
	provideKeyInvalidator,
	apikey.ProvideAuthCacheInvalidationWorker,
	keyredis.NewAPIKeyCache,
	keypostgres.NewAuthCacheInvalidationOutboxRepository,
	provideAPIKeyAuth,
)
