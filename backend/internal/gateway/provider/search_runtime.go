package provider

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/search"
)

// searchSource reads the registry owned by the configuration runtime.
type searchSource struct{ registry *search.Registry }

type searchGroupPolicy struct{ groupPolicies *routing.PricingConfigService }

func (s searchSource) Current() searchtools.Searcher {
	if s.registry == nil {
		return nil
	}
	manager := s.registry.Get()
	if manager == nil {
		return nil
	}
	return manager
}

func (p searchGroupPolicy) Enabled(ctx context.Context, groupID int64, platform string) (bool, error) {
	policy, err := p.groupPolicies.GetGroupPolicy(ctx, groupID)
	if err != nil || policy == nil {
		return false, err
	}
	return policy.IsWebSearchEmulationEnabled(platform), nil
}

// NewSearchTools binds group policy, search settings, and the manager registry for synchronous search.
func NewSearchTools(runtime *search.ConfigService, groupPolicies *routing.PricingConfigService) *searchtools.Emulator {
	var registry *search.Registry
	var settings searchtools.Settings
	if runtime != nil {
		registry = runtime.Registry()
		settings = runtime
	}
	var policy searchtools.GroupPolicy
	if groupPolicies != nil {
		policy = searchGroupPolicy{groupPolicies}
	}
	return searchtools.NewEmulator(searchSource{registry}, settings, policy, time.Now, func() string { return uuid.New().String() }, observeSearch)
}

// SearchProviderMode reads the provider's search mode and logs legacy configuration.
func SearchProviderMode(value *searchtools.ProviderPolicy) string {
	selection := searchtools.ProviderMode(value)
	if selection.LegacyBool != nil {
		slog.Debug("legacy bool web_search_emulation value", "provider_id", value.ID, "value", *selection.LegacyBool)
	}
	return selection.Mode
}

func observeSearch(e searchtools.Event) {
	switch e.Kind {
	case "executing":
		slog.Info("web search emulation: executing search", "provider_id", e.ProviderID, "provider_name", e.ProviderName, "query", e.Query)
	case "completed":
		slog.Info("web search emulation: search completed", "provider", e.Provider, "results_count", e.Results)
	case "search_failed":
		slog.Error("web search emulation: search failed", "error", e.Err)
	case "write_failed":
		slog.Warn("web search emulation: SSE write failed, stopping", "error", e.Err)
	}
}
