package grok

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A web SSO token becomes a Build OAuth grant through a device authorization
// sequence. The flow keeps SSO, session, and CSRF cookies in a private jar
// while validating each exposed redirect before the next request. Device and
// user codes retain different roles: the user code verifies and approves the
// grant, while the device code retrieves its tokens.
//
// Redirect handling can change a POST form to GET for 303 or eligible 301/302
// responses, while other redirect statuses retain method and form. Response
// bodies are bounded and closed before continuing. Token polling sleeps
// before attempts and responds to pending and slow_down states. The smaller
// advertised expiry and local budget are checked before each waiting
// iteration, so an admitted wait can issue a request after expiry. Callers provide
// overall cancellation and redirect-suppressed injected clients, because
// request timeout and polling-loop deadlines alone do not bound the entire
// authorization flow.

// Device conversion uses fixed grant endpoints, bounded response/token sizes,
// a browser identity, and local timeout/expiry defaults for its request and polling phases.
const (
	// SSOBuildScope requests the identity, API, and conversation capabilities sent
	// with device authorization, while the adjacent URLs identify each grant phase.
	SSOBuildScope        = "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write" // device authorization scope text
	SSOAccountsURL       = "https://accounts.x.ai/"                                                                                // web session validation URL
	SSODeviceURL         = OAuthIssuer + "/oauth2/device/code"                                                                     // device grant creation URL
	SSOVerifyURL         = OAuthIssuer + "/oauth2/device/verify"                                                                   // device code verification URL
	SSOApproveURL        = OAuthIssuer + "/oauth2/device/approve"                                                                  // device grant approval URL
	SSOTokenURL          = OAuthIssuer + "/oauth2/token"                                                                           // device token exchange URL
	SSOConversionTimeout = 90 * time.Second                                                                                        // per-request default timeout duration

	ssoMaxAuthBody     = 2 << 20                                                                                                                 // maximum authorization response bytes
	ssoMaxTokenLength  = 16 << 10                                                                                                                // maximum normalized token bytes
	ssoDefaultUA       = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36" // default device browser identity
	ssoDefaultTokenTTL = 6 * time.Hour                                                                                                           // default issued token lifetime
)

// These sentinels separate invalid web sessions from denied or expired device
// grants so application callers can assign different authorization error categories.
var (
	ErrSSOUnauthorized        = errors.New("xai sso unauthorized")
	ErrSSOAuthorizationDenied = errors.New("xai device authorization denied")
)

// SSOHTTPError retains an upstream status for application-level SSO error
// classification.
type SSOHTTPError struct {
	Status int /* observed upstream HTTP status */
}

// SSODeviceHTTPClient performs device-flow exchanges with redirect responses
// exposed to the flow.
type SSODeviceHTTPClient interface {
	// Do returns an exchange response with automatic redirects disabled.
	// The flow closes its body and follows each Location itself; a nil-error response
	// needs a readable body, and the transport honors the request context.
	Do(*http.Request) (*http.Response, error)
}

// SSODeviceOptions supplies optional transport, identity, and waiting hooks
// for one SSO conversion.
type SSODeviceOptions struct {
	// HTTPClient defaults to a timeout client exposing redirects. Injected
	// clients must also expose them so manual redirect validation can protect
	// subsequent requests.
	HTTPClient SSODeviceHTTPClient // optional device flow transport
	UserAgent  string              // optional device browser identity
	// Sleep defaults to sleepContext. Injected waits control polling and need to
	// honor cancellation so the caller's context can stop a pending device
	// conversion.
	Sleep func(context.Context, time.Duration) error // optional context-aware wait callback
}

// ssoDeviceFlow owns one conversion's transport hooks and private scoped
// cookie state.
type ssoDeviceFlow struct {
	client    SSODeviceHTTPClient // device exchange transport client
	userAgent string              // device browser identity text
	// cookieJar belongs to one ConvertSSOToBuild invocation. It is seeded for
	// account and issuer hosts and receives response cookies before each
	// manually followed redirect.
	cookieJar http.CookieJar                             // private device cookie storage
	sleep     func(context.Context, time.Duration) error // device polling wait callback
}

// Error formats the retained upstream HTTP status as an SSO diagnostic
// message.
func (e SSOHTTPError) Error() string { return fmt.Sprintf("xAI OAuth HTTP %d", e.Status) }

// ConvertSSOToBuild normalizes SSO input and constructs a private device-flow
// cookie jar. Blank or oversized normalized tokens return ErrSSOUnauthorized.
// Nil options select a default redirect-suppressed client, browser identity,
// and context-aware sleep; supplied clients must expose redirect responses
// for the flow's own validation.
//
// The default client has a per-request SSOConversionTimeout, while callers
// provide an overall bounded ctx for the complete sequence. Each conversion
// seeds both account and issuer cookies in its own jar, keeping its cookie
// changes separate from other conversions. Successful returned token fields
// remain exchange data that the caller validates and persists as appropriate.
func ConvertSSOToBuild(ctx context.Context, ssoToken string, opts *SSODeviceOptions) (*TokenResponse, error) {
	ssoToken = NormalizeSSOToken(ssoToken)
	if ssoToken == "" {
		return nil, ErrSSOUnauthorized
	}
	if opts == nil {
		opts = &SSODeviceOptions{}
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: SSOConversionTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	userAgent := strings.TrimSpace(opts.UserAgent)
	if userAgent == "" {
		userAgent = ssoDefaultUA
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	seedSSOCookies(jar, ssoToken)

	flow := &ssoDeviceFlow{
		client:    client,
		userAgent: userAgent,
		cookieJar: jar,
		sleep:     sleep,
	}
	return flow.convert(ctx)
}

// convert validates the web session, creates a device grant, visits its
// verification URL, and verifies then approves the user code before polling
// with the device code. Unauthorized responses or sign-in/sign-up
// destinations reject the SSO session; verify and approval must reach their
// expected final URL markers.
//
// Returned codes and the verification destination are checked before further
// requests. Missing positive poll interval/expiry use local defaults, while
// the request helper owns cookie capture and redirect checks. The sequence
// retains its user code through approval and its distinct device code through
// token polling, preventing these grant identifiers from being interchanged.
func (f *ssoDeviceFlow) convert(ctx context.Context) (*TokenResponse, error) {
	status, finalURL, _, err := f.do(ctx, http.MethodGet, SSOAccountsURL, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized || strings.Contains(finalURL, "sign-in") || strings.Contains(finalURL, "sign-up") {
		return nil, ErrSSOUnauthorized
	}
	if status < 200 || status >= 400 {
		return nil, fmt.Errorf("validate Grok Web SSO: %w", SSOHTTPError{Status: status})
	}

	status, _, body, err := f.do(ctx, http.MethodPost, SSODeviceURL, url.Values{
		"client_id": {DefaultClientID},
		"scope":     {SSOBuildScope},
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("start xAI device flow: %w", SSOHTTPError{Status: status})
	}
	var device struct {
		// DeviceCode and UserCode come from the same device response but serve
		// separate exchanges. UserCode verifies and approves the grant; DeviceCode
		// later retrieves its OAuth tokens, and both are checked before the
		// sequence proceeds.
		DeviceCode string `json:"device_code"` // token polling grant identifier
		UserCode   string `json:"user_code"`   // device approval user code
		// VerificationURIComplete must pass safeXAIAuthURL before the flow visits
		// it. The request helper checks each later redirect destination under the
		// same policy.
		VerificationURIComplete string `json:"verification_uri_complete"` // device verification destination URL
		// Interval and ExpiresIn control token polling in seconds after local
		// defaults replace nonpositive values. pollToken additionally applies its
		// one-second minimum wait and seventy-five-second admission cap.
		Interval  int `json:"interval"`   // seconds
		ExpiresIn int `json:"expires_in"` // seconds
	}
	if err := json.Unmarshal(body, &device); err != nil {
		return nil, fmt.Errorf("parse xAI device flow response: %w", err)
	}
	if device.DeviceCode == "" || device.UserCode == "" || !safeXAIAuthURL(device.VerificationURIComplete) {
		return nil, errors.New("xAI device flow response is incomplete")
	}
	if device.Interval <= 0 {
		device.Interval = 5
	}
	if device.ExpiresIn <= 0 {
		device.ExpiresIn = 1800
	}

	status, _, _, err = f.do(ctx, http.MethodGet, device.VerificationURIComplete, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 400 {
		return nil, fmt.Errorf("open xAI device verification page: %w", SSOHTTPError{Status: status})
	}

	status, finalURL, _, err = f.do(ctx, http.MethodPost, SSOVerifyURL, url.Values{"user_code": {device.UserCode}})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 400 {
		return nil, fmt.Errorf("verify xAI device code: %w", SSOHTTPError{Status: status})
	}
	if !strings.Contains(finalURL, "consent") {
		return nil, errors.New("xAI device verification did not reach consent page")
	}

	status, finalURL, _, err = f.do(ctx, http.MethodPost, SSOApproveURL, url.Values{
		"user_code":      {device.UserCode},
		"action":         {"allow"},
		"principal_type": {"User"},
		"principal_id":   {""},
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 400 {
		return nil, fmt.Errorf("approve xAI device code: %w", SSOHTTPError{Status: status})
	}
	if !strings.Contains(finalURL, "done") {
		return nil, errors.New("xAI device approval did not reach done page")
	}

	return f.pollToken(ctx, device.DeviceCode, time.Duration(device.Interval)*time.Second, time.Duration(device.ExpiresIn)*time.Second)
}

// pollToken waits before exchanging the device code until tokens arrive or
// polling ends. Intervals below one second are raised to one second. The
// loop tests its deadline before each wait, using the smaller positive
// advertised expiry and seventy-five seconds; an admitted wait can start a
// request after that deadline. Supply a sleeper that honors ctx cancellation.
//
// Pending responses continue and slow_down adds five seconds to later waits.
// Denial or expiry returns ErrSSOAuthorizationDenied; other failures retain
// typed HTTP context. Success needs a nonempty access token and supplies
// local defaults for nonpositive lifetime or empty token type. ctx and
// transport bound an in-flight exchange, while loop exhaustion returns an
// ordinary polling-timeout error.
func (f *ssoDeviceFlow) pollToken(ctx context.Context, deviceCode string, interval, expiresIn time.Duration) (*TokenResponse, error) {
	// sleepContext uses this minimum to space request starts. Injected sleeps
	// still control whether the interval delays the next token request.
	if interval < time.Second {
		interval = time.Second
	}
	// Check expiry before starting each wait. A previously admitted wait still
	// proceeds to its exchange, whose cancellation remains with ctx and transport.
	deadline := time.Now().Add(minDuration(expiresIn, 75*time.Second))
	for time.Now().Before(deadline) {
		if err := f.sleep(ctx, interval); err != nil {
			return nil, err
		}
		status, _, body, err := f.do(ctx, http.MethodPost, SSOTokenURL, url.Values{
			"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},

			"client_id": {DefaultClientID},

			"device_code": {deviceCode},
		})
		if err != nil {
			return nil, err
		}
		var payload struct {
			// AccessToken is required for polling success. An empty token enters error
			// handling even when the HTTP status is successful, so no incomplete grant is returned.
			AccessToken  string `json:"access_token"`  // returned OAuth access token
			RefreshToken string `json:"refresh_token"` // returned OAuth refresh token
			IDToken      string `json:"id_token"`      // returned OAuth identity token
			// TokenType is retained in the converted grant. A successful response with
			// empty type receives Bearer before its token fields are returned to the caller.
			TokenType string `json:"token_type"` // returned token authorization type
			// ExpiresIn reports the token lifetime in seconds. A successful grant with
			// a nonpositive value receives ssoDefaultTokenTTL before callers store credential expiry.
			ExpiresIn        int64  `json:"expires_in"`        // seconds
			Scope            string `json:"scope"`             // returned OAuth scope text
			Error            string `json:"error"`             // token polling error code
			ErrorDescription string `json:"error_description"` // token polling error description
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("parse xAI token response: %w", err)
		}
		if status >= 200 && status < 300 && payload.AccessToken != "" {
			// A successful access-token response can omit expiry or type. Supply local
			// credential metadata defaults so conversion callers receive usable
			// lifetime and authorization fields.
			if payload.ExpiresIn <= 0 {
				payload.ExpiresIn = int64(ssoDefaultTokenTTL.Seconds())
			}
			if payload.TokenType == "" {
				payload.TokenType = "Bearer"
			}
			return &TokenResponse{
				AccessToken: payload.AccessToken,

				RefreshToken: payload.RefreshToken,

				IDToken: payload.IDToken,

				TokenType: payload.TokenType,

				ExpiresIn: payload.ExpiresIn,

				Scope: payload.Scope,
			}, nil
		}
		switch payload.Error {
		case "authorization_pending":
			// Pending is a grant state rather than terminal failure. Wait again using
			// the current interval so later approval or token readiness can finish this flow.
			continue
		case "slow_down":
			// The polling response asks for less frequent exchanges. Increase later
			// waits by five seconds while retaining the same device code and admission deadline.
			interval += 5 * time.Second
			continue
		case "access_denied", "expired_token":
			return nil, ErrSSOAuthorizationDenied
		default:
			if status >= 400 {
				return nil, fmt.Errorf("xAI token polling failed (%s): %w", firstNonEmpty(payload.ErrorDescription, payload.Error), SSOHTTPError{Status: status})
			}
			return nil, fmt.Errorf("xAI token polling failed: %s", firstNonEmpty(payload.ErrorDescription, payload.Error, strconv.Itoa(status)))
		}
	}
	return nil, errors.New("xAI device flow token polling timed out")
}

// do exchanges one trusted endpoint while manually following up to nine
// responses through the flow's cookie jar. Callers supply a client that does
// not follow redirects itself, so every next URL remains visible for
// validation. Each response captures cookies, reads at most the configured
// body cap plus one byte, and closes its body before continuation or return.
//
// A redirect needs Location and a trusted resolved destination. HTTP 303 and
// non-GET/HEAD 301/302 switch to GET and discard the form; other redirects
// keep method and form. The flow therefore retains grant data only for
// redirects whose method rule permits it. Missing locations, untrusted
// targets, excessive redirects, oversized bodies, and read failures end the
// exchange.
func (f *ssoDeviceFlow) do(ctx context.Context, method, endpoint string, form url.Values) (int, string, []byte, error) {
	if !safeXAIAuthURL(endpoint) {
		return 0, "", nil, errors.New("xAI OAuth URL is not trusted")
	}
	currentURL := endpoint
	currentMethod := method
	currentForm := form
	// Handle each redirect here so the next destination is checked before the
	// transport sends grant data or jar cookies; automatic client following must be disabled.
	for range 9 {
		var body io.Reader
		if currentForm != nil {
			body = strings.NewReader(currentForm.Encode())
		}
		request, err := http.NewRequestWithContext(ctx, currentMethod, currentURL, body)
		if err != nil {
			return 0, currentURL, nil, err
		}
		request.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.8")
		request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
		request.Header.Set("User-Agent", f.userAgent)
		if cookie := f.cookieHeader(request.URL); cookie != "" {
			request.Header.Set("Cookie", cookie)
		}
		if currentForm != nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}

		response, err := f.client.Do(request)
		if err != nil {
			return 0, currentURL, nil, err
		}
		f.captureCookies(request.URL, response)
		// Read one byte beyond the response cap so oversized authorization pages are
		// distinguishable from a complete bounded body before this response is closed.
		data, readErr := io.ReadAll(io.LimitReader(response.Body, ssoMaxAuthBody+1))
		_ = response.Body.Close()
		if readErr != nil {
			return response.StatusCode, currentURL, nil, readErr
		}
		if len(data) > ssoMaxAuthBody {
			return response.StatusCode, currentURL, nil, errors.New("xAI OAuth response exceeds 2 MiB")
		}
		if response.StatusCode < 300 || response.StatusCode > 399 {
			return response.StatusCode, currentURL, data, nil
		}

		location := strings.TrimSpace(response.Header.Get("Location"))
		if location == "" {
			return response.StatusCode, currentURL, data, errors.New("xAI OAuth redirect missing Location")
		}
		base, _ := url.Parse(currentURL)
		next, err := url.Parse(location)
		if err != nil {
			return response.StatusCode, currentURL, data, err
		}
		currentURL = base.ResolveReference(next).String()
		if !safeXAIAuthURL(currentURL) {
			return response.StatusCode, currentURL, data, errors.New("xAI OAuth redirected to untrusted host")
		}
		// Switching eligible redirects to GET drops the earlier form, while retained
		// method redirects replay it; this choice keeps grant fields tied to the redirect rule.
		if response.StatusCode == http.StatusSeeOther || ((response.StatusCode == http.StatusMovedPermanently || response.StatusCode == http.StatusFound) && currentMethod != http.MethodGet && currentMethod != http.MethodHead) {
			currentMethod = http.MethodGet
			currentForm = nil
		}
	}
	return 0, currentURL, nil, errors.New("xAI OAuth redirected too many times")
}

// seedSSOCookies installs sso and sso-rw cookies for account and issuer
// origins. Nil jars are ignored; each cookie is secure, HttpOnly, and rooted
// at / so the supplied session can begin the device conversion.
func seedSSOCookies(jar http.CookieJar, token string) {
	if jar == nil {
		return
	}
	for _, rawURL := range []string{SSOAccountsURL, OAuthIssuer + "/"} {
		// Seed origins independently: a parse failure skips this cookie set while
		// the other origin can still initialize its own host-only SSO cookies.
		target, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		jar.SetCookies(target, []*http.Cookie{
			{Name: "sso", Value: token, Path: "/", Secure: true, HttpOnly: true},
			{Name: "sso-rw", Value: token, Path: "/", Secure: true, HttpOnly: true},
		})
	}
}

// captureCookies trims and filters response cookie names/values before
// installing them in the private jar. Nil flow, jar, URL, or response is
// ignored. Name/value length and control-byte checks discard unusable
// entries; cookiejar then applies their domain/path scope.
func (f *ssoDeviceFlow) captureCookies(requestURL *url.URL, response *http.Response) {
	if f == nil || f.cookieJar == nil || requestURL == nil || response == nil {
		return
	}
	cookies := make([]*http.Cookie, 0)
	for _, cookie := range response.Cookies() {
		name := strings.TrimSpace(cookie.Name)
		value := strings.TrimSpace(cookie.Value)
		// Filter unusable cookie names and values before jar installation; accepted
		// entries still rely on cookiejar's domain and path rules when preparing another request.
		if name == "" || len(name) > 128 || len(value) > 16384 || strings.ContainsAny(name+value, "\r\n\x00") {
			continue
		}
		cookie.Name = name
		cookie.Value = value
		cookies = append(cookies, cookie)
	}
	f.cookieJar.SetCookies(requestURL, cookies)
}

// cookieHeader formats jar cookies applicable to requestURL in sorted-name
// order. Nil flow, jar, or URL returns empty. Scope selection belongs to
// cookiejar, so cookies from another host or path do not automatically enter
// this request header.
func (f *ssoDeviceFlow) cookieHeader(requestURL *url.URL) string {
	if f == nil || f.cookieJar == nil || requestURL == nil {
		return ""
	}
	cookies := f.cookieJar.Cookies(requestURL)
	sort.Slice(cookies, func(i, j int) bool { return cookies[i].Name < cookies[j].Name })
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

// safeXAIAuthURL normally accepts HTTPS x.ai or subdomain URLs after
// rejecting userinfo and missing hostname. Unsafe override mode accepts other
// nonempty schemes/hosts but still rejects userinfo; ports, queries, and
// fragments are not independently restricted here.
func safeXAIAuthURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" {
		return false
	}
	// Unsafe mode relaxes scheme and host policy after the userinfo guard, so
	// that earlier credential-bearing authority rejection still applies to injected endpoints.
	if AllowUnsafeURLOverrides() {
		return parsed.Scheme != "" && parsed.Host != ""
	}
	if parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "x.ai" || strings.HasSuffix(host, ".x.ai")
}

// NormalizeSSOToken selects the first sso or sso-rw value from a cookie
// header, or uses raw token text before a semicolon. Cookie/name matching is
// case-insensitive; selected text is sanitized and oversized tokens return
// empty for unauthorized handling.
//
// The selected cookie value is sanitized after extraction, so other cookie
// names do not become part of that token text. Authentication remains the
// later accounts-session check; this helper interprets and bounds the
// temporary input before it is seeded into the conversion's private cookie
// jar.
func NormalizeSSOToken(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "cookie:") {
		value = strings.TrimSpace(value[len("cookie:"):])
	}
	for part := range strings.SplitSeq(value, ";") {
		name, token, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "sso", "sso-rw":
			return sanitizeSSOToken(token)
		}
	}
	if token, _, found := strings.Cut(value, ";"); found {
		value = strings.TrimSpace(token)
	}
	return sanitizeSSOToken(value)
}

// sanitizeSSOToken trims a token and removes CR, LF, and NUL bytes before
// checking the length cap. Oversized sanitized text returns empty; shorter
// text is retained for cookie seeding rather than verified as an
// authenticated web session.
func sanitizeSSOToken(value string) string {
	value = strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(strings.TrimSpace(value))
	// Reject oversized token input before it reaches cookie headers. Removing
	// control bytes first makes the cap apply to the actual token text being retained.
	if len(value) > ssoMaxTokenLength {
		return ""
	}
	return value
}

// DecodeJWTClaims decodes the second dot-separated segment as unpadded
// base64url JSON. Missing segments, invalid encoding, or malformed JSON
// returns nil; signature, issuer, and expiry are not verified, so callers use
// this only for token metadata.
func DecodeJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	// Claims are optional metadata for conversion callers. Invalid payload encoding
	// or JSON yields no claims, allowing string and tier readers to report missing metadata.
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}

// JWTClaimString returns a trimmed string claim, or empty when absent or
// another type. Numeric claims receive no conversion here; callers needing
// numeric subscription tiers use their dedicated claim interpretation helper.
func JWTClaimString(claims map[string]any, key string) string {
	value, _ := claims[key].(string)
	return strings.TrimSpace(value)
}

// sleepContext waits for its timer or ctx cancellation and returns the
// context error when that branch wins. It stops the timer on return; when
// both are ready, select does not guarantee which result wins.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// minDuration selects the smaller duration when a is positive, otherwise b.
// It does not reject nonpositive b, so callers supply their desired positive
// polling cap as that second argument.
func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if a < b {
		return a
	}
	return b
}

// firstNonEmpty returns the first nonblank trimmed string, or empty when none
// exists. It retains input order so callers can prefer an earlier identifier
// or error description before later alternatives.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
