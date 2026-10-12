//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// schedulerAssemblyProviders groups Wire providers for the scheduler module.
var schedulerAssemblyProviders = wire.NewSet(
	schedulerProviders,
	provideProviderDiagnostics,
	provideSchedulerAdminDefaults,
)
