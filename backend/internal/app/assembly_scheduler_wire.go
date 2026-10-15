//go:build wireinject

package app

import (
	"github.com/google/wire"
)

// assembly_scheduler_wire.go registers scheduler resources, provider
// diagnostics, and administrator default readers with Wire.
// Provider selection and administrator diagnostics use the same eligibility
// rules and runtime scheduling feedback. Scheduling constructors supply those
// shared resources, while diagnostic and default-parameter readers connect
// management views to the scheduler assembled for gateway requests.

// schedulerAssemblyProviders supplies scheduling resources, provider
// diagnostics, and administrator default-parameter readers to the application
// graph.
var schedulerAssemblyProviders = wire.NewSet(
	schedulerProviders,
	provideProviderDiagnostics,
	provideSchedulerAdminDefaults,
)
