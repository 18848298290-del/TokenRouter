package openai

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/oauthpkce"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

const (
	// ClientID identifies the official Codex CLI OAuth client.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// OAuth endpoints.
	AuthorizeURL = "https://auth.openai.com/oauth/authorize"
	TokenURL     = "https://auth.openai.com/oauth/token"

	// Default redirect URI (can be customized).
	DefaultRedirectURI = "http://localhost:1455/auth/callback"

	// Scopes.
	DefaultScopes = "openid profile email offline_access"
	// RefreshScopes contains the scopes sent on token refresh; offline_access is omitted.
	RefreshScopes = "openid profile email"

	// OAuthPlatformOpenAI uses OpenAI Codex-compatible OAuth client.
	OAuthPlatformOpenAI = "openai"
)

type TokenResponse = wire.OAuthTokenResponse

type IDTokenClaims = wire.OAuthIDTokenClaims

// GenerateRandomBytes generates cryptographically secure random bytes.
func GenerateRandomBytes(n int) ([]byte, error) {
	return oauthpkce.RandomBytes(n)
}

// GenerateState generates a random state string for OAuth.
func GenerateState() (string, error) {
	bytes, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GenerateSessionID generates a unique session ID.
func GenerateSessionID() (string, error) {
	bytes, err := GenerateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GenerateCodeVerifier returns 64 random bytes encoded as hex for the OpenAI PKCE flow.
func GenerateCodeVerifier() (string, error) {
	return oauthpkce.HexVerifier()
}

// GenerateCodeChallenge returns the RFC 7636 S256 challenge using raw URL-safe Base64.
func GenerateCodeChallenge(verifier string) string {
	return oauthpkce.Challenge(verifier)
}

// BuildAuthorizationURLForPlatform builds authorization URL by platform.
func BuildAuthorizationURLForPlatform(state, codeChallenge, redirectURI, platform string) string {
	if redirectURI == "" {
		redirectURI = DefaultRedirectURI
	}

	clientID, codexFlow := OAuthClientConfigByPlatform(platform)

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", DefaultScopes)
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	// OpenAI specific parameters
	params.Set("id_token_add_organizations", "true")
	if codexFlow {
		params.Set("codex_cli_simplified_flow", "true")
	}

	return fmt.Sprintf("%s?%s", AuthorizeURL, params.Encode())
}

// OAuthClientConfigByPlatform returns oauth client_id and whether codex simplified flow should be enabled.
func OAuthClientConfigByPlatform(platform string) (clientID string, codexFlow bool) {
	return ClientID, true
}

// DecodeIDToken decodes the JWT payload without checking its signature or expiration.
// Use it for metadata extraction, including expired tokens during import; it does not authenticate a token.
func DecodeIDToken(idToken string) (*IDTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	// Decode payload (second part)
	payload := parts[1]
	// Add padding if necessary
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		// Try standard encoding
		decoded, err = base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
		}
	}

	var claims IDTokenClaims
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	return &claims, nil
}

// ParseIDToken decodes claims and checks exp with a two-minute clock tolerance.
// It does not verify the JWT signature. Authorization decisions require signature
// verification against https://auth.openai.com/.well-known/jwks.json.
func ParseIDToken(idToken string) (*IDTokenClaims, error) {
	claims, err := DecodeIDToken(idToken)
	if err != nil {
		return nil, err
	}

	// Allow two minutes of clock skew when checking expiration.
	const clockSkewTolerance = 120 // seconds
	now := time.Now().Unix()
	if claims.Exp > 0 && now > claims.Exp+clockSkewTolerance {
		return nil, fmt.Errorf("id_token has expired (exp: %d, now: %d, skew_tolerance: %ds)", claims.Exp, now, clockSkewTolerance)
	}

	return claims, nil
}
