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

// gatewayModelAvailability 包含三种诊断函数，共用提供商存储和分组映射读取实例。
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

// provideGatewayRuntimeReaders 绑定共享读取器，动态设置在请求期间读取。
func provideGatewayRuntimeReaders(store *settings.Store, gatewayRuntime *gateway.RuntimeSettings, providerRuntime *provider.RuntimeSettings, quota *provider.QuotaSettingsCache, routingRuntime *routing.RuntimeSettings, moderationRuntime *moderation.RuntimeSettings, searchRuntime *search.ConfigService, attributes *routing.ModelAttributeService) *gatewayadapter.RuntimeReaders {
	antigravity.SetUserAgentVersionResolver(gatewayRuntime.GetAntigravityUserAgentVersion)
	openai.SetCodexCanonicalUserAgentResolver(func() string { return gatewayRuntime.GetOpenAICodexUserAgent(context.Background()) })
	readers := &gatewayadapter.RuntimeReaders{Attributes: attributes, Gateway: gatewayRuntime, Provider: providerRuntime, Quota: quota, Routing: routingRuntime, Moderation: moderationRuntime, Search: searchRuntime, Scheduler: store}
	return readers
}

// provideModerationSettings 为网关审核绑定独立的设置缓存。
func provideModerationSettings(store *settings.Store) *moderation.RuntimeSettings {
	return moderation.NewRuntimeSettings(store, settings.ErrSettingNotFound)
}

// provideSelectionReads 组合选择器的数据查询函数，选择用例决定快照与数据库的查询顺序。
func provideSelectionReads(providers gatewayadapter.ExecutionProviderStore, groups routing.GroupRepository, snapshots selection.Snapshots) selection.Reads {
	return selection.Reads{Providers: providers, Groups: groups, Snapshot: snapshots}
}

// provideSelectionShared 发布共享的反馈、参数、计数、健康和分组策略读取实例。
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

// selectionOptions 从启动配置读取选择器参数，配置中的零值直接传给选择器。
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

// provideSelectionModelTransient 由执行反馈与选号共同持有一个模型瞬态状态。
func provideSelectionModelTransient() *provider.ModelTransientState {
	return provider.NewModelTransientState(0)
}

// provideSelectionProxyCircuit 用正数配置覆盖默认值，执行反馈与选择器共用代理隔离状态。
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

// productEnv 优先读取 TOKENROUTER 变量，值为空时尝试旧名称，零值按已配置处理。
func productEnv(suffix string) string {
	value := os.Getenv("TOKENROUTER_" + suffix)
	if value != "" {
		return value
	}
	return os.Getenv("SUB2API_" + suffix)
}

// gatewayCompatibilitySnapshot 返回共享粘性会话计数，metadata 计数为零。
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

// stopOpenAI429 将凭据资格传给重试预算规则。
func stopOpenAI429(value *gatewayadapter.ExecutionProvider, status, switches int, state *failover.OAuth429State) bool {
	return failover.StopOAuth429(failover.OAuth429Provider{OpenAI: value != nil && value.View().IsOpenAIOAuthLike(), Grok: value != nil && value.Record.Platform == capability.PlatformGrok && value.Record.Type == capability.ProviderTypeOAuth}, status, switches, state)
}
