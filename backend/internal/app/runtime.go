package app

import "github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"

// runtimeReady makes Wire register runtime resources before returning the application.
type runtimeReady struct{}

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
