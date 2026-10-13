package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// OIDC authorize URLs carry nonce and PKCE challenge values alongside the
// configured scopes. The fixture uses one issuer URL and fixed values to
// inspect those query parameters and the S256 method marker before any token
// exchange.

// TestBuildOIDCAuthorizeURLIncludesNonceAndPKCE builds an issuer authorize URL
// with fixed nonce and challenge inputs. It checks those query values, the
// S256 marker, and encoded scope text.
func TestBuildOIDCAuthorizeURLIncludesNonceAndPKCE(t *testing.T) {
	cfg := identity.OIDCOAuthOptions{
		AuthorizeURL: "https://issuer.example.com/auth",
		ClientID:     "cid",
		Scopes:       "openid email profile",
	}

	u, err := BuildOIDCAuthorizeURL(cfg, "state123", "nonce123", "challenge123", "https://app.example.com/callback")
	require.NoError(t, err)
	require.Contains(t, u, "nonce=nonce123")
	require.Contains(t, u, "code_challenge=challenge123")
	require.Contains(t, u, "code_challenge_method=S256")
	require.Contains(t, u, "scope=openid+email+profile")
}
