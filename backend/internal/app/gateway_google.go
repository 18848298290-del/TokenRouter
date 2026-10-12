package app

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// googleForwardOptions 从启动配置读取响应、日志和目标地址限制。
func googleForwardOptions(cfg *config.Config) googleforward.Options {
	limit := cfg.Gateway.UpstreamResponseReadMaxBytes
	if limit <= 0 {
		limit = config.DefaultUpstreamResponseReadMaxBytes
	}
	return googleforward.Options{
		Configured:           true,
		LogErrorBody:         cfg.Gateway.LogUpstreamErrorBody,
		LogErrorBodyMaxBytes: cfg.Gateway.LogUpstreamErrorBodyMaxBytes,
		DebugHeaders:         cfg.Gateway.GeminiDebugResponseHeaders,
		ResponseReadLimit:    limit,
		MaxLineSize:          cfg.Gateway.MaxLineSize,
		StreamInterval:       cfg.Gateway.StreamDataIntervalTimeout,
		StreamKeepalive:      cfg.Gateway.StreamKeepaliveInterval,
		URLAllowlistEnabled:  cfg.Security.URLAllowlist.Enabled,
		AllowInsecureHTTP:    cfg.Security.URLAllowlist.AllowInsecureHTTP,
		AllowedHosts:         append([]string(nil), cfg.Security.URLAllowlist.UpstreamHosts...),
		AllowPrivateHosts:    cfg.Security.URLAllowlist.AllowPrivateHosts,
	}
}

// provideGeminiForward 为 Gemini 转发绑定共享的凭据、配额检查和健康记录组件。
func provideGeminiForward(store gatewayadapter.ExecutionProviderStore, tokens *provider.GeminiTokenSource, health *provideradapter.UpstreamHealth, precheck *provider.GeminiPrecheck, transport httpclient.UpstreamTransport, filter *egress.CompiledHeaderFilter, cfg *config.Config, activity *gatewayRequestActivity, attributes *routing.ModelAttributeService) *googleforward.Gemini {
	daily := func() *int64 {
		value := provider.GeminiDailyResetTime(time.Now(), geminiQuotaLocation()).Unix()
		return &value
	}
	return &googleforward.Gemini{
		Attributes:   attributes,
		Options:      googleForwardOptions(cfg),
		Tokens:       tokens,
		Health:       health,
		Transport:    transport,
		HeaderFilter: filter,
		Enter:        activity.Enter,
		Errors: &provideradapter.GeminiErrorObserver{
			Other:          health,
			Precheck:       precheck,
			SetRateLimited: store.SetRateLimited,
			DailyReset:     daily,
			ResetTime:      func(body []byte) *int64 { return gemini.ParseGeminiRateLimitResetTime(body, daily) },
		},
	}
}

func provideGeminiExecutor(runtime *googleforward.Gemini) *gatewayhttp.GeminiExecutor {
	return &gatewayhttp.GeminiExecutor{Runtime: runtime}
}

// provideAntigravityForward 绑定动态设置读取函数，并与提供商探测共享重试和健康状态组件。
func provideAntigravityForward(store gatewayadapter.ExecutionProviderStore, tokens *provider.AntigravityTokenSource, retry *provideradapter.AntigravityRetry, observer *provideradapter.AntigravityErrorObserver, transport httpclient.UpstreamTransport, readers *gatewayadapter.RuntimeReaders, sticky session.GatewayCache, cfg *config.Config, activity *gatewayRequestActivity) *googleforward.Antigravity {
	return &googleforward.Antigravity{
		Attributes: readers.Attributes,
		Options:    googleForwardOptions(cfg),
		Tokens:     tokens,
		Retry:      retry,
		Errors:     observer,
		Store:      store,
		Transport:  transport,
		Gateway:    readers.Gateway,
		Routing:    readers.Routing,
		Sticky:     sticky,
		Enter:      activity.Enter,
	}
}

func provideAntigravityExecutor(runtime *googleforward.Antigravity) *gatewayhttp.AntigravityExecutor {
	return &gatewayhttp.AntigravityExecutor{Runtime: runtime}
}
