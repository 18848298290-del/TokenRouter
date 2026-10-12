//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// runtimeAssemblyProviders groups application lifecycle Wire providers.
var runtimeAssemblyProviders = wire.NewSet(
	provideModelCatalogRuntime,
	provideSettingsRuntime,
	provideAuthRuntime,
	provideMaintenanceRuntime,
	provideOpsRuntime,
	provideQueuesRuntime,
	provideJobsRuntime,
	provideCoreRuntime,
	provideRuntime,
	provideApplication,
)
