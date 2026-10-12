package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/messageforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/requestdebug"
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

func provideGatewayErrorRules(repo errorpolicy.ErrorPassthroughRepository, cache errorpolicy.ErrorPassthroughCache) *errorpolicy.ErrorPassthroughService {
	return errorpolicy.NewErrorPassthroughService(repo, cache, telemetry.ErrorRules)
}

// provideGatewayRequestDebug shares a trace handle and closes it after requests and completion work finish.
func provideGatewayRequestDebug(manager *lifecycle.Manager) *requestdebug.Trace {
	trace := requestdebug.New(productEnv("DEBUG_GATEWAY_BODY"), productEnv("DEBUG_CLAUDE_MIMIC"))
	manager.Register(lifecycle.Hook{Name: "GatewayRequestDebug", StopOrder: 90, Stop: func(context.Context) error { return trace.Close() }})
	return trace
}

// provideMessagesExecution binds Messages forwarding, compatible conversions, and token counting.
// @project-doc docs/architecture/system_architecture.md#dependency_layers
func provideMessagesExecution(credentials *provider.MessageCredentialSource, fingerprint *anthropic.RequestFingerprint, transport httpclient.UpstreamTransport, health *provideradapter.UpstreamHealth, tls *egressprovider.TLSProfiles, readers *gatewayadapter.RuntimeReaders, prices *billing.PriceResolver, search *searchtools.Emulator, activity *gatewayRequestActivity, debug *requestdebug.Trace, providers gatewayadapter.ExecutionProviderStore, deferred *provider.DeferredService, cfg *config.Config, filter *egress.CompiledHeaderFilter, modelConfigs *routing.PricingConfigService) *gatewayhttp.MessagesExecutor {
	deps := messageforward.Dependencies{Credentials: credentials, Fingerprint: fingerprint, Transport: transport, Health: health, TLS: tls, Prices: prices, Search: search, ProviderState: providers, Deferred: deferred}
	if modelConfigs != nil {
		deps.GroupPolicies = modelConfigs
	}
	if debug != nil {
		deps.Debug = debug
	}
	if readers != nil {
		deps.Settings = readers.Gateway
		deps.Attributes = readers.Attributes
	}
	if activity != nil {
		deps.Enter = activity.Enter
	}
	return gatewayhttp.NewMessagesExecutor(messageforward.NewRuntime(deps, messageExecutionOptions(cfg)), filter)
}

// messageExecutionOptions captures startup parameters; execution reads runtime settings per request.
func messageExecutionOptions(cfg *config.Config) messageforward.Options {
	options := messageforward.Options{ResponseReadLimit: config.DefaultUpstreamResponseReadMaxBytes}
	if cfg == nil {
		return options
	}
	options.Configured = true
	options.InjectAPIKeyBeta = cfg.Gateway.InjectBetaForAPIKey
	options.LogErrorBody = cfg.Gateway.LogUpstreamErrorBody
	options.LogErrorBodyMaxBytes = cfg.Gateway.LogUpstreamErrorBodyMaxBytes
	options.FailoverOn400 = cfg.Gateway.FailoverOn400
	options.GeminiDebugHeaders = cfg.Gateway.GeminiDebugResponseHeaders
	options.MaxLineSize = cfg.Gateway.MaxLineSize
	options.StreamInterval = time.Duration(cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	options.StreamKeepalive = time.Duration(cfg.Gateway.StreamKeepaliveInterval) * time.Second
	if cfg.Gateway.UpstreamResponseReadMaxBytes > 0 {
		options.ResponseReadLimit = cfg.Gateway.UpstreamResponseReadMaxBytes
	}
	options.PreserveContentType = !cfg.Security.ResponseHeaders.Enabled
	options.URLAllowlistEnabled = cfg.Security.URLAllowlist.Enabled
	options.AllowInsecureHTTP = cfg.Security.URLAllowlist.AllowInsecureHTTP
	options.URLValidation = egress.ValidationOptions{AllowedHosts: cfg.Security.URLAllowlist.UpstreamHosts, RequireAllowlist: true, AllowPrivate: cfg.Security.URLAllowlist.AllowPrivateHosts}
	return options
}

// provideGatewayPromptPolicy binds the settings store and diagnostics to the cached prompt-policy service.
func provideGatewayPromptPolicy(store *settings.Store) *promptpolicy.Service {
	return promptpolicy.New(store, settings.ErrSettingNotFound, slog.Warn)
}
