package provider

import (
	"context"
	"strconv"
	"time"
)

// Claude refresh uses the provider's stored expiry and merges exchanged
// credentials before coordinated persistence. OAuth and Setup Token records
// share eligibility at this adapter, while the authorization exchange still
// rejects a missing usable refresh token.

// ClaudeTokenRefresher supplies Anthropic exchange rules to shared refresh
// coordination.
type ClaudeTokenRefresher struct {
	Authorization *ClaudeAuthorization // configured Claude authorization service
}

// CanRefresh reports Anthropic OAuth-like eligibility through the shared
// predicate.
func (r *ClaudeTokenRefresher) CanRefresh(value *Record) bool {
	return CanRefreshClaude(value)
}

// NeedsRefresh checks stored expiry against the caller supplied refresh
// window. It delegates expiry qualification alone, so coordination separately
// checks CanRefresh before selecting this exchanger. Pass a nonnil record.
func (r *ClaudeTokenRefresher) NeedsRefresh(value *Record, window time.Duration) bool {
	return NeedsRefreshClaude(value, window)
}

// CacheKey returns the Claude token namespace for the supplied record.
func (r *ClaudeTokenRefresher) CacheKey(value *Record) string {
	return ClaudeTokenCacheKey(value)
}

// Refresh exchanges Claude tokens through the configured authorization service
// and merges credentials. Supply a nonnil Authorization and record; exchange
// errors propagate and the coordinator owns subsequent persistence.
func (r *ClaudeTokenRefresher) Refresh(ctx context.Context, value *Record) (map[string]any, error) {
	return RefreshClaudeCredentials(ctx, value, r.Authorization.RefreshProviderToken)
}

// CanRefreshClaude accepts nonnil Anthropic OAuth and Setup Token records. The
// predicate checks category alone; NeedsRefreshClaude checks expiry and the
// eventual exchange requires a usable refresh token.
func CanRefreshClaude(provider *Record) bool {
	return provider.Platform == PlatformAnthropic && provider.IsOAuth()
}

// NeedsRefreshClaude reports whether stored expiry is strictly inside
// refreshWindow. Missing or unparseable expiry returns false; this predicate
// does not check platform or authentication eligibility, which callers apply
// separately.
func NeedsRefreshClaude(provider *Record, refreshWindow time.Duration) bool {
	expiresAt := provider.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return false
	}
	return time.Until(*expiresAt) < refreshWindow
}

// RefreshClaudeCredentials exchanges the nonnil record and merges returned
// credential fields. Supply a nonnil exchange callback returning nonnil token
// data on success; errors propagate and omitted fields retain stored values.
// The coordinator persists the returned map.
func RefreshClaudeCredentials(ctx context.Context, provider *Record, exchange func(context.Context, *Record) (*ClaudeTokenInfo, error)) (map[string]any, error) {
	tokenInfo, err := exchange(ctx, provider)
	if err != nil {
		return nil, err
	}

	newCredentials := BuildClaudeProviderCredentials(tokenInfo)
	// Optional renewal and scope keys are omitted by the builder when empty.
	// Merge keeps their stored values and other omitted metadata for persistence.
	newCredentials = MergeCredentials(provider.Credentials, newCredentials)

	return newCredentials, nil
}

// BuildClaudeProviderCredentials converts nonnil token information into a new
// credential map. Expiry values become decimal strings; refresh_token and
// scope are included only when nonempty so merging can retain omitted stored
// fields.
func BuildClaudeProviderCredentials(tokenInfo *ClaudeTokenInfo) map[string]any {
	creds := map[string]any{
		"access_token": tokenInfo.AccessToken,
		"token_type":   tokenInfo.TokenType,
		// Store expiry seconds as decimal strings accepted by Record credential
		// readers.
		"expires_in": strconv.FormatInt(tokenInfo.ExpiresIn, 10),
		"expires_at": strconv.FormatInt(tokenInfo.ExpiresAt, 10),
	}
	if tokenInfo.RefreshToken != "" {
		creds["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.Scope != "" {
		creds["scope"] = tokenInfo.Scope
	}
	return creds
}
