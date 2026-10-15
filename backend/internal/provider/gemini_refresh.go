package provider

import (
	"context"
	"time"
)

// gemini_refresh.go adapts Gemini token exchange and expiry checks to
// background credential refresh.
// Gemini background refresh accepts OAuth records with a parseable expiry
// inside the caller supplied window. The exchanger builds platform fields and
// merges them with existing credentials, allowing omitted metadata to remain
// available for coordinated persistence.

// GeminiTokenRefresher supplies Gemini exchange policy to shared refresh
// coordination.
type GeminiTokenRefresher struct {
	Authorization *GeminiAuthorization // configured Gemini authorization service
	// Key is required when CacheKey is called. It selects the namespace shared by
	// request and background token maintenance.
	Key func(*Record) string
}

// CanRefresh reports whether the record uses supported Gemini OAuth
// authentication.
func (r *GeminiTokenRefresher) CanRefresh(value *Record) bool {
	return CanRefreshGemini(value)
}

// NeedsRefresh delegates Gemini eligibility and expiry-window qualification to
// the shared predicate. Missing or invalid expiry rejects refresh; callers
// provide a nonnil record and the intended window.
func (r *GeminiTokenRefresher) NeedsRefresh(value *Record, window time.Duration) bool {
	return NeedsRefreshGemini(value, window)
}

// CacheKey invokes the configured Gemini token-cache namespace callback.
// Supply a nonnil Key before refresh coordination reads it; the callback
// receives the same provider record supplied here.
func (r *GeminiTokenRefresher) CacheKey(value *Record) string {
	return r.Key(value)
}

// Refresh exchanges and merges Gemini credentials through the configured
// authorization service. Supply Authorization and a nonnil record; errors
// propagate before any returned credential map can be persisted.
func (r *GeminiTokenRefresher) Refresh(ctx context.Context, value *Record) (map[string]any, error) {
	return RefreshGeminiCredentials(ctx, value, r.Authorization)
}

// CanRefreshGemini reports whether a nonnil record is Gemini OAuth.
func CanRefreshGemini(provider *Record) bool {
	return provider.Platform == PlatformGemini && provider.Type == ProviderTypeOAuth
}

// NeedsRefreshGemini qualifies Gemini OAuth whose expiry is strictly inside
// refreshWindow. Missing or unparseable expiry returns false, and the supplied
// window controls this check rather than a platform constant.
func NeedsRefreshGemini(provider *Record, refreshWindow time.Duration) bool {
	if !CanRefreshGemini(provider) {
		return false
	}
	expiresAt := provider.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return false
	}
	return time.Until(*expiresAt) < refreshWindow
}

// RefreshGeminiCredentials exchanges a nonnil record through authorization and
// returns merged credentials. Exchange errors propagate; fields omitted by the
// platform builder retain their stored values, with persistence left to the
// coordinator.
func RefreshGeminiCredentials(ctx context.Context, provider *Record, authorization *GeminiAuthorization) (map[string]any, error) {
	tokenInfo, err := authorization.RefreshProviderToken(ctx, provider)
	if err != nil {
		return nil, err
	}

	newCredentials := authorization.BuildProviderCredentials(tokenInfo)
	// Discovery and exchange can omit stored metadata fields. Merge retains those
	// keys beside refreshed values so coordinated persistence can keep the complete
	// credential record.
	newCredentials = MergeCredentials(provider.Credentials, newCredentials)

	return newCredentials, nil
}
