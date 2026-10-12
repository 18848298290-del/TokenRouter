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

// provideClaudeAuthorizationHTTP binds Claude authorization to its HTTP handler.
func provideClaudeAuthorizationHTTP(source *providerauth.ClaudeAuthorization) *providerhttp.ClaudeOAuthHandler {
	return providerhttp.NewClaudeOAuthHandler(source)
}

func provideClaudeAuthorization(proxies egress.ProxyRepository, client providerauth.ClaudeOAuthClient) *providerauth.ClaudeAuthorization {
	options := provideradapter.ClaudeAuthorizationOptions(func(ctx context.Context, id int64) (string, bool) {
		proxy, err := proxies.GetByID(ctx, id)
		if err != nil || proxy == nil {
			return "", false
		}
		return proxy.URL(), true
	})
	return providerauth.NewClaudeAuthorization(client, options)
}

// provideClaudeTokens binds OAuth and Vertex credential sources with their cache policies.
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

// provideMessageCredentials reuses Claude and Vertex token lookup; other platforms use stored credentials.
func provideMessageCredentials(claude *providerauth.ClaudeTokenSource) *providerauth.MessageCredentialSource {
	result := &providerauth.MessageCredentialSource{}
	if claude != nil {
		result.Claude = claude.GetAccessToken
	}
	return result
}
