package grok

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	urlvalidator "github.com/TokenFlux/TokenRouter/internal/egress/urlpolicy"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/pkg/oauthpkce"
	wiregrok "github.com/TokenFlux/TokenRouter/internal/protocol/grok"
)

const (
	OAuthIssuer = "https://auth.x.ai"

	DefaultAuthorizeURL   = OAuthIssuer + "/oauth2/authorize"
	DefaultTokenURL       = OAuthIssuer + "/oauth2/token"
	DefaultBaseURL        = "https://api.x.ai/v1"
	DefaultCLIBaseURL     = "https://cli-chat-proxy.grok.com/v1"
	DefaultUSEast1BaseURL = "https://us-east-1.api.x.ai/v1"
	DefaultUSWest2BaseURL = "https://us-west-2.api.x.ai/v1"
	DefaultEUWest1BaseURL = "https://eu-west-1.api.x.ai/v1"
	DefaultClientID       = "b1a00492-073a-47ea-816f-4c329264a828"
	DefaultScope          = "openid profile email offline_access grok-cli:access api:access"
	DefaultRedirectURI    = "http://127.0.0.1:56121/callback"

	EnvAuthorizeURL               = "XAI_OAUTH_AUTHORIZE_URL"
	EnvTokenURL                   = "XAI_OAUTH_TOKEN_URL"
	EnvClientID                   = "XAI_OAUTH_CLIENT_ID"
	EnvScope                      = "XAI_OAUTH_SCOPE"
	EnvRedirectURI                = "XAI_OAUTH_REDIRECT_URI"
	EnvBaseURL                    = "XAI_BASE_URL"
	EnvAllowUnsafeURLOverrides    = "XAI_ALLOW_UNSAFE_URL_OVERRIDES"
	EnvUnsafeAllowHighConcurrency = "XAI_GROK_UNSAFE_ALLOW_CONCURRENCY_GT_ONE"
)

var (
	oauthEndpointAllowedHosts = []string{"x.ai", "*.x.ai"}
	// Regional API hosts allow operators to select another region when an endpoint
	// is unavailable, including us-east-1, us-west-2, and eu-west-1.
	baseURLAllowedHosts = []string{"api.x.ai", "*.api.x.ai", "cli-chat-proxy.grok.com"}
)

// BaseURLValidator applies outbound URL policy before appending an xAI endpoint path.
// API-key providers inject security.url_allowlist policy; OAuth uses trusted-host validation.
type BaseURLValidator func(string) (string, error)

type RuntimeSanityCheck struct {
	Value     string `json:"value"`
	Valid     bool   `json:"valid"`
	Error     string `json:"error,omitempty"`
	IsDefault bool   `json:"is_default,omitempty"`
}

type RuntimeSanityReport struct {
	BaseURL               RuntimeSanityCheck `json:"base_url"`
	OAuthAuthorizeURL     RuntimeSanityCheck `json:"oauth_authorize_url"`
	OAuthTokenURL         RuntimeSanityCheck `json:"oauth_token_url"`
	OAuthRedirectURI      RuntimeSanityCheck `json:"oauth_redirect_uri"`
	UnsafeURLOverrides    bool               `json:"unsafe_url_overrides"`
	UnsafeHighConcurrency bool               `json:"unsafe_high_concurrency"`
	PublicGatewayScope    string             `json:"public_gateway_scope"`
	ProxyPolicy           string             `json:"proxy_policy"`
}

type AuthorizationInput = wiregrok.AuthorizationInput

type TokenResponse = wiregrok.TokenResponse

func EffectiveAuthorizeURL() string {
	return envOrDefault(EnvAuthorizeURL, DefaultAuthorizeURL)
}

func ValidatedAuthorizeURL() (string, error) {
	return ValidateOAuthEndpointURL(EffectiveAuthorizeURL())
}

func EffectiveTokenURL() string {
	return envOrDefault(EnvTokenURL, DefaultTokenURL)
}

func ValidatedTokenURL() (string, error) {
	return ValidateOAuthEndpointURL(EffectiveTokenURL())
}

func EffectiveClientID() string {
	return envOrDefault(EnvClientID, DefaultClientID)
}

func EffectiveScope() string {
	return envOrDefault(EnvScope, DefaultScope)
}

func EffectiveRedirectURI(override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}
	return envOrDefault(EnvRedirectURI, DefaultRedirectURI)
}

func EffectiveBaseURL(override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return strings.TrimRight(trimmed, "/")
	}
	return strings.TrimRight(envOrDefault(EnvBaseURL, DefaultBaseURL), "/")
}

func ValidatedBaseURL(override string) (string, error) {
	return ValidateBaseURL(EffectiveBaseURL(override))
}

func validatedBaseURLWithValidator(override string, validator BaseURLValidator) (string, error) {
	if validator == nil {
		return ValidatedBaseURL(override)
	}
	raw := EffectiveBaseURL(override)
	validated, err := validator(raw)
	if err != nil {
		return "", err
	}
	return normalizeKnownBaseURLPath(validated)
}

func RuntimeSanity() RuntimeSanityReport {
	return RuntimeSanityReport{
		BaseURL: runtimeSanityCheck(EffectiveBaseURL(""), EnvBaseURL, ValidatedBaseURL),

		OAuthAuthorizeURL: runtimeSanityCheck(EffectiveAuthorizeURL(), EnvAuthorizeURL, func(string) (string, error) {
			return ValidatedAuthorizeURL()
		}),

		OAuthTokenURL: runtimeSanityCheck(EffectiveTokenURL(), EnvTokenURL, func(string) (string, error) {
			return ValidatedTokenURL()
		}),

		OAuthRedirectURI: runtimeSanityCheck(EffectiveRedirectURI(""), EnvRedirectURI, validateRedirectURI),

		UnsafeURLOverrides: AllowUnsafeURLOverrides(),

		UnsafeHighConcurrency: AllowUnsafeHighConcurrency(),

		PublicGatewayScope: "responses_only",

		ProxyPolicy: "provider_proxy_optional; OAuth URLs use trusted-host allowlists; API-key base URLs require public HTTPS unless unsafe overrides are enabled",
	}
}

func runtimeSanityCheck(value string, envKey string, validate func(string) (string, error)) RuntimeSanityCheck {
	normalized, err := validate(value)
	check := RuntimeSanityCheck{
		Value:     sanitizeRuntimeURLValue(normalized),
		Valid:     err == nil,
		IsDefault: strings.TrimSpace(os.Getenv(envKey)) == "",
	}
	if err != nil {
		check.Value = sanitizeRuntimeURLValue(value)
		check.Error = sanitizeRuntimeError(err.Error(), value)
	}
	return check
}

func validateRedirectURI(raw string) (string, error) {
	return urlvalidator.ValidateURLFormat(raw, true)
}

func sanitizeRuntimeURLValue(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return trimmed
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func sanitizeRuntimeError(rawErr string, rawValue string) string {
	redacted := logredact.RedactText(rawErr)
	trimmedValue := strings.TrimSpace(rawValue)
	if trimmedValue == "" {
		return redacted
	}
	sanitizedValue := sanitizeRuntimeURLValue(trimmedValue)
	redacted = strings.ReplaceAll(redacted, trimmedValue, sanitizedValue)
	redacted = strings.ReplaceAll(redacted, logredact.RedactText(trimmedValue), sanitizedValue)
	return redacted
}

func ValidateOAuthEndpointURL(raw string) (string, error) {
	if AllowUnsafeURLOverrides() {
		return urlvalidator.ValidateURLFormat(raw, true)
	}
	return urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     oauthEndpointAllowedHosts,
		RequireAllowlist: true,
		AllowPrivate:     false,
	})
}

func ValidateBaseURL(raw string) (string, error) {
	if AllowUnsafeURLOverrides() {
		return urlvalidator.ValidateURLFormat(raw, true)
	}
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowPrivate: false,
	})
	if err != nil {
		return "", err
	}
	return normalizeKnownBaseURLPath(normalized)
}

func ValidateTrustedBaseURL(raw string) (string, error) {
	if AllowUnsafeURLOverrides() {
		return urlvalidator.ValidateURLFormat(raw, true)
	}
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     baseURLAllowedHosts,
		RequireAllowlist: true,
		AllowPrivate:     false,
	})
	if err != nil {
		return "", err
	}
	return normalizeKnownBaseURLPath(normalized)
}

// normalizeKnownBaseURLPath normalizes the base URL path:
//   - Official hosts require /v1; an empty path becomes /v1 and other paths fail.
//   - Custom hosts retain configured prefixes such as /xxx/v1.
//     An empty custom-host path also becomes /v1.
//
// All hosts reject userinfo, query, and fragment components and trim trailing slashes.
func normalizeKnownBaseURLPath(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid base URL")
	}
	if parsed.User != nil {
		return "", errors.New("base URL must not include userinfo")
	}
	if parsed.ForceQuery || parsed.RawQuery != "" {
		return "", errors.New("base URL must not include a query")
	}
	if parsed.Fragment != "" {
		return "", errors.New("base URL must not include a fragment")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path == "" {
		parsed.Path = "/v1"
		parsed.RawPath = ""
		return strings.TrimRight(parsed.String(), "/"), nil
	}
	if path != "/v1" && IsOfficialBaseURLHost(parsed.Hostname()) {
		return "", fmt.Errorf("base URL path must be /v1")
	}
	parsed.Path = path
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// IsOfficialBaseURLHost reports whether host is an xAI API, regional API, or CLI host.
func IsOfficialBaseURLHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, allowed := range baseURLAllowedHosts {
		if after, ok := strings.CutPrefix(allowed, "*."); ok {
			suffix := after
			if host == suffix || strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == allowed {
			return true
		}
	}
	return false
}

// IsOfficialBaseURL recognizes official hosts, including case variants, explicit
// ports, and encoded paths found in stored credentials.
// Empty or unparseable URLs return true so callers select the default endpoint.
func IsOfficialBaseURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return true
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return true
	}
	return IsOfficialBaseURLHost(parsed.Hostname())
}

func AllowUnsafeURLOverrides() bool {
	return envBool(EnvAllowUnsafeURLOverrides)
}

func AllowUnsafeHighConcurrency() bool {
	return envBool(EnvUnsafeAllowHighConcurrency)
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func GenerateRandomBytes(n int) ([]byte, error) {
	return oauthpkce.RandomBytes(n)
}

func GenerateState() (string, error) {
	bytes, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func GenerateNonce() (string, error) {
	bytes, err := GenerateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func GenerateSessionID() (string, error) {
	bytes, err := GenerateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func GenerateCodeVerifier() (string, error) {
	return oauthpkce.Verifier()
}

func GenerateCodeChallenge(verifier string) string {
	return oauthpkce.Challenge(verifier)
}

func BuildAuthorizationURL(state, codeChallenge, redirectURI, nonce string) (string, error) {
	redirectURI = EffectiveRedirectURI(redirectURI)
	authorizeURL, err := ValidatedAuthorizeURL()
	if err != nil {
		return "", fmt.Errorf("invalid authorize url: %w", err)
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", EffectiveClientID())
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", EffectiveScope())
	params.Set("state", state)
	params.Set("nonce", nonce)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("plan", "generic")
	params.Set("referrer", "tokenrouter")

	return fmt.Sprintf("%s?%s", authorizeURL, params.Encode()), nil
}

// ParseAuthorizationInput accepts a callback URL, query string, or authorization code.
func ParseAuthorizationInput(raw string) AuthorizationInput {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return AuthorizationInput{}
	}

	if parsed, err := url.Parse(trimmed); err == nil && parsed != nil {
		values := parsed.Query()
		if code := strings.TrimSpace(values.Get("code")); code != "" {
			return AuthorizationInput{
				Code:          code,
				State:         strings.TrimSpace(values.Get("state")),
				RequiresState: true,
			}
		}
	}

	queryCandidate := strings.TrimPrefix(trimmed, "?")
	if strings.Contains(queryCandidate, "=") {
		if values, err := url.ParseQuery(queryCandidate); err == nil {
			if code := strings.TrimSpace(values.Get("code")); code != "" {
				return AuthorizationInput{
					Code:          code,
					State:         strings.TrimSpace(values.Get("state")),
					RequiresState: true,
				}
			}
		}
	}

	return AuthorizationInput{Code: trimmed}
}

func BuildResponsesURLWithValidator(baseURL string, validator BaseURLValidator) (string, error) {
	validatedBaseURL, err := validatedBaseURLWithValidator(baseURL, validator)
	if err != nil {
		return "", fmt.Errorf("invalid base url: %w", err)
	}
	return validatedBaseURL + "/responses", nil
}

func BuildChatCompletionsURLWithValidator(baseURL string, validator BaseURLValidator) (string, error) {
	validatedBaseURL, err := validatedBaseURLWithValidator(baseURL, validator)
	if err != nil {
		return "", fmt.Errorf("invalid base url: %w", err)
	}
	return validatedBaseURL + "/chat/completions", nil
}

func BuildImagesGenerationsURLWithValidator(baseURL string, validator BaseURLValidator) (string, error) {
	validatedBaseURL, err := validatedBaseURLWithValidator(baseURL, validator)
	if err != nil {
		return "", fmt.Errorf("invalid base url: %w", err)
	}
	return validatedBaseURL + "/images/generations", nil
}

func BuildImagesEditsURLWithValidator(baseURL string, validator BaseURLValidator) (string, error) {
	validatedBaseURL, err := validatedBaseURLWithValidator(baseURL, validator)
	if err != nil {
		return "", fmt.Errorf("invalid base url: %w", err)
	}
	return validatedBaseURL + "/images/edits", nil
}

func BuildVideosGenerationsURLWithValidator(baseURL string, validator BaseURLValidator) (string, error) {
	validatedBaseURL, err := validatedBaseURLWithValidator(baseURL, validator)
	if err != nil {
		return "", fmt.Errorf("invalid base url: %w", err)
	}
	return validatedBaseURL + "/videos/generations", nil
}

func BuildVideosEditsURLWithValidator(baseURL string, validator BaseURLValidator) (string, error) {
	validatedBaseURL, err := validatedBaseURLWithValidator(baseURL, validator)
	if err != nil {
		return "", fmt.Errorf("invalid base url: %w", err)
	}
	return validatedBaseURL + "/videos/edits", nil
}

func BuildVideosExtensionsURLWithValidator(baseURL string, validator BaseURLValidator) (string, error) {
	validatedBaseURL, err := validatedBaseURLWithValidator(baseURL, validator)
	if err != nil {
		return "", fmt.Errorf("invalid base url: %w", err)
	}
	return validatedBaseURL + "/videos/extensions", nil
}

func BuildVideoURLWithValidator(baseURL, requestID string, validator BaseURLValidator) (string, error) {
	validatedBaseURL, err := validatedBaseURLWithValidator(baseURL, validator)
	if err != nil {
		return "", fmt.Errorf("invalid base url: %w", err)
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "", fmt.Errorf("request id is required")
	}
	// requestID becomes one URL path segment. Reject dot segments and NUL/CR/LF
	// in addition to escaping the value so it cannot redirect path resolution.
	if requestID == "." || requestID == ".." || strings.ContainsAny(requestID, "\x00\r\n") {
		return "", fmt.Errorf("invalid request id")
	}
	return validatedBaseURL + "/videos/" + url.PathEscape(requestID), nil
}
