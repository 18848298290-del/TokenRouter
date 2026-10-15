package oauth

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/oauthpkce"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// oauth.go builds Claude authorization URLs and generates PKCE and session
// values.
// Claude authorization uses separate browser, token exchange, and callback
// endpoints with PKCE S256 proof. Random state, verifier, and session strings
// supply URL-safe inputs, while the selected scope determines which permissions
// the authorization URL asks the user to grant.

// OAuth client, endpoint, and scope values supply the configured proof exchange
// and browser authorization URL built for Claude accounts.
const (
	// ClientID identifies the Claude OAuth client.
	ClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// OAuth endpoints.
	AuthorizeURL = "https://claude.com/cai/oauth/authorize"
	TokenURL     = "https://platform.claude.com/v1/oauth/token"
	RedirectURI  = "https://platform.claude.com/oauth/code/callback"

	// ScopeOAuth includes API key creation for browser authorization.
	ScopeOAuth = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	// ScopeAPI requests profile, inference, session, MCP, and upload permissions
	// while omitting the org:create_api_key permission present in ScopeOAuth.
	ScopeAPI = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	// ScopeInference limits setup tokens to inference.
	ScopeInference = "user:inference"
)

// Protocol aliases expose the token response and its optional organization and
// account information to authorization and refresh callers.
type (
	// TokenResponse aliases credential and identity data decoded from token
	// exchange.
	TokenResponse = wire.OAuthTokenResponse
	// OrgInfo aliases organization metadata returned with a Claude token response.
	OrgInfo = wire.OAuthOrgInfo
	// ProviderInfo aliases account metadata returned with a Claude token response.
	ProviderInfo = wire.OAuthProviderInfo
)

// GenerateRandomBytes delegates allocation and cryptographic filling of n
// bytes. Zero returns an empty slice, and negative n panics during allocation.
// The configured crypto/rand.Read fills the buffer or terminates the process,
// so its error return does not provide entropy-failure recovery.
func GenerateRandomBytes(n int) ([]byte, error) {
	return oauthpkce.RandomBytes(n)
}

// GenerateState returns unpadded base64url text from thirty-two random bytes
// for OAuth state. BuildAuthorizationURL inserts this URL-safe value directly.
// The configured random API terminates on entropy failure before this helper
// can return an error.
func GenerateState() (string, error) {
	bytes, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return base64URLEncode(bytes), nil
}

// GenerateSessionID returns thirty-two lowercase hexadecimal characters from
// sixteen random bytes. Authorization callers use it to identify stored
// sessions. Entropy failure terminates through the configured random API before
// an error can be returned.
func GenerateSessionID() (string, error) {
	bytes, err := GenerateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GenerateCodeVerifier delegates generation of a forty-three-character unpadded
// base64url PKCE verifier from thirty-two random bytes. The delegated helper uses
// the configured random API, whose entropy failure terminates the process
// before returning an error.
func GenerateCodeVerifier() (string, error) {
	return oauthpkce.Verifier()
}

// GenerateCodeChallenge returns the verifier's unpadded base64url SHA-256 PKCE
// digest.
func GenerateCodeChallenge(verifier string) string {
	return oauthpkce.Challenge(verifier)
}

// base64URLEncode returns data encoded as unpadded URL-safe base64 text.
func base64URLEncode(data []byte) string {
	return oauthpkce.Base64URL(data)
}

// BuildAuthorizationURL constructs the browser authorization URL with fixed
// parameter order. Scope and callback are query-escaped; state and
// codeChallenge are inserted directly, so callers supply URL-safe values such
// as the generated state and PKCE challenge.
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
