package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// TestCompletionKeySnapshotPreservesSourceGroup captures a Key twice while
// changing its group multiplier between captures. The source keeps its group
// pointer, and each snapshot retains its own observed multiplier plus billing
// mode and rate-limit presence.
func TestCompletionKeySnapshotPreservesSourceGroup(t *testing.T) {
	group := &routing.Group{ID: 100, RateMultiplier: 2}
	key := &apikey.APIKey{Group: group, BillingMode: apikey.APIKeyBillingModeBalance, RateLimit5h: 1}
	first := ProjectCompletionKey(key)
	require.Same(t, group, key.Group)
	require.Equal(t, apikey.APIKeyBillingModeBalance, first.BillingMode)
	require.True(t, first.HasRateLimits)
	group.RateMultiplier = 3
	second := ProjectCompletionKey(key)
	require.Equal(t, 2.0, first.Group.RateMultiplier)
	require.Equal(t, 3.0, second.Group.RateMultiplier)
}
