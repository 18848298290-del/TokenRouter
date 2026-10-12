package provider

import (
	"context"
	"strings"
	"time"
)

type OpenAIRefreshTokenService interface {
	RefreshProviderToken(context.Context, *Record) (*OpenAITokenInfo, error)
}
type OpenAITokenRefresher struct{ Authorization OpenAIRefreshTokenService }

// CacheKey returns the token cache key also used for refresh locking.
func (r *OpenAITokenRefresher) CacheKey(provider *Record) string {
	return OpenAITokenCacheKey(provider)
}

// CanRefresh accepts OpenAI OAuth providers whose credentials are not inherited from a parent.
func (r *OpenAITokenRefresher) CanRefresh(provider *Record) bool {
	if provider.IsCredentialShadow() {
		return false
	}
	return provider.Platform == PlatformOpenAI && provider.Type == ProviderTypeOAuth
}

// NeedsRefresh checks expiry for providers with a refresh token, excluding PATs.
// When expiry is missing, a rate-limited provider is refreshed to avoid leaving an expired token unnoticed.
func (r *OpenAITokenRefresher) NeedsRefresh(provider *Record, refreshWindow time.Duration) bool {
	if provider.IsOpenAIPersonalAccessToken() {
		return false
	}
	if strings.TrimSpace(provider.GetOpenAIRefreshToken()) == "" {
		return false
	}
	expiresAt := provider.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return provider.IsRateLimited()
	}

	return time.Until(*expiresAt) < refreshWindow
}

// Refresh obtains token metadata and merges it with stored credentials.
// Fields omitted by the response remain, except OAuth-only fields removed for PAT credentials.
func (r *OpenAITokenRefresher) Refresh(ctx context.Context, provider *Record) (map[string]any, error) {
	tokenInfo, err := r.Authorization.RefreshProviderToken(ctx, provider)
	if err != nil {
		return nil, err
	}

	// Merge returned fields before applying PAT-specific cleanup.
	newCredentials := BuildOpenAIProviderCredentials(tokenInfo)
	newCredentials = MergeCredentials(provider.Credentials, newCredentials)
	newCredentials = NormalizeOpenAIPersonalAccessTokenCredentials(provider, tokenInfo, newCredentials)

	return newCredentials, nil
}
