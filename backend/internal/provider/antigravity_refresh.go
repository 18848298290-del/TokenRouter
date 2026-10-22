package provider

import (
	"context"
	"strings"
	"time"
)

// antigravity_refresh.go qualifies Antigravity OAuth refreshes and merges
// exchanged credentials with retained provider settings.
// Antigravity refresh qualification uses its fixed fifteen-minute window and
// an independent force flag. Refresh exchanges tokens even when project
// discovery is incomplete, so returned credentials can retain a usable stored
// project while the platform records missing discovery evidence.
//
// The exchange builder supplies token fields, and merging retains stored keys
// that it omitted. A blank project_id gets the previous trimmed value when
// available; discovery warnings therefore remain separate from token success.
// Refresh returns the resulting map for the coordinator to persist.
// Qualification checks category before force or expiry, and logging callbacks
// report those choices without changing admission or introducing another retry
// loop.

// AntigravityRefreshWindow qualifies near-expiry credentials independently of
// the global background refresh window setting.
const AntigravityRefreshWindow = 15 * time.Minute

// AntigravityRefreshRules combines refresh qualification, exchange callbacks,
// and discovery diagnostics.
type AntigravityRefreshRules struct {
	// RefreshProviderToken performs the exchange and returns nonnil token data on
	// success. Refresh propagates its error before credential construction.
	RefreshProviderToken func(context.Context, *Record) (*AntigravityTokenInfo, error)
	// BuildProviderCredentials returns writable token fields for merging. Refresh
	// retains stored keys omitted from that map and repairs blank project
	// identity.
	BuildProviderCredentials func(*AntigravityTokenInfo) map[string]any
	// Printf and Logf are required in their diagnostic branches. Printf reports
	// near-expiry qualification; Logf reports successful exchange with incomplete
	// project discovery.
	// Printf: refresh qualification logger; Logf: discovery diagnostic logger.
	Printf, Logf func(string, ...any)
}

// CacheKey returns the Antigravity namespace shared by request and background
// refresh locks.
func (r *AntigravityRefreshRules) CacheKey(value *Record) string {
	return AntigravityTokenCacheKey(value)
}

// CanRefresh reports whether a nonnil record is Antigravity OAuth.
func (r *AntigravityRefreshRules) CanRefresh(provider *Record) bool {
	return provider.Platform == PlatformAntigravity && provider.Type == ProviderTypeOAuth
}

// NeedsRefresh qualifies Antigravity OAuth by force flag or stored expiry. The
// supplied global duration is ignored; absent expiry rejects ordinary refresh,
// and Printf is required when expiry falls inside the fixed window.
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

// AntigravityForceTokenRefreshExtra returns a new patch requesting token
// refresh with its reason. The patch captures current UTC time as RFC3339 text
// for later observation and cleanup.
func AntigravityForceTokenRefreshExtra(reason string) map[string]any {
	return map[string]any{
		AntigravityForceTokenRefreshExtraKey:       true,
		AntigravityForceTokenRefreshReasonExtraKey: reason,
		AntigravityForceTokenRefreshAtExtraKey:     time.Now().UTC().Format(time.RFC3339),
	}
}

// Refresh exchanges Antigravity tokens and returns merged credentials for
// persistence. Configure both exchange and builder callbacks; exchange errors
// return before merge, and Logf is required when discovery reports missing
// project information.
//
// The token result remains usable even when project discovery fails. Stored
// fields omitted by the builder survive merging, and an explicitly blank
// project result is replaced with the previous trimmed project when one
// exists. Discovery flags control diagnostics, while the coordinator decides
// persistence and later refresh attempts.
func (r *AntigravityRefreshRules) Refresh(ctx context.Context, provider *Record) (map[string]any, error) {
	tokenInfo, err := r.RefreshProviderToken(ctx, provider)
	if err != nil {
		return nil, err
	}

	newCredentials := r.BuildProviderCredentials(tokenInfo)
	// The exchange builder supplies token fields while stored keys it omits retain
	// their previous values, keeping provider metadata available for persistence.
	newCredentials = MergeCredentials(provider.Credentials, newCredentials)

	// An empty project result must not erase a previously usable project. Keep
	// its trimmed stored identity while accepting token fields from this exchange.
	if newProjectID, _ := newCredentials["project_id"].(string); newProjectID == "" {
		if oldProjectID := strings.TrimSpace(provider.GetCredential("project_id")); oldProjectID != "" {
			newCredentials["project_id"] = oldProjectID
		}
	}

	// Report project discovery failure while retaining the refreshed tokens.
	// A later refresh can retry discovery after a transient network failure.
	// The token exchange itself has already succeeded.
	if tokenInfo.ProjectIDMissing {
		if tokenInfo.ProjectID != "" {
			// Discovery failed, but an existing project ID remains available.
			r.Logf("[INFO] [AntigravityTokenRefresher] Provider %d: LoadCodeAssist temporarily failed, retaining existing project_id", provider.ID)
		} else {
			// Allow future discovery attempts when no project ID
			// has ever been obtained.
			r.Logf("[INFO] [AntigravityTokenRefresher] Provider %d: LoadCodeAssist failed and project_id is missing; token updated, will retry on next refresh", provider.ID)
		}
	}

	return newCredentials, nil
}
