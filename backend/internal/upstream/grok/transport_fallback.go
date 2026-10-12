package grok

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	grokCLIProxyHost       = CLIProxyHost
	grokOfficialAPIHost    = "api.x.ai"
	grokCLIStableVersion   = CLIClientVersion
	grokCLIVersionOverride = CLIVersionEnv
	grokFallbackBodyLimit  = 64 << 10
)

// AccessDeniedFallbackTransport retries replayable CLI OAuth requests once at
// api.x.ai when a 403 matches a recognized CLI compatibility denial.
// Other authorization failures return to the caller for provider handling.
type AccessDeniedFallbackTransport struct {
	Base http.RoundTripper
}

// prefixedReadCloser replays a buffered prefix and closes the underlying response body.
type prefixedReadCloser struct {
	io.Reader
	io.Closer
}

// ClientWithAccessDeniedFallback clones the client and wraps its transport with CLI denial recovery.
func ClientWithAccessDeniedFallback(client *http.Client) *http.Client {
	if client == nil {
		return nil
	}
	clone := *client
	base := clone.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = &AccessDeniedFallbackTransport{Base: base}
	return &clone
}

// RoundTrip retries recognized CLI compatibility denials with a replayable OAuth request.
func (t *AccessDeniedFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.Base.RoundTrip(req)
	if err != nil || !IsCLIAccessDeniedFallbackCandidate(req, resp) {
		return resp, err
	}

	body, ok := bufferSmallResponseBody(resp, grokFallbackBodyLimit)
	if !ok || !IsCLICompatibilityAccessDenied(body) {
		return resp, nil
	}

	fallbackReq, err := newGrokOfficialAPIFallbackRequest(req)
	if err != nil {
		return resp, nil
	}
	fallbackResp, fallbackErr := t.Base.RoundTrip(fallbackReq)
	if fallbackErr != nil {
		slog.Debug("grok_cli_access_denied_api_fallback_failed", "path", req.URL.EscapedPath(), "error", fallbackErr)
		return resp, nil
	}
	if fallbackResp.StatusCode < http.StatusOK || fallbackResp.StatusCode >= http.StatusMultipleChoices {
		if fallbackResp.Body != nil {
			_ = fallbackResp.Body.Close()
		}
		return resp, nil
	}

	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	slog.Warn("grok_cli_access_denied_api_fallback_succeeded", "method", req.Method, "path", req.URL.EscapedPath())
	return fallbackResp, nil
}

// IsCLICompatibilityAccessDenied recognizes plain access-denied and structured chat permission errors.
func IsCLICompatibilityAccessDenied(body []byte) bool {
	lower := bytes.ToLower(body)
	if bytes.Contains(lower, []byte("access denied")) {
		return true
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || !strings.EqualFold(strings.TrimSpace(payload.Code), "permission_denied") {
		return false
	}
	const chatEndpointDeniedPrefix = "access to the chat endpoint is denied. please ensure you're using the correct credentials. if you believe this is a mistake, please"
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(payload.Error)), chatEndpointDeniedPrefix)
}

// IsCLIAccessDeniedFallbackCandidate checks host, identity, status, and replayability before
// reading the body.
func IsCLIAccessDeniedFallbackCandidate(req *http.Request, resp *http.Response) bool {
	return req != nil && req.URL != nil && req.GetBody != nil && resp != nil &&
		resp.StatusCode == http.StatusForbidden &&
		strings.EqualFold(strings.TrimSpace(req.URL.Hostname()), grokCLIProxyHost) &&
		strings.EqualFold(strings.TrimSpace(req.Header.Get("X-XAI-Token-Auth")), "xai-grok-cli") &&
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Header.Get("Authorization"))), "bearer ")
}

// newGrokOfficialAPIFallbackRequest rebuilds the body for api.x.ai and removes CLI-specific headers.
func newGrokOfficialAPIFallbackRequest(req *http.Request) (*http.Request, error) {
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	fallbackReq := req.Clone(req.Context())
	fallbackReq.Body = body
	fallbackReq.URL = cloneURL(req.URL)
	fallbackReq.URL.Scheme = "https"
	fallbackReq.URL.Host = grokOfficialAPIHost
	fallbackReq.Host = ""
	fallbackReq.RequestURI = ""
	fallbackReq.Header = req.Header.Clone()
	for _, header := range []string{
		"X-XAI-Token-Auth",
		"X-Grok-Client-Version",
		"X-Grok-Client-Surface",
		"X-UserID",
		"X-Email",
		"User-Agent",
	} {
		fallbackReq.Header.Del(header)
	}
	return fallbackReq, nil
}

// cloneURL copies a URL so target rewriting does not mutate the caller's request.
func cloneURL(value *url.URL) *url.URL {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// bufferSmallResponseBody bounds reads and restores consumed bytes on read failure or overflow.
func bufferSmallResponseBody(resp *http.Response, limit int64) ([]byte, bool) {
	if resp == nil || resp.Body == nil || limit <= 0 {
		return nil, false
	}
	original := resp.Body
	body, err := io.ReadAll(io.LimitReader(original, limit+1))
	if err != nil || int64(len(body)) > limit {
		resp.Body = &prefixedReadCloser{
			Reader: io.MultiReader(bytes.NewReader(body), original),
			Closer: original,
		}
		return nil, false
	}
	_ = original.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return body, true
}

// ApplyTransportCLIHeaders adds Grok Build identity before the shared transport sends a request.
// It matches the CLI proxy hostname for Responses, Chat, media, quota probes,
// and provider tests; other hosts pass through unchanged.
func ApplyTransportCLIHeaders(req *http.Request) {
	if req == nil || req.URL == nil || !strings.EqualFold(strings.TrimSpace(req.URL.Hostname()), grokCLIProxyHost) {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	version := strings.TrimSpace(os.Getenv(grokCLIVersionOverride))
	if !IsTransportCLIVersionSupported(version) {
		version = grokCLIStableVersion
	}
	req.Header.Set("X-XAI-Token-Auth", CLITokenAuth)
	req.Header.Set("x-grok-client-version", version)
	req.Header.Set("x-grok-client-identifier", CLIClientIdentifier)
	req.Header.Set("User-Agent", CLIUserAgent(version))
}

// IsTransportCLIVersionSupported accepts canonical SemVer at or above CLIClientVersion.
func IsTransportCLIVersionSupported(version string) bool {
	canonical := "v" + version
	minimum := "v" + CLIClientVersion
	return semver.IsValid(canonical) &&
		semver.Canonical(canonical) == canonical &&
		semver.Compare(canonical, minimum) >= 0
}
