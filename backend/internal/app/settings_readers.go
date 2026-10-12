package app

import (
	"log/slog"

	"github.com/TokenFlux/TokenRouter/internal/audit"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/server/runtimeconfig"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// providePanelSettings binds the shared Store to the panel settings reader, which owns its cache and read timing.
// @project-doc docs/interfaces/configuration.md#runtime_settings
func providePanelSettings(store *settings.Store) *runtimeconfig.PanelSettings {
	return runtimeconfig.NewPanelSettings(store)
}

func providePromotionSettings(store *settings.Store) *promotion.RuntimeSettings {
	return promotion.NewRuntimeSettings(store)
}

func provideProviderSettings(store *settings.Store) *provider.RuntimeSettings {
	return provider.NewRuntimeSettings(store, settings.ErrSettingNotFound)
}

func provideUsageSettings(store *settings.Store) *usage.RuntimeSettings {
	return usage.NewRuntimeSettings(store)
}

func provideAuditSettings(store *settings.Store) *audit.RetentionSettings {
	return audit.NewRetentionSettings(store)
}

func provideIdentitySettings(store *settings.Store) *identity.RuntimeSettings {
	return identity.NewRuntimeSettings(store, settings.ErrSettingNotFound)
}

func provideRoutingSettings(store *settings.Store) *routing.RuntimeSettings {
	return routing.NewRuntimeSettings(store)
}

func provideBackendMode(store *settings.Store) *admission.BackendMode {
	return admission.NewBackendMode(store, slog.Warn)
}

// provideCreativeRuntimeSettings binds the shared Store for settings read on each creative request.
func provideCreativeRuntimeSettings(store *settings.Store) *creative.RuntimeSettings {
	return creative.NewRuntimeSettings(store, settings.ErrSettingNotFound)
}
