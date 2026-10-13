package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestGrokTokenRefreshWindowWithJitter_StableAndBounded repeats one provider ID
// and samples fifty other IDs. It asserts stable bounded windows for that
// repeated ID and more than one window in the sample.
func TestGrokTokenRefreshWindowWithJitter_StableAndBounded(t *testing.T) {
	base := GrokTokenRefreshSkew
	w1 := GrokTokenRefreshWindowWithJitter(42, base)
	w2 := GrokTokenRefreshWindowWithJitter(42, base)
	require.Equal(t, w1, w2, "same provider id must yield stable window")
	require.GreaterOrEqual(t, w1, GrokTokenRefreshSkewMin)
	require.LessOrEqual(t, w1, base)

	// Different provider IDs may collide, but this sample should contain
	// more than one refresh window.
	seen := map[time.Duration]bool{}
	for id := int64(1); id <= 50; id++ {
		seen[GrokTokenRefreshWindowWithJitter(id, base)] = true
	}
	require.Greater(t, len(seen), 1, "jitter should spread windows across providers")
}

// TestGrokTokenRefresher_NeedsRefresh_UsesSkewFloor gives renewable credentials
// fifty minutes before expiry and then three hours before expiry. It asserts a
// small supplied window is raised enough to refresh the first case while
// leaving the later case usable.
func TestGrokTokenRefresher_NeedsRefresh_UsesSkewFloor(t *testing.T) {
	refresher := NewGrokTokenRefresher(nil)
	// Fifty minutes remaining falls inside the one-hour refresh window even
	// after jitter.
	expires := time.Now().Add(50 * time.Minute).UTC().Format(time.RFC3339)
	provider := &Record{
		ID:       7,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "at",
			"refresh_token": "rt",
			"expires_at":    expires,
		},
	}
	// NeedsRefresh raises this small window to GrokTokenRefreshSkew before
	// applying jitter.
	require.True(t, refresher.NeedsRefresh(provider, time.Minute))

	// A token expiring well beyond the window needs no refresh.
	provider.Credentials["expires_at"] = time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	require.False(t, refresher.NeedsRefresh(provider, time.Minute))
}
