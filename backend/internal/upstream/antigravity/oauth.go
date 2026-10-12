package antigravity

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/oauthpkce"
)

const (
	// Google OAuth endpoints.
	AuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	TokenURL     = "https://oauth2.googleapis.com/token"
	UserInfoURL  = "https://www.googleapis.com/oauth2/v2/userinfo"

	// ClientID identifies the Antigravity OAuth client.
	ClientID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"

	// AntigravityOAuthClientSecretEnv names the client-secret environment variable.
	AntigravityOAuthClientSecretEnv = "ANTIGRAVITY_OAUTH_CLIENT_SECRET"

	// AntigravityUserAgentVersionEnv names the User-Agent version environment variable.
	AntigravityUserAgentVersionEnv = "ANTIGRAVITY_USER_AGENT_VERSION"

	// DefaultUserAgentVersion applies when no valid override is configured.
	DefaultUserAgentVersion = "1.23.2"

	// RedirectURI is the loopback callback from which users copy the authorization code.
	RedirectURI = "http://localhost:8085/callback"

	// Scopes lists the permissions requested during authorization.
	Scopes = "https://www.googleapis.com/auth/cloud-platform " +
		"https://www.googleapis.com/auth/userinfo.email " +
		"https://www.googleapis.com/auth/userinfo.profile " +
		"https://www.googleapis.com/auth/cclog " +
		"https://www.googleapis.com/auth/experimentsandconfigs"

	// SessionTTL bounds the authorization session lifetime.
	SessionTTL = 30 * time.Minute

	// URLAvailabilityTTL determines when an unavailable endpoint becomes eligible again.
	URLAvailabilityTTL = 5 * time.Minute

	// Antigravity API endpoints.
	antigravityProdBaseURL  = "https://cloudcode-pa.googleapis.com"
	antigravityDailyBaseURL = "https://daily-cloudcode-pa.googleapis.com"
)

var (
	userAgentVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

	// defaultUserAgentVersion uses a valid environment override when present.
	defaultUserAgentVersion  = DefaultUserAgentVersion
	userAgentVersionMu       sync.RWMutex
	userAgentVersionResolver UserAgentVersionResolver

	// defaultClientSecret accepts an environment override at startup.
	defaultClientSecret = "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"

	// BaseURLs lists API endpoints in account-management request order.
	BaseURLs = []string{
		antigravityProdBaseURL,  // preferred
		antigravityDailyBaseURL, // alternate
	}

	// BaseURL is the default endpoint for NewAPIRequest.
	BaseURL = BaseURLs[0]

	// DefaultURLAvailability shares endpoint availability across requests.
	DefaultURLAvailability = NewURLAvailability(URLAvailabilityTTL)
)

// UserAgentVersionResolver supplies a runtime version override for a request.
type UserAgentVersionResolver func(ctx context.Context) string

// URLAvailability tracks temporary endpoint failures and the last successful endpoint.
type URLAvailability struct {
	mu          sync.RWMutex
	unavailable map[string]time.Time // URL to recovery time
	ttl         time.Duration
	lastSuccess string // preferred when available
}

func init() {
	if version := NormalizeUserAgentVersion(os.Getenv(AntigravityUserAgentVersionEnv)); version != "" {
		defaultUserAgentVersion = version
	}
	if secret := os.Getenv(AntigravityOAuthClientSecretEnv); secret != "" {
		defaultClientSecret = secret
	}
}

// NormalizeUserAgentVersion trims a major.minor.patch version and rejects invalid values.
func NormalizeUserAgentVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || !userAgentVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

// GetDefaultUserAgentVersion returns the startup default, including any environment override.
func GetDefaultUserAgentVersion() string {
	return defaultUserAgentVersion
}

// SetUserAgentVersionResolver installs the runtime version resolver.
func SetUserAgentVersionResolver(resolver UserAgentVersionResolver) {
	userAgentVersionMu.Lock()
	defer userAgentVersionMu.Unlock()
	userAgentVersionResolver = resolver
}

// GetUserAgentVersionForContext resolves a valid runtime version or returns the startup default.
func GetUserAgentVersionForContext(ctx context.Context) string {
	if ctx == nil {
		ctx = context.Background()
	}
	userAgentVersionMu.RLock()
	resolver := userAgentVersionResolver
	userAgentVersionMu.RUnlock()
	if resolver != nil {
		if version := NormalizeUserAgentVersion(resolver(ctx)); version != "" {
			return version
		}
	}
	return defaultUserAgentVersion
}

// BuildUserAgent formats a version, using the startup default for invalid input.
func BuildUserAgent(version string) string {
	if normalized := NormalizeUserAgentVersion(version); normalized != "" {
		return fmt.Sprintf("antigravity/%s windows/amd64", normalized)
	}
	return fmt.Sprintf("antigravity/%s windows/amd64", defaultUserAgentVersion)
}

// GetUserAgentForContext returns the User-Agent for this request.
func GetUserAgentForContext(ctx context.Context) string {
	return BuildUserAgent(GetUserAgentVersionForContext(ctx))
}

func getClientSecret() (string, error) {
	if v := strings.TrimSpace(defaultClientSecret); v != "" {
		return v, nil
	}
	return "", infraerrors.Newf(infraerrors.CategoryBadRequest, "ANTIGRAVITY_OAUTH_CLIENT_SECRET_MISSING", "missing antigravity oauth client_secret; set %s", AntigravityOAuthClientSecretEnv)
}

// ForwardBaseURLs returns a copy of the endpoints with the daily endpoint first.
func ForwardBaseURLs() []string {
	if len(BaseURLs) == 0 {
		return nil
	}
	urls := append([]string(nil), BaseURLs...)
	dailyIndex := -1
	for i, url := range urls {
		if url == antigravityDailyBaseURL {
			dailyIndex = i
			break
		}
	}
	if dailyIndex <= 0 {
		return urls
	}
	reordered := make([]string, 0, len(urls))
	reordered = append(reordered, urls[dailyIndex])
	for i, url := range urls {
		if i == dailyIndex {
			continue
		}
		reordered = append(reordered, url)
	}
	return reordered
}

// NewURLAvailability creates an endpoint tracker with the given failure TTL.
func NewURLAvailability(ttl time.Duration) *URLAvailability {
	return &URLAvailability{
		unavailable: make(map[string]time.Time),
		ttl:         ttl,
	}
}

// MarkUnavailable excludes an endpoint until its failure TTL expires.
func (u *URLAvailability) MarkUnavailable(url string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.unavailable[url] = time.Now().Add(u.ttl)
}

// MarkSuccess restores an endpoint and gives it priority in subsequent selections.
func (u *URLAvailability) MarkSuccess(url string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.lastSuccess = url
	delete(u.unavailable, url)
}

// IsAvailable reports whether the endpoint has no active failure TTL.
func (u *URLAvailability) IsAvailable(url string) bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	expiry, exists := u.unavailable[url]
	if !exists {
		return true
	}
	return time.Now().After(expiry)
}

// GetAvailableURLsWithBase filters the supplied endpoints by availability.
// The last successful endpoint comes first; the others retain the supplied order.
func (u *URLAvailability) GetAvailableURLsWithBase(baseURLs []string) []string {
	u.mu.RLock()
	defer u.mu.RUnlock()

	now := time.Now()
	result := make([]string, 0, len(baseURLs))

	if u.lastSuccess != "" {
		found := slices.Contains(baseURLs, u.lastSuccess)
		if found {
			expiry, exists := u.unavailable[u.lastSuccess]
			if !exists || now.After(expiry) {
				result = append(result, u.lastSuccess)
			}
		}
	}

	for _, url := range baseURLs {
		if url == u.lastSuccess {
			continue
		}
		expiry, exists := u.unavailable[url]
		if !exists || now.After(expiry) {
			result = append(result, url)
		}
	}
	return result
}

func GenerateRandomBytes(n int) ([]byte, error) { return oauthpkce.RandomBytes(n) }

func GenerateState() (string, error) {
	bytes, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return base64URLEncode(bytes), nil
}

func GenerateSessionID() (string, error) {
	bytes, err := GenerateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func GenerateCodeVerifier() (string, error) { return oauthpkce.Verifier() }

func GenerateCodeChallenge(verifier string) string { return oauthpkce.Challenge(verifier) }

func base64URLEncode(data []byte) string { return oauthpkce.Base64URL(data) }

// BuildAuthorizationURL builds the Google consent URL with S256 PKCE.
func BuildAuthorizationURL(state, codeChallenge string) string {
	params := url.Values{}
	params.Set("client_id", ClientID)
	params.Set("redirect_uri", RedirectURI)
	params.Set("response_type", "code")
	params.Set("scope", Scopes)
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")
	params.Set("include_granted_scopes", "true")

	return fmt.Sprintf("%s?%s", AuthorizeURL, params.Encode())
}
