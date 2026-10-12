package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/search"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// RuntimeReaders supplies configuration and policy readers for execution adapters.
// Readers share app instances; dynamic values are read when the request needs them.
type RuntimeReaders struct {
	Attributes *routing.ModelAttributeService
	Gateway    *gateway.RuntimeSettings
	Provider   *provider.RuntimeSettings
	Quota      *provider.QuotaSettingsCache
	Routing    *routing.RuntimeSettings
	Moderation *moderation.RuntimeSettings
	Search     *search.ConfigService
	Scheduler  settings.Repository
}
