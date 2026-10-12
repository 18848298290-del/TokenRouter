package provider

import "context"

// Antigravity can carry a one-time server request to refresh its OAuth
// credentials before ordinary expiry checks require it. Success and permanent
// failure clear its flag, reason and timestamp together through storage that
// compares authentication identity. The predicate requires the Antigravity OAuth
// combination.

// Antigravity refresh-request keys record the one-time request and the reason
// and timestamp written with its flag.
const (
	// AntigravityForceTokenRefreshExtraKey stores the boolean request flag.
	AntigravityForceTokenRefreshExtraKey = "antigravity_force_token_refresh"
	// AntigravityForceTokenRefreshReasonExtraKey stores the request reason.
	AntigravityForceTokenRefreshReasonExtraKey = "antigravity_force_token_refresh_reason"
	// AntigravityForceTokenRefreshAtExtraKey stores the request timestamp text.
	AntigravityForceTokenRefreshAtExtraKey = "antigravity_force_token_refresh_at"
)

// RefreshRequestClearer conditionally removes a one-time Antigravity refresh
// request after exchange handling.
type RefreshRequestClearer interface {
	// ClearAntigravityRefreshRequest clears all request fields when the expected
	// Antigravity OAuth identity still matches. False reports an unsupported target
	// or comparison conflict; callers retain their local request fields in that case.
	ClearAntigravityRefreshRequest(context.Context, CredentialVersion) (bool, error)
}

// ClearedAntigravityRefreshRequest returns a fresh patch resetting the flag,
// reason and timestamp.
func ClearedAntigravityRefreshRequest() map[string]any {
	return map[string]any{AntigravityForceTokenRefreshExtraKey: false, AntigravityForceTokenRefreshReasonExtraKey: "", AntigravityForceTokenRefreshAtExtraKey: ""}
}

// NeedsAntigravityRefreshRequest reports a set one-time flag on an Antigravity
// OAuth provider.
func NeedsAntigravityRefreshRequest(value *Record) bool {
	return value != nil && value.Platform == PlatformAntigravity && value.Type == ProviderTypeOAuth && value.GetExtraBool(AntigravityForceTokenRefreshExtraKey)
}
