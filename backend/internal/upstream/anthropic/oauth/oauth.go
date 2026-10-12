package oauth

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/oauthpkce"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// Claude OAuth Constants.
const (
	// ClientID identifies the Claude OAuth client.
	ClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// OAuth endpoints.
	AuthorizeURL = "https://claude.com/cai/oauth/authorize"
	TokenURL     = "https://platform.claude.com/v1/oauth/token"
	RedirectURI  = "https://platform.claude.com/oauth/code/callback"

	// ScopeOAuth includes API key creation for browser authorization.
	ScopeOAuth = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	// ScopeAPI omits API key creation, which the internal API does not support.
	ScopeAPI = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	// ScopeInference limits setup tokens to inference.
	ScopeInference = "user:inference"
)

type (
	TokenResponse = wire.OAuthTokenResponse
	OrgInfo       = wire.OAuthOrgInfo
	ProviderInfo  = wire.OAuthProviderInfo
)

// GenerateRandomBytes generates cryptographically secure random bytes.
func GenerateRandomBytes(n int) ([]byte, error) {
	return oauthpkce.RandomBytes(n)
}

// GenerateState generates a random state string for OAuth (base64url encoded).
func GenerateState() (string, error) {
	bytes, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return base64URLEncode(bytes), nil
}

// GenerateSessionID returns a random 128-bit session ID encoded as hex.
func GenerateSessionID() (string, error) {
	bytes, err := GenerateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GenerateCodeVerifier generates a PKCE code verifier (RFC 7636).
// It encodes 32 random bytes as 43 unpadded base64url characters.
func GenerateCodeVerifier() (string, error) {
	return oauthpkce.Verifier()
}

// GenerateCodeChallenge generates a PKCE code challenge using S256 method.
func GenerateCodeChallenge(verifier string) string {
	return oauthpkce.Challenge(verifier)
}

// base64URLEncode encodes bytes to base64url without padding.
func base64URLEncode(data []byte) string {
	return oauthpkce.Base64URL(data)
}

// BuildAuthorizationURL builds a Claude OAuth URL with a fixed parameter order.
func BuildAuthorizationURL(state, codeChallenge, scope string) string {
	encodedRedirectURI := url.QueryEscape(RedirectURI)
	encodedScope := strings.ReplaceAll(url.QueryEscape(scope), "%20", "+")

	return fmt.Sprintf("%s?code=true&client_id=%s&response_type=code&redirect_uri=%s&scope=%s&code_challenge=%s&code_challenge_method=S256&state=%s",
		AuthorizeURL,
		ClientID,
		encodedRedirectURI,
		encodedScope,
		codeChallenge,
		state,
	)
}
