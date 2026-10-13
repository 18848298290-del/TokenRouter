package httpapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Pending login redirects accept a bounded path beginning with one slash.
// These examples reject external URLs, protocol-relative forms, CRLF content,
// and an oversized path while retaining a valid trimmed dashboard path for the
// browser continuation.

// TestSanitizeFrontendRedirectPath compares valid dashboard paths with
// external, protocol-relative, newline-containing, and oversized inputs. It
// checks that accepted paths retain their trimmed text and rejected values
// become empty.
func TestSanitizeFrontendRedirectPath(t *testing.T) {
	require.Equal(t, "/dashboard", SanitizeFrontendRedirectPath("/dashboard"))
	require.Equal(t, "/dashboard", SanitizeFrontendRedirectPath(" /dashboard "))
	require.Equal(t, "", SanitizeFrontendRedirectPath("dashboard"))
	require.Equal(t, "", SanitizeFrontendRedirectPath("//evil.com"))
	require.Equal(t, "", SanitizeFrontendRedirectPath("https://evil.com"))
	require.Equal(t, "", SanitizeFrontendRedirectPath("/\nfoo"))

	long := "/" + strings.Repeat("a", linuxDoOAuthMaxRedirectLen)
	require.Equal(t, "", SanitizeFrontendRedirectPath(long))
}
