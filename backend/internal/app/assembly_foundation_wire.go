//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// foundationAssemblyProviders groups infrastructure and storage Wire providers.
var foundationAssemblyProviders = wire.NewSet(
	provideCalendar,
	cacheProviders,
	provideEnt,
	provideRedis,
	storageProviders,
	moduleStorageProviders,
)
