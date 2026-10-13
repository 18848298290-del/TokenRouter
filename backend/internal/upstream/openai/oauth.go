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

// OpenAI OAuth authorization sends a PKCE challenge with Codex client defaults
// and optional redirect customization. State and session identifiers are random
// hex strings, while ID-token payload readers extract metadata from JWT content
// supplied by the importing caller.
//
// JWT decoding accepts URL-safe or standard Base64 with repaired padding.
// DecodeIDToken intentionally permits expired metadata, and ParseIDToken adds
// an expiration check with clock tolerance when exp is positive. Neither reader
// validates a signature, so authorization decisions need a separate
// authenticated token-validation path. The platform argument currently selects
// the same Codex client and simplified flow for every value.

// OAuth constants select the Codex client, authorization/token endpoints, local
// callback default, and distinct scope sets for authorization and refresh exchanges.
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

// TokenResponse names the protocol value returned by an OAuth token exchange.
type TokenResponse = wire.OAuthTokenResponse

// IDTokenClaims names decoded protocol metadata carried by OpenAI ID tokens.
type IDTokenClaims = wire.OAuthIDTokenClaims

// GenerateRandomBytes returns n bytes from the shared cryptographic random
// source.
func GenerateRandomBytes(n int) ([]byte, error) {
	return oauthpkce.RandomBytes(n)
}

// GenerateState returns a random thirty-two-byte OAuth state encoded as
// hexadecimal.
func GenerateState() (string, error) {
	bytes, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GenerateSessionID returns a random sixteen-byte authorization session encoded
// as hexadecimal.
func GenerateSessionID() (string, error) {
	bytes, err := GenerateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GenerateCodeVerifier returns the shared hex-encoded verifier for the OpenAI
// PKCE flow.
func GenerateCodeVerifier() (string, error) {
	return oauthpkce.HexVerifier()
}

// GenerateCodeChallenge derives the raw URL-safe Base64 S256 challenge from
// verifier.
func GenerateCodeChallenge(verifier string) string {
	return oauthpkce.Challenge(verifier)
}

// BuildAuthorizationURLForPlatform encodes a PKCE authorization URL with state
// and challenge. An empty redirect uses DefaultRedirectURI; platform currently
// selects the same client ID and simplified Codex flow for every supplied
// value.
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
	// Ask the authorization response to include organization claims for provider
	// metadata; Codex client selection also requests its simplified CLI login flow.
	params.Set("id_token_add_organizations", "true")
	if codexFlow {
		params.Set("codex_cli_simplified_flow", "true")
	}

	return fmt.Sprintf("%s?%s", AuthorizeURL, params.Encode())
}

// OAuthClientConfigByPlatform returns the Codex client ID and enabled
// simplified-flow flag.
func OAuthClientConfigByPlatform(platform string) (clientID string, codexFlow bool) {
	return ClientID, true
}

// DecodeIDToken extracts claims from a three-part JWT with repaired payload
// padding. It accepts URL-safe or standard Base64 and checks JSON decoding;
// callers use the result for metadata because signature and expiration
// validation are separate.
func DecodeIDToken(idToken string) (*IDTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	// JWT metadata comes from the middle segment; this extraction does not
	// establish that the supplied header or signature authenticates those claims.
	payload := parts[1]
	// JWT payloads commonly omit Base64 padding; repair its length before using
	// padded decoders so the metadata reader accepts both supported encoding spellings.
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		// Import metadata can use standard Base64 spelling. Retry that decoder after
		// URL-safe decoding fails while retaining JSON validation for the decoded payload.
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

// ParseIDToken decodes metadata and checks positive exp against a two-minute
// clock tolerance. Missing or nonpositive expiry is accepted, and signature
// validation remains the responsibility of an authenticated authorization path.
func ParseIDToken(idToken string) (*IDTokenClaims, error) {
	claims, err := DecodeIDToken(idToken)
	if err != nil {
		return nil, err
	}

	// Account for two minutes of clock skew when checking expiry.
	const clockSkewTolerance = 120 // seconds
	now := time.Now().Unix()
	if claims.Exp > 0 && now > claims.Exp+clockSkewTolerance {
		return nil, fmt.Errorf("id_token has expired (exp: %d, now: %d, skew_tolerance: %ds)", claims.Exp, now, clockSkewTolerance)
	}

	return claims, nil
}
