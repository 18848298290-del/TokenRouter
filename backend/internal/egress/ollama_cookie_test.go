package egress

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNormalizeOllamaCloudUsageCookieAllowlist supplies session, tracking, and
// sharded cookie names with whitespace and malformed variants. It checks
// filtering, retained order and casing, surrounding whitespace handling, and
// rejection of injection, duplicates, attributes, missing sessions, and
// oversized raw input.
func TestNormalizeOllamaCloudUsageCookieAllowlist(t *testing.T) {
	normalized, err := NormalizeOllamaCloudUsageCookie(" tracking=discard ; wos-session=secret ; __Secure-authjs.session-token.0=part-a ; device=discard ")
	require.NoError(t, err)
	require.Equal(t, "wos-session=secret; __Secure-authjs.session-token.0=part-a", normalized)

	normalized, err = NormalizeOllamaCloudUsageCookie(" \t\r\nwos-session=secret; tracking=discard\r\n\t ")
	require.NoError(t, err)
	require.Equal(t, "wos-session=secret", normalized)

	_, err = NormalizeOllamaCloudUsageCookie("wos-session=secret\r\nHost: evil.test")
	require.ErrorContains(t, err, "invalid header")

	for _, allowed := range []string{
		"wos-session", "__Secure-session", "session", "ollama_session", "__Host-ollama_session",
		"next-auth.session-token", "next-auth.session-token.0", "__Secure-next-auth.session-token.12",
		"authjs.session-token", "__Secure-authjs.session-token.1",
	} {
		normalized, err := NormalizeOllamaCloudUsageCookie(allowed + "=value")
		require.NoError(t, err, allowed)
		require.Equal(t, allowed+"=value", normalized)
	}

	for _, invalid := range []string{
		"", "Domain=ollama.com; wos-session=x", "wos-session=x; Path=/",
		"wos-session=x; wos-session=y", "Secure", "tracking=only", "__session=arbitrary",
		"authjs.session-token.bad=not-a-shard", "Authjs.session-token=wrong-case",
	} {
		_, err := NormalizeOllamaCloudUsageCookie(invalid)
		require.Error(t, err, invalid)
	}
	_, err = NormalizeOllamaCloudUsageCookie("wos-session=" + strings.Repeat("x", ollamaCloudUsageMaxSessionBytes))
	require.ErrorContains(t, err, "too large")
}
