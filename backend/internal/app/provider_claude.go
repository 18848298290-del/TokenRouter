package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	providerauth "github.com/TokenFlux/TokenRouter/internal/provider"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// provider_claude.go assembles Claude authorization and token sources for
// gateway message credentials. Claude authorization and gateway credential
// lookup use the same provider token source and refresh coordinator. OAuth
// tokens follow Claude refresh policy, while Vertex credentials use the shared
// access-token cache through the service-account adapter before message
// execution receives them.

// provideClaudeAuthorizationHTTP binds Claude OAuth flows to their
// administration handler.
func provideClaudeAuthorizationHTTP(source *providerauth.ClaudeAuthorization) *providerhttp.ClaudeOAuthHandler {
	return providerhttp.NewClaudeOAuthHandler(source)
}

// provideClaudeAuthorization constructs platform OAuth with stored proxy
// selection. Lookup errors and absent proxies produce an unavailable URL
// result; proxies supplies the required read callback when authorization
// resolves a selected proxy.
func provideClaudeAuthorization(proxies egress.ProxyRepository, client providerauth.ClaudeOAuthClient) *providerauth.ClaudeAuthorization {
	options := provideradapter.ClaudeAuthorizationOptions(func(ctx context.Context, id int64) (string, bool) {
		proxy, err := proxies.GetByID(ctx, id)
		// The authorization resolver cannot use a failed or absent proxy read.
		// Its availability flag lets the Claude flow handle that selection before exchanging credentials.
		if err != nil || proxy == nil {
			return "", false
		}
		return proxy.URL(), true
	})
	return providerauth.NewClaudeAuthorization(client, options)
}

// provideClaudeTokens binds cached Claude OAuth and Vertex tokens to provider
// storage. It delegates refresh to the shared coordinator and platform policy,
// and service-account exchange uses the same access-token cache.
func provideClaudeTokens(store *postgres.ProviderStore, cache providerauth.AccessTokenCache, authorization *providerauth.ClaudeAuthorization, refresh *providerauth.OAuthRefreshAPI) *providerauth.ClaudeTokenSource {
	executor := &providerauth.ClaudeTokenRefresher{Authorization: authorization}
	return &providerauth.ClaudeTokenSource{Options: providerauth.ClaudeTokenOptions{
		Cache: cache, Repository: store, Policy: providerauth.ClaudeProviderRefreshPolicy(),
		Debug: slog.Debug, Warn: slog.Warn,
		Vertex: func(ctx context.Context, value *providerauth.Record) (string, error) {
			return provideradapter.VertexServiceAccountAccessToken(ctx, cache, value)
		},
		Refresh: func(ctx context.Context, value *providerauth.Record, window time.Duration) (*providerauth.OAuthRefreshResult, error) {
			return refresh.RefreshIfNeeded(ctx, value, executor, window)
		},
	}}
}

// provideMessageCredentials binds Claude lookup when its token source is
// supplied. Otherwise the callback remains unset; MessageCredentialSource
// retains its platform rules for deciding which stored or delegated credentials
// to use.
func provideMessageCredentials(claude *providerauth.ClaudeTokenSource) *providerauth.MessageCredentialSource {
	result := &providerauth.MessageCredentialSource{}
	if claude != nil {
		result.Claude = claude.GetAccessToken
	}
	return result
}
