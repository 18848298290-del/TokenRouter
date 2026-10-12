package app

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	openaiws "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws"
)

// provideGrokExecutor 复用凭据、HTTP 池、健康状态和应用活动屏障。
func provideGrokExecutor(cfg *config.Config, credentials *gatewayadapter.RequestCredentials, transport httpclient.UpstreamTransport, output *gatewayhttp.OpenAIResponseOutput, health *provideradapter.GrokHealth, tls *egressprovider.TLSProfiles, readers *gatewayadapter.RuntimeReaders, blocks *provider.RuntimeBlockState, deferred *provider.DeferredService, store gatewayadapter.ExecutionProviderStore, activity *gatewayRequestActivity, prices *billing.PriceResolver, connections *openaiws.OpenAIWSConnections) *gatewayhttp.GrokExecutor {
	routes := provideGrokRoutes(cfg, readers)
	var attributes *routing.ModelAttributeService
	if readers != nil {
		attributes = readers.Attributes
	}
	return &gatewayhttp.GrokExecutor{Attributes: attributes, FastPolicy: &gatewayadapter.ExecutionFastPolicy{Readers: readers, Prices: prices}, Credentials: credentials, Transport: transport, Output: output, Health: health, Routes: routes, TLS: tls, Dialer: connections.Dialer(), Enter: activity.Enter, Failure: &gatewayhttp.UpstreamTransportFailure{Health: &provideradapter.TransportHealth{Runtime: blocks, Deferred: deferred, Store: store}}}
}

// provideGrokRoutes 绑定静态目标地址策略，默认模式在请求期间读取。
func provideGrokRoutes(cfg *config.Config, readers *gatewayadapter.RuntimeReaders) gatewayadapter.GrokRoutes {
	routes := gatewayadapter.GrokRoutes{Validate: grok.ValidateBaseURL}
	if cfg != nil {
		routes.Validate = (egress.OperatorURLPolicy{Enabled: cfg.Security.URLAllowlist.Enabled, AllowInsecureHTTP: cfg.Security.URLAllowlist.AllowInsecureHTTP, AllowPrivateHosts: cfg.Security.URLAllowlist.AllowPrivateHosts, UpstreamHosts: cfg.Security.URLAllowlist.UpstreamHosts}).Validate
	}
	if readers != nil {
		routes.DefaultMode = readers.Gateway.GetGrokDefaultBaseURLMode
	}
	return routes
}

// provideGrokHealth 与 OpenAI 快照写入共享节流器，与选号共享运行阻断及模型冷却。
func provideGrokHealth(store *providerpostgres.ProviderStore, health *provideradapter.UpstreamHealth, blocks *provider.RuntimeBlockState, models *provider.ModelTransientState) *provideradapter.GrokHealth {
	return &provideradapter.GrokHealth{
		Store: store, Health: health, Runtime: blocks, ModelTransient: models,
		Throttle: provider.NewWriteThrottle(30 * time.Second),
		NormalizeModel: func(value *provider.Record, model string) string {
			return (gatewayadapter.ModelPolicy{Record: value}).NormalizeOpenAI(model)
		},
	}
}
