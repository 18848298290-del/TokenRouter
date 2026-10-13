package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// OAuth usage queries carry the selected access token, beta Header, and
// provider fingerprint User-Agent. A configured TLS profile and transport
// select the fingerprint path; other queries use the shared HTTP client with a
// thirty-second timeout. Resolved-address validation applies to that ordinary
// path when private hosts are disallowed.

// Usage query defaults supply NewUsageClient's OAuth endpoint and the
// User-Agent selected for queries with no nonempty fingerprint.UserAgent.
const (
	defaultClaudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

	// defaultUsageUserAgent supplies the query identity when a fingerprint omits
	// UserAgent; this value is independent of the initialized CLI request defaults.
	defaultUsageUserAgent = "claude-code/2.1.7"
)

// UsageClient queries Claude OAuth usage through a selected HTTP transport.
// UsageURL, AllowPrivateHosts, and DoTLS select its endpoint and transport;
// callers configure these fields before queries and keep them stable while
// concurrent calls read them.
type UsageClient struct {
	UsageURL string // usage query endpoint
	// AllowPrivateHosts controls private-address permission on the ordinary HTTP
	// client path; the injected DoTLS transport supplies its own target policy.
	AllowPrivateHosts bool // ordinary address permission
	// DoTLS is used when a query also supplies TLSProfile; nil selects the
	// ordinary client. Successful calls supply a nonnil response with a readable
	// Body, which FetchUsageWithOptions closes after inspection.
	DoTLS func(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) // optional fingerprint transport
}

// UsageFetchOptions carries credentials and optional transport identity for one
// query.
type UsageFetchOptions struct {
	AccessToken string // OAuth bearer token
	ProxyURL    string // optional request proxy
	// ProviderID reaches DoTLS as its provider isolation argument; the ordinary
	// shared-client branch uses proxy and timeout options instead.
	ProviderID int64 // TLS provider identity
	// TLSProfile reaches DoTLS as the supplied pointer when that callback exists;
	// a missing profile or callback selects the ordinary transport.
	TLSProfile *tlsfingerprint.Profile // optional transport profile
	// Fingerprint supplies UserAgent when that string is nonempty; a nil
	// fingerprint or empty string uses the query's built-in default.
	Fingerprint *Fingerprint // optional Header identity
}

// NewUsageClient sets the default usage endpoint and optional TLS transport
// function. A nil transport still permits ordinary HTTP queries, while
// AllowPrivateHosts starts false and can be configured before use.
func NewUsageClient(doTLS func(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error)) *UsageClient {
	return &UsageClient{UsageURL: defaultClaudeUsageURL, DoTLS: doTLS}
}

// FetchUsageWithOptions queries OAuth usage with ctx and the supplied transport
// identity. Nil options return an error; a nonempty fingerprint UA overrides
// the default, and TLS transport requires both profile and DoTLS.
//
// The ordinary path uses a thirty-second timeout and checks resolved IPs when
// AllowPrivateHosts is false. Responses returned without a transport error are
// closed after status and JSON inspection. A DoTLS error returns before cleanup
// is installed, so that callback must close any body returned together with an
// error. Non-200 status becomes an upstream error containing readable body
// bytes, and malformed successful JSON returns a decode error.
func (s *UsageClient) FetchUsageWithOptions(ctx context.Context, opts *UsageFetchOptions) (*wire.ClaudeUsageResponse, error) {
	if opts == nil {
		return nil, fmt.Errorf("options is nil")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.UsageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	// Let the HTTP transport manage compression by leaving Accept-Encoding unset.
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+opts.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	// Use a nonempty provider UA for the usage query's HTTP identity, with
	// the query-specific default selected when the fingerprint supplies no usable
	// value.
	userAgent := defaultUsageUserAgent
	if opts.Fingerprint != nil && opts.Fingerprint.UserAgent != "" {
		userAgent = opts.Fingerprint.UserAgent
	}
	req.Header.Set("User-Agent", userAgent)

	var resp *http.Response

	// The injected path receives provider isolation and profile arguments together.
	// Invoke it only when both are configured; an error from that path is returned
	// directly.
	if opts.TLSProfile != nil && s.DoTLS != nil {
		resp, err = s.DoTLS(req, opts.ProxyURL, opts.ProviderID, 0, opts.TLSProfile)
		if err != nil {
			return nil, fmt.Errorf("request with TLS fingerprint failed: %w", err)
		}
	} else {
		// Queries lacking a complete fingerprint transport use the ordinary shared
		// client and its thirty-second timeout. Address checks apply when this
		// client's private-host permission is disabled.
		client, err := httpclient.GetClient(httpclient.Options{
			ProxyURL:           opts.ProxyURL,
			Timeout:            30 * time.Second,
			ValidateResolvedIP: true,
			AllowPrivateHosts:  s.AllowPrivateHosts,
		})
		if err != nil {
			return nil, fmt.Errorf("create http client failed: %w", err)
		}

		resp, err = client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}
	}
	// Both transport branches transfer a response body to this method; close it
	// after success or error decoding so their connection resources can be
	// released.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Status classification remains available even if reading the error body
		// fails; use whatever bytes were obtained for the upstream-error message
		// before closing the response.
		body, _ := io.ReadAll(resp.Body)
		msg := fmt.Sprintf("API returned status %d: %s", resp.StatusCode, string(body))
		return nil, infraerrors.InternalServer("UPSTREAM_ERROR", msg)
	}

	var usageResp wire.ClaudeUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usageResp); err != nil {
		return nil, fmt.Errorf("decode response failed: %w", err)
	}

	return &usageResp, nil
}
