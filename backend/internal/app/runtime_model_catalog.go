package app

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
)

// runtime_model_catalog.go coordinates initial catalog loading with conditional
// startup of scheduled synchronization. Startup uses two hooks because an
// initialization failure permits the application to continue while preventing
// the catalog update loop from starting. The earlier hook publishes
// catalogReady after Initialize succeeds; the later hook reads that flag before
// calling Start.
//
// The lifecycle manager runs start hooks serially in StartOrder, so the flag
// belongs to that ordered startup sequence. A nil catalog skips both loading
// and synchronization. The registered stop callback signals a supplied catalog
// and waits for its update work, including when the loading hook failed. That
// callback delegates to Service.Stop, which has no cancellation-context
// parameter.

// modelCatalogRuntimeReady marks registration of catalog loading and synchronization lifecycle hooks.
type modelCatalogRuntimeReady struct{}

// provideModelCatalogRuntime registers initial catalog loading and later
// synchronization with manager. Its returned marker indicates registration,
// while catalogReady records successful loading during lifecycle startup.
// Call during construction, before manager startup or shutdown.
//
// Initialization errors are logged and suppressed, allowing other services to
// start while the later hook skips synchronization. StartOrder puts the loading
// hook before the synchronization hook, whose Start call requires successful
// initialization. A nil catalog is skipped by both start callbacks.
//
// The stop callback invokes Service.Stop for a supplied catalog even when
// loading failed. That method waits for update work and does not accept the
// manager's stop context.
// @project-doc docs/architecture/system_architecture.md#startup_and_shutdown
func provideModelCatalogRuntime(catalog *provider.Service, manager *lifecycle.Manager) *modelCatalogRuntimeReady {
	catalogReady := false
	manager.Register(lifecycle.Hook{Name: "ModelCatalogInitialization", StartOrder: 188, Start: func(context.Context) error {
		if catalog == nil {
			return nil
		}
		// Keep startup available after a catalog load failure. The unset readiness
		// flag prevents the later hook from starting synchronization for this catalog.
		if err := catalog.Initialize(); err != nil {
			logging.LegacyPrintf("service.modelcatalog", "[Service] Warning: Model catalog initialization failed: %v", err)
			return nil
		}
		catalogReady = true
		return nil
	}})

	manager.Register(lifecycle.Hook{Name: "ModelCatalogService", StartOrder: 980, StopOrder: shutdownStageProducers, Start: func(ctx context.Context) error {
		// The later hook consumes readiness published by initial loading. Starting
		// updates after success satisfies Service.Start's initialization requirement.
		if catalog != nil && catalogReady {
			catalog.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		// An uninitialized service still owns its stop signal. Stop every supplied
		// catalog to complete that lifecycle and wait for any update work it owns.
		if catalog != nil {
			catalog.Stop()
		}
		return nil
	}})
	return &modelCatalogRuntimeReady{}
}
