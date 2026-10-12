package provider

import "context"

const (
	AntigravityForceTokenRefreshExtraKey       = "antigravity_force_token_refresh"
	AntigravityForceTokenRefreshReasonExtraKey = "antigravity_force_token_refresh_reason"
	AntigravityForceTokenRefreshAtExtraKey     = "antigravity_force_token_refresh_at"
)

// RefreshRequestClearer clears a one-time refresh request against the completed exchange identity.
type RefreshRequestClearer interface {
	ClearAntigravityRefreshRequest(context.Context, CredentialVersion) (bool, error)
}

func ClearedAntigravityRefreshRequest() map[string]any {
	return map[string]any{AntigravityForceTokenRefreshExtraKey: false, AntigravityForceTokenRefreshReasonExtraKey: "", AntigravityForceTokenRefreshAtExtraKey: ""}
}

// NeedsAntigravityRefreshRequest checks the server-side one-time flag; platform adapters parse upstream responses.
func NeedsAntigravityRefreshRequest(value *Record) bool {
	return value != nil && value.Platform == PlatformAntigravity && value.Type == ProviderTypeOAuth && value.GetExtraBool(AntigravityForceTokenRefreshExtraKey)
}
