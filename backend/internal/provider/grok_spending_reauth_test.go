package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProviderGrokNeedsReauth supplies a nil provider, an Extra boolean, and a
// spending-limit error message. It asserts the two stored signals require
// reauthorization while the absent provider does not.
func TestProviderGrokNeedsReauth(t *testing.T) {
	require.False(t, GrokNeedsReauth(nil))
	require.True(t, GrokNeedsReauth(&Record{
		Extra: map[string]any{"grok_needs_reauth": true},
	}))
	require.True(t, GrokNeedsReauth(&Record{
		Status:       StatusError,
		ErrorMessage: "Grok spending limit reached; reauthorize or wait for billing reset",
	}))
}
