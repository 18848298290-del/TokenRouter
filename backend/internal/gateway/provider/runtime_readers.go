package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/search"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Execution adapters read live settings through app-owned services at the
// point where each policy is needed. RuntimeReaders groups those service
// references so request handling can use the same settings, quota, search, and
// attribute instances as other application consumers.

// RuntimeReaders groups app-owned readers for dynamic execution policy and
// configuration.
type RuntimeReaders struct {
	// Attributes, Gateway, Provider, Quota, Routing, Moderation, Search, and
	// Scheduler reference app-owned readers. Adapters read dynamic values when
	// applying the corresponding policy; this struct creates no settings snapshot.
	//
	// model attributes policy reader
	Attributes *routing.ModelAttributeService
	// gateway dynamic policy reader
	Gateway *gateway.RuntimeSettings
	// provider dynamic policy reader
	Provider *provider.RuntimeSettings
	// cached provider quota settings
	Quota *provider.QuotaSettingsCache
	// routing dynamic policy reader
	Routing *routing.RuntimeSettings
	// moderation dynamic policy reader
	Moderation *moderation.RuntimeSettings
	// published search configuration reader
	Search *search.ConfigService
	// scheduler stored settings reader
	Scheduler settings.Repository
}
