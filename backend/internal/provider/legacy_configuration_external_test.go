package provider_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// The public deprecated-Extra cleanup accepts the saved map directly and
// removes retired billing and probe keys in place. The external-package test
// verifies that consumers can use that cleanup while retaining an unrelated
// configuration entry in the same object.

// TestDiscardDeprecatedProviderExtra calls the public cleanup on malformed
// retired fields and one ordinary entry. The supplied object retains just that
// ordinary entry after the in-place removal.
func TestDiscardDeprecatedProviderExtra(t *testing.T) {
	extra := map[string]any{
		"openai_long_context_billing_enabled": "malformed",
		"upstream_billing_probe":              map[string]any{"status": "ok"},
		"upstream_billing_probe_enabled":      true,
		"preserved":                           "value",
	}

	provider.DiscardDeprecatedProviderExtra(extra)

	require.Equal(t, map[string]any{"preserved": "value"}, extra)
}
