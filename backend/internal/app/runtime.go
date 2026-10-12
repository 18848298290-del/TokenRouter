package app

import "github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"

// Application construction depends on registration of subsystem lifecycle
// hooks and HTTP idempotency bindings. The readiness arguments make Wire
// resolve those constructors before provideApplication receives its runtime
// marker; creating the marker itself starts no subsystem.

// runtimeReady marks resolution of application runtime registration dependencies for Wire.
type runtimeReady struct{}

// provideRuntime returns the application readiness marker after Wire resolves
// its registration dependencies. The marker orders construction of the graph;
// actual loading and background startup occur when the lifecycle manager runs
// the registered hooks.
func provideRuntime(
	_ *modelCatalogRuntimeReady,
	_ *settingsRuntimeReady,
	_ *authRuntimeReady,
	_ *maintenanceRuntimeReady,
	_ *opsRuntimeReady,
	_ *queuesRuntimeReady,
	_ *jobsRuntimeReady,
	_ *coreRuntimeReady,
	_ *idempotencyHTTPReady,
	_ *promptpolicy.Service,
) *runtimeReady {
	return &runtimeReady{}
}
