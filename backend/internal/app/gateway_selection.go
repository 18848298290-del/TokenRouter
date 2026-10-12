package app

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/search"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	"github.com/TokenFlux/TokenRouter/internal/usage"
	usagepostgres "github.com/TokenFlux/TokenRouter/internal/usage/postgres"
)

// gatewayModelAvailability groups three diagnostic functions sharing provider storage and group mapping readers.
type gatewayModelAvailability struct {
	Messages   routing.ModelAvailabilityDiagnoser
	Compatible routing.ModelAvailabilityDiagnoser
	Resolved   routing.ModelAvailabilityDiagnoser
}

func provideGatewayModelAvailability(store *postgres.ProviderStore, modelConfigs *routing.PricingConfigService) *gatewayModelAvailability {
	var source gatewayadapter.AvailabilityProviders
	if store != nil {
		source = store
	}
	general := gatewayadapter.NewModelAvailability(source, modelConfigs, false)
	compatible := gatewayadapter.NewModelAvailability(source, modelConfigs, true)
	return &gatewayModelAvailability{
		Messages:   routing.ModelAvailabilityDiagnoserFunc(general.DiagnoseGeneral),
		Compatible: routing.ModelAvailabilityDiagnoserFunc(compatible.DiagnoseCompatible),
		Resolved:   routing.ModelAvailabilityDiagnoserFunc(compatible.DiagnoseCompatibleRouting),
	}
}

// provideGatewayRuntimeReaders binds shared readers; dynamic settings are read during requests.
func provideGatewayRuntimeReaders(store *settings.Store, gatewayRuntime *gateway.RuntimeSettings, providerRuntime *provider.RuntimeSettings, quota *provider.QuotaSettingsCache, routingRuntime *routing.RuntimeSettings, moderationRuntime *moderation.RuntimeSettings, searchRuntime *search.ConfigService, attributes *routing.ModelAttributeService) *gatewayadapter.RuntimeReaders {
	antigravity.SetUserAgentVersionResolver(gatewayRuntime.GetAntigravityUserAgentVersion)
	openai.SetCodexCanonicalUserAgentResolver(func() string { return gatewayRuntime.GetOpenAICodexUserAgent(context.Background()) })
	readers := &gatewayadapter.RuntimeReaders{Attributes: attributes, Gateway: gatewayRuntime, Provider: providerRuntime, Quota: quota, Routing: routingRuntime, Moderation: moderationRuntime, Search: searchRuntime, Scheduler: store}
	return readers
}

// provideModerationSettings binds the moderation settings cache used by the gateway.
func provideModerationSettings(store *settings.Store) *moderation.RuntimeSettings {
	return moderation.NewRuntimeSettings(store, settings.ErrSettingNotFound)
}

// provideSelectionReads binds queries; selection determines the order of snapshot and database reads.
func provideSelectionReads(providers gatewayadapter.ExecutionProviderStore, groups routing.GroupRepository, snapshots selection.Snapshots) selection.Reads {
	return selection.Reads{Providers: providers, Groups: groups, Snapshot: snapshots}
}

// provideSelectionShared shares feedback, settings, counters, health, and group policy readers.
func provideSelectionShared(cache session.GatewayCache, concurrency *scheduler.ConcurrencyService, health *provideradapter.UpstreamHealth, modelConfigs *routing.PricingConfigService, shared *schedulerSharedState) selection.Shared {
	return selection.Shared{
		Cache:         cache,
		Concurrency:   concurrency,
		Health:        health,
		GroupPolicies: modelConfigs,
		Parameters:    shared.Parameters,
		Feedback:      shared.Feedback,
	}
}

// selectionOptions reads startup configuration and passes zero values through to selection.
func selectionOptions(cfg *config.Config) selection.Options {
	options := selection.DefaultOptions()
	value := strings.ToLower(strings.TrimSpace(productEnv("DEBUG_MODEL_ROUTING")))
	options.DebugRouting = value == "1" || value == "true" || value == "yes" || value == "on"
	if cfg == nil {
		return options
	}
	s := cfg.Gateway.Scheduling
	options.Scheduling = scheduler.FlowOptions{LoadBatchEnabled: s.LoadBatchEnabled, PreferSoonestReset: s.PreferSoonestReset, FallbackMaxWaiting: s.FallbackMaxWaiting, StickySessionMaxWaiting: s.StickySessionMaxWaiting, FallbackSelectionMode: s.FallbackSelectionMode, FallbackWaitTimeout: s.FallbackWaitTimeout, StickySessionWaitTimeout: s.StickySessionWaitTimeout}
	ws := cfg.Gateway.OpenAIWS
	options.ReadLegacySticky = ws.SessionHashReadOldFallback
	options.WriteLegacySticky = ws.SessionHashDualWriteOld
	if ws.StickySessionTTLSeconds > 0 {
		options.StickyTTL = time.Duration(ws.StickySessionTTLSeconds) * time.Second
	}
	if ws.StickyResponseIDTTLSeconds > 0 {
		options.ResponseTTL = time.Duration(ws.StickyResponseIDTTLSeconds) * time.Second
	}
	return options
}

// provideSelectionModelTransient shares model transient state between execution feedback and selection.
func provideSelectionModelTransient() *provider.ModelTransientState {
	return provider.NewModelTransientState(0)
}

// provideSelectionProxyCircuit applies positive overrides to defaults and shares proxy isolation state with
// feedback and selection.
func provideSelectionProxyCircuit(cfg *config.Config) *egress.ProxyStreamCircuit {
	options := egress.DefaultProxyStreamCircuitSettings()
	if cfg != nil {
		value := cfg.Gateway.OpenAIProxyStreamCircuit
		options.Disabled = value.Disabled
		if value.FailureThreshold > 0 {
			options.FailureThreshold = value.FailureThreshold
		}
		if value.WindowSeconds > 0 {
			options.FailureWindow = time.Duration(value.WindowSeconds) * time.Second
		}
		if value.TTLSeconds > 0 {
			options.QuarantineTTL = time.Duration(value.TTLSeconds) * time.Second
		}
	}
	return egress.NewProxyStreamCircuit(options)
}

func provideGenericSelection(reads selection.Reads, shared selection.Shared, cfg *config.Config, windows billing.WindowCostCache, source usage.UsageLogRepository, nativeUsage *usagepostgres.Store, rpm scheduler.RPMCache, sessions scheduler.SessionLimitCache, gates *selectionFreeQuotaGates, providers gatewayadapter.ExecutionProviderStore) *selection.Generic {
	stats := usageWindowStats{nativeUsage}
	guard := billing.NewWindowCostGuard(windows, stats, billing.WindowCostGuardOptions{Now: time.Now, Stats: billing.SharedWindowCostMetrics(), Log: func(format string, args ...any) { logging.LegacyPrintf("service.gateway", format, args...) }, Debug: slog.Debug})
	var write func(context.Context, int64, string) error
	if providers != nil {
		write = providers.SetError
	}
	return selection.NewGeneric(selection.GenericDependencies{
		Reads:                   reads,
		Shared:                  shared,
		Window:                  guard,
		WindowPrefetchAvailable: windows != nil && source != nil,
		RPM:                     rpm,
		Sessions:                sessions,
		FreeQuota:               gates.Generic,
		SetProviderError:        write,
	}, selectionOptions(cfg))
}

func provideCompatibleSelection(generic *selection.Generic, gemini *selection.Gemini, reads selection.Reads, shared selection.Shared, cfg *config.Config, responses session.OpenAIWSStateStore, quota *provider.QuotaSettingsCache, blocks *provider.RuntimeBlockState, transient *provider.ModelTransientState, proxy *egress.ProxyStreamCircuit, state *schedulerSharedState, gates *selectionFreeQuotaGates) *selection.Compatible {
	return selection.NewCompatible(selection.CompatibleDependencies{
		Generic: generic, Gemini: gemini,
		Reads:                reads,
		Shared:               shared,
		Responses:            responses,
		QuotaSettings:        quota,
		RuntimeBlocks:        blocks,
		ModelTransient:       transient,
		ProxyCircuit:         proxy,
		StickyStats:          state.Sticky,
		FreeQuota:            gates.Compatible,
		NewAdvancedFreeQuota: gates.Advanced,
	}, selectionOptions(cfg))
}

func provideGeminiSelection(reads selection.Reads, shared selection.Shared, cfg *config.Config, quota *provider.GeminiPrecheck) *selection.Gemini {
	return selection.NewGemini(selection.GeminiDependencies{Reads: reads, Shared: shared, QuotaPrecheck: quota}, selectionOptions(cfg))
}

// productEnv prefers TOKENROUTER variables and tries legacy names when empty; "0" counts as configured.
func productEnv(suffix string) string {
	value := os.Getenv("TOKENROUTER_" + suffix)
	if value != "" {
		return value
	}
	return os.Getenv("SUB2API_" + suffix)
}

// gatewayCompatibilitySnapshot returns the shared sticky-session count and zero metadata entries.
func gatewayCompatibilitySnapshot(shared *schedulerSharedState) gatewayhttp.CompatibilityLogSnapshot {
	if shared == nil || shared.Sticky == nil {
		return gatewayhttp.CompatibilityLogSnapshot{}
	}
	total, hit, dual := shared.Sticky.Snapshot()
	rate := float64(0)
	if total > 0 {
		rate = float64(hit) / float64(total)
	}
	return gatewayhttp.CompatibilityLogSnapshot{ReadTotal: total, ReadHit: hit, DualWrite: dual, ReadHitRate: rate}
}

// stopOpenAI429 passes credential eligibility to the retry budget policy.
func stopOpenAI429(value *gatewayadapter.ExecutionProvider, status, switches int, state *failover.OAuth429State) bool {
	return failover.StopOAuth429(failover.OAuth429Provider{OpenAI: value != nil && value.View().IsOpenAIOAuthLike(), Grok: value != nil && value.Record.Platform == capability.PlatformGrok && value.Record.Type == capability.ProviderTypeOAuth}, status, switches, state)
}
