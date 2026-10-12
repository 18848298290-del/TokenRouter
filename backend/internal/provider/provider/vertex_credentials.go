package provider

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/vertex"
)

// Vertex access tokens are cached under service-account identity when its stored
// JSON can be parsed. Email and private-key ID determine that identity, allowing
// providers with the same key to reuse the token, while missing parsed key data
// uses a provider-specific cache suffix.
//
// The credential adapter selects the stored JSON, resolves a loaded proxy, and
// passes the resulting exchange to the shared token coordinator. Gemini's cache
// key entry falls back to its ordinary project-or-provider key when parsing
// fails; token acquisition instead returns parsing errors before consulting the
// cache, so malformed service-account input cannot initiate an exchange.

// GeminiTokenCacheKey selects a Vertex identity key when service-account
// parsing succeeds.
// Otherwise it uses GeminiOAuthTokenCacheKey, which prefers project_id and then
// the provider ID. A nonnil record is required for that ordinary Gemini path.
func GeminiTokenCacheKey(value *provider.Record) string {
	if value != nil && value.Type == provider.ProviderTypeServiceAccount {
		if key, err := ParseVertexServiceAccountKey(value); err == nil {
			return VertexServiceAccountCacheKey(value, key)
		}
	}
	// Parsing failure still needs a stable refresh key for Gemini maintenance; use
	// its project-or-provider namespace, with actual token parsing left to
	// acquisition.
	return provider.GeminiOAuthTokenCacheKey(value)
}

// ParseVertexServiceAccountKey parses the service-account JSON selected from
// provider credentials.
// Credential-selection or JSON-validation errors are returned to the caller.
// Parsing validates required key fields but does not perform a token exchange
// or verify that the private key can complete one.
func ParseVertexServiceAccountKey(value *provider.Record) (*google.ServiceAccountKey, error) {
	raw, err := provider.VertexServiceAccountJSON(value)
	if err != nil {
		return nil, err
	}
	return vertex.ParseVertexServiceAccountJSON(raw)
}

// vertexServiceAccountProxyURL returns the loaded proxy URL used for token
// exchange.
// Nil records, missing proxy IDs, and unloaded proxy records return an empty
// string. This helper does not load a referenced proxy from storage.
func vertexServiceAccountProxyURL(value *provider.Record) string {
	if value == nil || value.ProxyID == nil || value.Proxy == nil {
		return ""
	}
	return value.Proxy.URL()
}

// VertexServiceAccountCacheKey identifies the cache entry for a parsed service
// account.
// A parsed key contributes its email and private-key ID to a shortened SHA-256
// digest, independent of the provider ID. With no key, a nonnil record uses its
// provider ID, while no record and no key produce the bare namespace.
//
// These choices keep a usable parsed identity shared across matching providers
// and keep unavailable identities on a provider-specific path. The digest names
// cache and refresh-lock entries; it does not authenticate service-account input
// or include the private-key bytes.
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

// VertexServiceAccountAccessToken obtains a token through the shared cache
// coordinator.
// It parses stored key fields before any cache lookup, then binds exchange to
// the record's loaded proxy. Cache and refresh-lock failures can still permit an
// exchange; cancellation during lock contention returns the cancellation error.
// A nil cache performs the exchange directly.
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
