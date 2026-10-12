package app

import (
	"context"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/rediscache"
	usagepostgres "github.com/TokenFlux/TokenRouter/internal/usage/postgres"
)

// provideGrokAuthorization binds dynamic configuration and uses Redis for sessions when available.
func provideGrokAuthorization(proxies egress.ProxyRepository, client provider.GrokAuthorizationClient, cfg *config.Config, redisClient *redis.Client) *provider.GrokAuthorization {
	options := provideradapter.GrokAuthorizationOptions(proxies, func() bool {
		return cfg != nil && cfg.Gateway.Grok.PasswordAuthEnabled
	})
	authorization := provider.NewGrokAuthorization(client, options)
	if redisClient != nil {
		authorization.Store.Stop()
		authorization.Store = rediscache.NewGrokSessionStore(redisClient)
	}
	return authorization
}

// provideGrokQuota binds provider storage and upstream requests for quota probes.
func provideGrokQuota(store *providerpostgres.ProviderStore, proxies egress.ProxyRepository, token *provider.GrokTokenSource, transport httpclient.UpstreamTransport, cfg *config.Config, usageStore *usagepostgres.Store, settings *gateway.RuntimeSettings) *provider.GrokQuotaService {
	policy := egress.OperatorURLPolicy{Enabled: cfg.Security.URLAllowlist.Enabled, AllowInsecureHTTP: cfg.Security.URLAllowlist.AllowInsecureHTTP, AllowPrivateHosts: cfg.Security.URLAllowlist.AllowPrivateHosts, UpstreamHosts: slices.Clone(cfg.Security.URLAllowlist.UpstreamHosts)}
	requests := &provideradapter.GrokQuotaTransport{Do: transport.Do, Proxy: proxies.GetByID, DefaultBaseURL: gatewayprovider.GrokDefaultBaseURLReader(settings), OperatorValidator: policy.Validate, MapStatus: forward.MapStatus}

	return provideradapter.NewGrokQuota(store, token, requests, newProviderLocalUsageStats(usageStore))
}

// provideGrokTokens shares the token cache and refresh coordinator across gateway and manual queries.
func provideGrokTokens(store *providerpostgres.ProviderStore, cache provider.AccessTokenCache, authorization *provider.GrokAuthorization, refresh *provider.OAuthRefreshAPI) *provider.GrokTokenSource {
	executor := provider.NewGrokTokenRefresher(authorization)
	return &provider.GrokTokenSource{
		Cache:      cache,
		Repository: store,
		Policy:     provider.GrokProviderRefreshPolicy(),
		Refresh: func(ctx context.Context, record *provider.Record, window time.Duration) (*provider.OAuthRefreshResult, error) {
			return refresh.RefreshIfNeeded(ctx, record, executor, window)
		},
	}
}
