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

// search_runtime.go binds search-tool emulation to shared search
// configuration, group policy, and event logging.
// Search-tool emulation reads the current search manager from the shared
// configuration registry and group policy from routing. Missing runtime or
// group-policy dependencies remain optional, so the emulator can apply its own
// disable/default decisions without constructing another manager or settings
// cache.

// searchSource adapts the configuration-owned registry to synchronous search
// manager reads.
type searchSource struct {
	registry *search.Registry /* shared search manager registry */
}

// searchGroupPolicy adapts routing group policy to search-emulation enablement
// checks.
type searchGroupPolicy struct {
	// group search policy reader
	groupPolicies *routing.PricingConfigService
}

// Current returns the registry's currently published manager as the search
// implementation. A nil registry or an empty publication returns nil, allowing
// the emulator to treat search execution as unavailable.
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

// Enabled reads group routing policy and asks whether search emulation is
// enabled for platform. Missing policy returns false, and lookup errors
// propagate; the bound group-policy service must be configured.
func (p searchGroupPolicy) Enabled(ctx context.Context, groupID int64, platform string) (bool, error) {
	policy, err := p.groupPolicies.GetGroupPolicy(ctx, groupID)
	if err != nil || policy == nil {
		return false, err
	}
	return policy.IsWebSearchEmulationEnabled(platform), nil
}

// NewSearchTools binds search runtime, registry, and optional group policy to
// a synchronous emulator. Nil dependencies remain unset, while time.Now, UUID
// generation, and observeSearch provide its clock, request IDs, and event
// reporting.
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

// SearchProviderMode resolves a provider's search-emulation mode and logs
// recognized legacy boolean input. Mode resolution owns nil/default behavior;
// the log includes provider identity when legacy configuration was observed.
func SearchProviderMode(value *searchtools.ProviderPolicy) string {
	selection := searchtools.ProviderMode(value)
	if selection.LegacyBool != nil {
		slog.Debug("legacy bool web_search_emulation value", "provider_id", value.ID, "value", *selection.LegacyBool)
	}
	return selection.Mode
}

// observeSearch logs execution, completion, search failures, and output-write
// failures for emulation. Other event kinds are ignored, leaving the
// emulator's event producers to choose which transitions have gateway
// diagnostics.
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
