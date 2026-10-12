package provider

import (
	"context"
	"strconv"
	"time"
)

// ClaudeTokenRefresher supplies credential eligibility and merging for the shared refresh coordinator.
type ClaudeTokenRefresher struct {
	Authorization *ClaudeAuthorization
}

func (r *ClaudeTokenRefresher) CanRefresh(value *Record) bool {
	return CanRefreshClaude(value)
}

func (r *ClaudeTokenRefresher) NeedsRefresh(value *Record, window time.Duration) bool {
	return NeedsRefreshClaude(value, window)
}

func (r *ClaudeTokenRefresher) CacheKey(value *Record) string {
	return ClaudeTokenCacheKey(value)
}

func (r *ClaudeTokenRefresher) Refresh(ctx context.Context, value *Record) (map[string]any, error) {
	return RefreshClaudeCredentials(ctx, value, r.Authorization.RefreshProviderToken)
}

// CanRefreshClaude accepts Anthropic OAuth and setup-token providers.
// NeedsRefreshClaude checks expiry separately; exchange still requires a usable refresh token.
func CanRefreshClaude(provider *Record) bool {
	return provider.Platform == PlatformAnthropic && provider.IsOAuth()
}

// NeedsRefreshClaude reports whether expires_at is within the refresh window.
// Missing or unparseable expiry does not qualify for background refresh.
func NeedsRefreshClaude(provider *Record, refreshWindow time.Duration) bool {
	expiresAt := provider.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return false
	}
	return time.Until(*expiresAt) < refreshWindow
}

// RefreshClaudeCredentials exchanges tokens and merges the result into stored credentials.
// Fields omitted by the exchange retain their stored values.
func RefreshClaudeCredentials(ctx context.Context, provider *Record, exchange func(context.Context, *Record) (*ClaudeTokenInfo, error)) (map[string]any, error) {
	tokenInfo, err := exchange(ctx, provider)
	if err != nil {
		return nil, err
	}

	newCredentials := BuildClaudeProviderCredentials(tokenInfo)
	newCredentials = MergeCredentials(provider.Credentials, newCredentials)

	return newCredentials, nil
}

// BuildClaudeProviderCredentials converts Claude token information to credential fields.
// Refresh token and scope are included when supplied.
func BuildClaudeProviderCredentials(tokenInfo *ClaudeTokenInfo) map[string]any {
	creds := map[string]any{
		"access_token": tokenInfo.AccessToken,
		"token_type":   tokenInfo.TokenType,
		"expires_in":   strconv.FormatInt(tokenInfo.ExpiresIn, 10),
		"expires_at":   strconv.FormatInt(tokenInfo.ExpiresAt, 10),
	}
	if tokenInfo.RefreshToken != "" {
		creds["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.Scope != "" {
		creds["scope"] = tokenInfo.Scope
	}
	return creds
}
