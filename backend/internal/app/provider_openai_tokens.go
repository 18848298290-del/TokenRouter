package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	openaiws "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws"
)

// OpenAI authorization, quota management, and execution credentials share token
// refresh and cached provider reads. Agent identity changes invalidate shared
// WS connections, quota actions have an early stop hook, and optional parent or
// token sources are exposed according to the dependencies supplied during
// assembly.

// provideOAuthTokenCache binds cached provider access tokens to the shared
// Redis client.
func provideOAuthTokenCache(rdb *redis.Client) provider.AccessTokenCache {
	return rediscache.NewOAuthTokenCache(rdb)
}

// provideOpenAIAuthorization constructs shared OAuth sessions with proxy, TLS,
// and privacy clients. Runtime settings attach a request-time Codex User-Agent
// reader when present; the authorization service receives the newly constructed
// session store.
func provideOpenAIAuthorization(proxies egress.ProxyRepository, client provideradapter.OpenAIOAuthClient, privacy openai.PrivacyClientFactory, settings *gateway.RuntimeSettings, routers provideradapter.OpenAITokenRouterReader, profiles provideradapter.OpenAITokenProfileResolver) *provider.OpenAIAuthorization {
	deps := &provideradapter.OpenAIAuthorizationDependencies{
		Proxies:        proxies,
		Client:         client,
		PrivacyFactory: privacy,
		Routers:        routers,
		Profiles:       profiles,
	}
	if settings != nil {
		deps.CodexUserAgent = settings.GetOpenAICodexUserAgent
	}
	return provider.NewOpenAIAuthorization(provider.NewOpenAISessionStore(), provideradapter.OpenAIAuthorizationOptions(deps))
}

// provideOpenAIProviderOAuth binds OAuth and quota HTTP with selected proxy
// reads. Call before lifecycle startup; quota actions stop at level fourteen,
// and the proxy callback retains lookup errors with its unavailable result.
func provideOpenAIProviderOAuth(auth *provider.OpenAIAuthorization, admin *provider.Admin, quota *provider.OpenAIQuotaService, recovery *provider.RecoveryService, proxies *egress.ProxyAdmin, manager *lifecycle.Manager) *providerhttp.OpenAIOAuthHandler {
	handler := providerhttp.NewOpenAIOAuthHandler(auth, admin, quota, recovery, providerhttp.OpenAIHTTPOptions{
		ClientID: openai.OAuthClientConfigByPlatform,
		ProxyURL: func(ctx context.Context, id int64) (string, bool, error) {
			proxy, err := proxies.GetProxy(ctx, id)
			if err != nil || proxy == nil {
				return "", false, err
			}
			return proxy.URL(), true, nil
		},
	})
	// Quota actions finish before quota and provider dependencies stop; the lifecycle manager reports timeouts.
	manager.Register(lifecycle.Hook{Name: "OpenAIQuotaActions", StopOrder: 14, Stop: handler.QuotaActions.StopContext})
	return handler
}

// provideOpenAIQuota binds quota operations to provider management, tokens,
// transport, and task coordination. Agent registration uses the fixed accounts
// endpoint, conditional credential persistence returns its error, and
// connection invalidation shares the application WS owner.
func provideOpenAIQuota(admin *provider.Admin, store *postgres.ProviderStore, proxies egress.ProxyRepository, transport httpclient.UpstreamTransport, token *provider.OpenAITokenSource, profiles *egressprovider.TLSProfiles, routers *egress.TLSFingerprintRouterService, connections *openaiws.OpenAIWSConnections, coordinator *provider.OpenAITaskCoordinator) *provider.OpenAIQuotaService {
	factory := &provideradapter.OpenAIQuotaFactory{
		Proxy: proxies.GetByID, Transport: transport, Profiles: profiles, Routers: routers,
		Tasks: coordinator,
		TaskOptions: provider.OpenAITaskOptions{
			Read: store.GetByID,
			Register: func(ctx context.Context, value *provider.Record) (string, error) {
				return provideradapter.RegisterAgentIdentityTask(ctx, value, "https://auth.openai.com/api/accounts")
			},
			Persist: func(ctx context.Context, value *provider.Record, credentials map[string]any) error {
				_, err := provider.PersistCredentials(ctx, store, value, credentials, slog.Warn)
				return err
			},
			Invalidate: connections.InvalidateProvider,
		},
	}
	return provider.NewOpenAIQuotaService(provider.OpenAIQuotaOptions{
		Configured: func() bool { return admin != nil && transport != nil },
		Read:       admin.GetProvider, Token: token.GetAccessToken, Client: factory.Client,
		SaveExtra: store.UpdateExtra, RedeemID: openai.GenerateOpenAIQuotaRedeemRequestID,
		Warn: slog.Warn, Info: slog.Info,
	})
}

// provideOpenAITokens constructs cached token lookup with refresh metrics and
// scheduling containment. The block callback applies only to nonnil OpenAI or
// Grok records; refresh uses the common coordinator and the OpenAI platform
// policy.
func provideOpenAITokens(store *postgres.ProviderStore, cache provider.AccessTokenCache, authorization *provider.OpenAIAuthorization, refresh *provider.OAuthRefreshAPI, blocks *provider.RuntimeBlockState) *provider.OpenAITokenSource {
	executor := &provider.OpenAITokenRefresher{Authorization: authorization}
	return &provider.OpenAITokenSource{
		Cache: cache,
		Block: func(record *provider.Record, until time.Time, reason string) {
			if record != nil && (record.Platform == capability.PlatformOpenAI || record.Platform == capability.PlatformGrok) {
				blocks.Block(record.ID, until, reason)
			}
		},
		Repository: store,
		SetError:   store.SetError,
		Metrics:    &provider.OpenAITokenMetricsStore{},
		Policy:     provider.OpenAIProviderRefreshPolicy(),
		Debug:      slog.Debug,
		Warn:       slog.Warn,
		Refresh: func(ctx context.Context, record *provider.Record, window time.Duration) (*provider.OAuthRefreshResult, error) {
			return refresh.RefreshIfNeeded(ctx, record, executor, window)
		},
	}
}

// provideOpenAIExecutionCredentials binds parent lookup and platform token
// readers when available. Missing store or token sources leave their callbacks
// unset, allowing the execution credential service to distinguish the
// capabilities supplied by assembly.
func provideOpenAIExecutionCredentials(store *postgres.ProviderStore, openai *provider.OpenAITokenSource, grok *provider.GrokTokenSource) *provider.OpenAIExecutionCredentials {
	out := &provider.OpenAIExecutionCredentials{}
	if store != nil {
		out.Parent = store.GetByID
	}
	if openai != nil {
		out.OpenAI = openai.GetAccessToken
	}
	if grok != nil {
		out.Grok = grok.GetAccessToken
	}
	return out
}
