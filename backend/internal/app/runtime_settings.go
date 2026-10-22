package app

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/server/runtimeconfig"
)

// runtime_settings.go registers forwarded-IP settings loading and the Grok
// default-model migration in one startup hook. Each failure produces its own
// warning and the hook returns success, allowing catalog initialization and the
// remaining application hooks to proceed.

// settingsRuntimeReady marks registration of forwarding settings load and default-model migration.
type settingsRuntimeReady struct{}

// provideSettingsRuntime registers settings initialization before catalog
// loading in startup order. It attempts forwarded-IP loading followed by the
// Grok default-model migration, logs each error, and returns a registration
// marker before those startup operations execute. Call before manager startup
// or shutdown, while it still accepts hook registration.
// @project-doc docs/architecture/system_architecture.md#startup_and_shutdown
func provideSettingsRuntime(
	manager *lifecycle.Manager,
	settingService *gateway.RuntimeSettings,
	forwarded *runtimeconfig.ForwardedSettings,
) *settingsRuntimeReady {
	manager.Register(lifecycle.Hook{Name: "RuntimeSettingsInitialization", StartOrder: 181, StopOrder: shutdownStageSettings, Start: func(ctx context.Context) error {
		// Report a forwarded-IP load failure and still attempt the Grok migration.
		// Returning success lets the remaining application startup hooks proceed.
		if err := forwarded.LoadForwardedClientIPSettings(ctx); err != nil {
			logging.LegacyPrintf("service.setting", "Warning: load forwarded client IP settings failed: %v", err)
		}
		// Treat a default-model migration error as a startup warning. Other
		// services can start even when this settings migration did not complete.
		if err := settingService.MigrateGrokDefaultTextModel(ctx); err != nil {
			logging.LegacyPrintf("service.setting", "Warning: migrate Grok default text model failed: %v", err)
		}
		return nil
	}})
	return &settingsRuntimeReady{}
}
