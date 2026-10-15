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

// settings_readers.go constructs domain settings readers over the shared
// application settings store. Assembly passes the missing-setting sentinel to
// readers that distinguish absent values, while panel caching, backend-mode
// warnings, and per-request creative reads remain with the corresponding reader
// implementation.

// providePanelSettings binds panel request-limit policy to shared settings
// storage.
// @project-doc docs/interfaces/configuration.md#runtime_settings
func providePanelSettings(store *settings.Store) *runtimeconfig.PanelSettings {
	return runtimeconfig.NewPanelSettings(store)
}

// providePromotionSettings binds promotion feature flags to shared settings
// storage.
func providePromotionSettings(store *settings.Store) *promotion.RuntimeSettings {
	return promotion.NewRuntimeSettings(store)
}

// provideProviderSettings binds provider maintenance policy with the
// missing-setting sentinel.
func provideProviderSettings(store *settings.Store) *provider.RuntimeSettings {
	return provider.NewRuntimeSettings(store, settings.ErrSettingNotFound)
}

// provideUsageSettings binds public usage ranking policy to shared settings
// storage.
func provideUsageSettings(store *settings.Store) *usage.RuntimeSettings {
	return usage.NewRuntimeSettings(store)
}

// provideAuditSettings binds audit retention-day reads to shared settings
// storage.
func provideAuditSettings(store *settings.Store) *audit.RetentionSettings {
	return audit.NewRetentionSettings(store)
}

// provideIdentitySettings binds identity runtime policy with the
// missing-setting sentinel.
func provideIdentitySettings(store *settings.Store) *identity.RuntimeSettings {
	return identity.NewRuntimeSettings(store, settings.ErrSettingNotFound)
}

// provideRoutingSettings binds routing runtime policy to shared settings
// storage.
func provideRoutingSettings(store *settings.Store) *routing.RuntimeSettings {
	return routing.NewRuntimeSettings(store)
}

// provideBackendMode binds cached backend admission policy and warning
// diagnostics.
func provideBackendMode(store *settings.Store) *admission.BackendMode {
	return admission.NewBackendMode(store, slog.Warn)
}

// provideCreativeRuntimeSettings binds creative settings read on each request.
// Missing-setting identity is supplied to the reader, which owns default
// interpretation and the operation context used for those reads.
func provideCreativeRuntimeSettings(store *settings.Store) *creative.RuntimeSettings {
	return creative.NewRuntimeSettings(store, settings.ErrSettingNotFound)
}
