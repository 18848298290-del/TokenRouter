package provider

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/vertex"
)

// GeminiTokenCacheKey uses service-account identity keys when parsing succeeds, otherwise OAuth provider-ID keys.
func GeminiTokenCacheKey(value *provider.Record) string {
	if value != nil && value.Type == provider.ProviderTypeServiceAccount {
		if key, err := ParseVertexServiceAccountKey(value); err == nil {
			return VertexServiceAccountCacheKey(value, key)
		}
	}
	return provider.GeminiOAuthTokenCacheKey(value)
}

// ParseVertexServiceAccountKey parses the provider's stored service-account JSON.
func ParseVertexServiceAccountKey(value *provider.Record) (*google.ServiceAccountKey, error) {
	raw, err := provider.VertexServiceAccountJSON(value)
	if err != nil {
		return nil, err
	}
	return vertex.ParseVertexServiceAccountJSON(raw)
}

// vertexServiceAccountProxyURL returns the bound proxy URL, or an empty string when unavailable.
func vertexServiceAccountProxyURL(value *provider.Record) string {
	if value == nil || value.ProxyID == nil || value.Proxy == nil {
		return ""
	}
	return value.Proxy.URL()
}

// VertexServiceAccountCacheKey includes the parsed key identity, with provider-only keys when parsing data is absent.
func VertexServiceAccountCacheKey(value *provider.Record, key *google.ServiceAccountKey) string {
	var id int64
	if value != nil {
		id = value.ID
	}
	if key == nil {
		if value == nil {
			return "vertex:service_account:"
		}
		return provider.VertexServiceAccountCacheKey(id, "", "", false)
	}
	return provider.VertexServiceAccountCacheKey(id, key.ClientEmail, key.PrivateKeyID, true)
}

// VertexServiceAccountAccessToken exchanges tokens through the provider's shared cache and refresh coordination.
func VertexServiceAccountAccessToken(ctx context.Context, cache provider.AccessTokenCache, value *provider.Record) (string, error) {
	key, err := ParseVertexServiceAccountKey(value)
	if err != nil {
		return "", err
	}
	return provider.GetVertexServiceAccountAccessToken(ctx, provider.VertexTokenOptions{
		ProviderID: value.ID,
		CacheKey:   VertexServiceAccountCacheKey(value, key),
		Cache:      cache,
		Warn:       slog.Warn,
		Exchange: func(ctx context.Context) (string, time.Duration, error) {
			return vertex.ExchangeServiceAccountToken(ctx, key, vertexServiceAccountProxyURL(value))
		},
	})
}
