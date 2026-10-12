package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	wshttp "github.com/TokenFlux/TokenRouter/internal/gateway/ws/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	openaiws "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws"
)

// provideCodexTurnStateHeaders binds the shared source registry and sticky-session TTL to HTTP responses.
func provideCodexTurnStateHeaders(choices *selection.Compatible) *gatewayhttp.CodexTurnStateHeaders {
	return &gatewayhttp.CodexTurnStateHeaders{Origins: session.NewCodexTurnOrigins(time.Now), TTL: choices.SessionStickyTTL}
}

// provideWSConnections shares the platform dialer and starts the connection pool on demand.
func provideWSConnections(cfg *config.Config, manager *lifecycle.Manager, runtime *ws.Runtime) *openaiws.OpenAIWSConnections {
	connections := openaiws.NewOpenAIWSConnections(openAIWSPoolOptions(cfg), openai.NewDefaultWSClientDialer())
	runtime.SetApply(func(value ws.Parameters) error { return connections.UpdateOptions(responsesWSPoolOptions(value)) })
	manager.Register(lifecycle.Hook{Name: "OpenAIWSConnections", StartOrder: 990, StopOrder: 10, Stop: func(context.Context) error { connections.Close(); return nil }})
	return connections
}

func openAIWSPoolOptions(cfg *config.Config) *openaiws.WSPoolOptions {
	if cfg == nil {
		return nil
	}
	returnValue := openAIWSExecutionOptions(cfg)
	returnPtr := responsesWSPoolOptions(*returnValue)
	return &returnPtr
}

// responsesWSPoolOptions extracts connection pool configuration.
func responsesWSPoolOptions(options ws.Parameters) openaiws.WSPoolOptions {
	return openaiws.WSPoolOptions{
		MaxConnsPerProvider:                         options.MaxConnsPerProvider,
		DynamicMaxConnsByProviderConcurrencyEnabled: options.DynamicMaxConnsByProviderConcurrencyEnabled,
		OAuthMaxConnsFactor:                         options.OAuthMaxConnsFactor,
		APIKeyMaxConnsFactor:                        options.APIKeyMaxConnsFactor,
		MinIdlePerProvider:                          options.MinIdlePerProvider,
		MaxIdlePerProvider:                          options.MaxIdlePerProvider,
		QueueLimitPerConn:                           options.QueueLimitPerConn,
		PoolTargetUtilization:                       options.PoolTargetUtilization,
		PrewarmCooldownMS:                           options.PrewarmCooldownMS,
		DialTimeoutSeconds:                          options.DialTimeoutSeconds,
	}
}

// openAIWSExecutionOptions handles nil configuration and passes configured zero values through.
func openAIWSExecutionOptions(cfg *config.Config) *wshttp.OpenAIWSOptions {
	if cfg == nil {
		return nil
	}
	v := cfg.Gateway.OpenAIWS
	return &wshttp.OpenAIWSOptions{
		MaxConnsPerProvider:                         v.MaxConnsPerProvider,
		MinIdlePerProvider:                          v.MinIdlePerProvider,
		MaxIdlePerProvider:                          v.MaxIdlePerProvider,
		DynamicMaxConnsByProviderConcurrencyEnabled: v.DynamicMaxConnsByProviderConcurrencyEnabled,
		OAuthMaxConnsFactor:                         v.OAuthMaxConnsFactor,
		APIKeyMaxConnsFactor:                        v.APIKeyMaxConnsFactor,
		QueueLimitPerConn:                           v.QueueLimitPerConn,
		PoolTargetUtilization:                       v.PoolTargetUtilization,
		PrewarmCooldownMS:                           v.PrewarmCooldownMS,
		ClientFirstMessageTimeoutSeconds:            v.ClientFirstMessageTimeoutSeconds,
		IngressInterTurnIdleTimeoutSeconds:          v.IngressInterTurnIdleTimeoutSeconds,
		MaxIngressConnectionsPerAPIKey:              v.MaxIngressConnectionsPerAPIKey,
		ClientReadLimitBytes:                        v.ClientReadLimitBytes,
		HTTPBridgeThresholdBytes:                    v.HTTPBridgeThresholdBytes,
		DialTimeoutSeconds:                          v.DialTimeoutSeconds,
		ReadTimeoutSeconds:                          v.ReadTimeoutSeconds,
		WriteTimeoutSeconds:                         v.WriteTimeoutSeconds,
		IngressPreviousResponseRecoveryEnabled:      v.IngressPreviousResponseRecoveryEnabled,
		StickySessionTTLSeconds:                     v.StickySessionTTLSeconds,
		StickyResponseIDTTLSeconds:                  v.StickyResponseIDTTLSeconds,
	}
}

// provideResponsesWSRuntime assembles shared option snapshots and stops refreshing before connections close.
func provideResponsesWSRuntime(cfg *config.Config, store *settings.Store, manager *lifecycle.Manager) *ws.Runtime {
	defaults := ws.DefaultParameters()
	if cfg != nil {
		v := cfg.Gateway.OpenAIWS
		defaults.MaxConnsPerProvider = v.MaxConnsPerProvider
		defaults.MinIdlePerProvider = v.MinIdlePerProvider
		defaults.MaxIdlePerProvider = v.MaxIdlePerProvider
		defaults.DynamicMaxConnsByProviderConcurrencyEnabled = v.DynamicMaxConnsByProviderConcurrencyEnabled
		defaults.OAuthMaxConnsFactor = v.OAuthMaxConnsFactor
		defaults.APIKeyMaxConnsFactor = v.APIKeyMaxConnsFactor
		defaults.QueueLimitPerConn = v.QueueLimitPerConn
		defaults.PoolTargetUtilization = v.PoolTargetUtilization
		defaults.PrewarmCooldownMS = v.PrewarmCooldownMS
		defaults.ClientFirstMessageTimeoutSeconds = v.ClientFirstMessageTimeoutSeconds
		defaults.IngressInterTurnIdleTimeoutSeconds = v.IngressInterTurnIdleTimeoutSeconds
		defaults.MaxIngressConnectionsPerAPIKey = v.MaxIngressConnectionsPerAPIKey
		defaults.ClientReadLimitBytes = v.ClientReadLimitBytes
		defaults.HTTPBridgeThresholdBytes = v.HTTPBridgeThresholdBytes
		defaults.DialTimeoutSeconds = v.DialTimeoutSeconds
		defaults.ReadTimeoutSeconds = v.ReadTimeoutSeconds
		defaults.WriteTimeoutSeconds = v.WriteTimeoutSeconds
		defaults.IngressPreviousResponseRecoveryEnabled = v.IngressPreviousResponseRecoveryEnabled
		defaults.StickySessionTTLSeconds = v.StickySessionTTLSeconds
		defaults.StickyResponseIDTTLSeconds = v.StickyResponseIDTTLSeconds
	}
	runtime := ws.NewRuntime(defaults, store, func(err error) { slog.Warn("Responses WS 设置刷新失败", "error", err) })
	manager.Register(lifecycle.Hook{Name: "ResponsesWSSettings", StartOrder: 980, StopOrder: 9, Start: runtime.Start, Stop: runtime.Stop})
	return runtime
}
