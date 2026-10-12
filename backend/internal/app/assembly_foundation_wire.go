//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// Ent and direct SQL consumers share the pool initialized during database
// bootstrap. The same graph supplies Redis and a Calendar captured after
// timezone initialization, so modules use common storage resources and date
// interpretation.

// foundationAssemblyProviders supplies shared application resources together
// with the storage and counter-cache provider sets.
var foundationAssemblyProviders = wire.NewSet(
	provideCalendar,
	cacheProviders,
	provideEnt,
	provideRedis,
	storageProviders,
	moduleStorageProviders,
)
