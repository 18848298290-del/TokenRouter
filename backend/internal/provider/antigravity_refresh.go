package provider

import (
	"context"
	"strings"
	"time"
)

const AntigravityRefreshWindow = 15 * time.Minute

type AntigravityRefreshRules struct {
	RefreshProviderToken     func(context.Context, *Record) (*AntigravityTokenInfo, error)
	BuildProviderCredentials func(*AntigravityTokenInfo) map[string]any
	Printf, Logf             func(string, ...any)
}

// CacheKey shares the provider lock key with request-time and background refresh.
func (r *AntigravityRefreshRules) CacheKey(value *Record) string {
	return AntigravityTokenCacheKey(value)
}

// CanRefresh reports whether this is an Antigravity OAuth provider.
func (r *AntigravityRefreshRules) CanRefresh(provider *Record) bool {
	return provider.Platform == PlatformAntigravity && provider.Type == ProviderTypeOAuth
}

// NeedsRefresh checks expiry and forced-refresh state.
// Antigravity uses a fixed 15-minute window regardless of the global setting.
func (r *AntigravityRefreshRules) NeedsRefresh(provider *Record, _ time.Duration) bool {
	if !r.CanRefresh(provider) {
		return false
	}
	if NeedsAntigravityRefreshRequest(provider) {
		return true
	}
	expiresAt := provider.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return false
	}
	timeUntilExpiry := time.Until(*expiresAt)
	needsRefresh := timeUntilExpiry < AntigravityRefreshWindow
	if needsRefresh {
		r.Printf("[AntigravityTokenRefresher] Provider %d needs refresh: expires_at=%s, time_until_expiry=%v, window=%v\n",
			provider.ID, expiresAt.Format("2006-01-02 15:04:05"), timeUntilExpiry, AntigravityRefreshWindow)
	}
	return needsRefresh
}

// AntigravityForceTokenRefreshExtra builds persistent forced-refresh fields.
func AntigravityForceTokenRefreshExtra(reason string) map[string]any {
	return map[string]any{
		AntigravityForceTokenRefreshExtraKey:       true,
		AntigravityForceTokenRefreshReasonExtraKey: reason,
		AntigravityForceTokenRefreshAtExtraKey:     time.Now().UTC().Format(time.RFC3339),
	}
}

// Refresh refreshes tokens and merges the returned credential fields.
func (r *AntigravityRefreshRules) Refresh(ctx context.Context, provider *Record) (map[string]any, error) {
	tokenInfo, err := r.RefreshProviderToken(ctx, provider)
	if err != nil {
		return nil, err
	}

	newCredentials := r.BuildProviderCredentials(tokenInfo)
	// Retain stored fields omitted by the refresh response.
	newCredentials = MergeCredentials(provider.Credentials, newCredentials)

	// Keep a nonempty stored project_id when discovery returns an empty value.
	// The stored project remains usable if LoadCodeAssist fails.
	if newProjectID, _ := newCredentials["project_id"].(string); newProjectID == "" {
		if oldProjectID := strings.TrimSpace(provider.GetCredential("project_id")); oldProjectID != "" {
			newCredentials["project_id"] = oldProjectID
		}
	}

	// Warn on project discovery failure while retaining the refreshed tokens.
	// A later refresh can retry discovery after a transient network failure.
	// The token exchange itself has already succeeded.
	if tokenInfo.ProjectIDMissing {
		if tokenInfo.ProjectID != "" {
			// Discovery failed, but an existing project ID remains available.
			r.Logf("[AntigravityTokenRefresher] Provider %d: LoadCodeAssist 临时失败，保留旧 project_id", provider.ID)
		} else {
			// Allow future discovery attempts when no project ID has ever been obtained.
			r.Logf("[AntigravityTokenRefresher] Provider %d: LoadCodeAssist 失败，project_id 缺失，但 token 已更新，将在下次刷新时重试", provider.ID)
		}
	}

	return newCredentials, nil
}
