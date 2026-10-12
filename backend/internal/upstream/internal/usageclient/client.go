package usageclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// Usage adapters query fixed provider endpoints through a supplied request
// snapshot. A forwarding URL builder handles versioned API bases, while root
// management paths remove a trailing OpenAI version segment and discard
// credential-bearing URL components. The client returns the raw HTTP status
// for the adapter to interpret after the body has been read.
//
// Provider header overrides run before adapter credentials. Fixed-header
// requests remove matching case variants for each supplied protected header;
// bearer requests replace canonical Authorization and New-Api-User values
// while retaining other overrides. Body reads reserve one byte beyond the
// display limit so an oversized response is rejected instead of silently
// normalized from a truncated prefix. The request callbacks own transport
// policy, including redirect restrictions.

// MaxBodyBytes bounds usage-response bytes before adapters decode and
// normalize a remote balance or quota response.
const MaxBodyBytes = 512 * 1024

// Client reads usage responses through the callbacks in a borrowed request snapshot.
type Client struct {
	// Request retains the supplied snapshot pointer. Configure its callbacks
	// before querying and keep credentials and callbacks stable while a query runs.
	*usagecontract.Request // shared usage request snapshot
}

// New creates a usage client borrowing input and its callback functions. It
// performs no snapshot validation; query methods require the fields and
// callbacks used by their selected request path.
func New(input *usagecontract.Request) *Client { return &Client{Request: input} }

// Get builds an adapter path using the configured forwarding endpoint builder
// and sends a bounded GET. authenticated selects APIKey as the bearer token;
// endpoint construction errors become configuration errors.
func (c *Client) Get(ctx context.Context, path string, authenticated bool) ([]byte, int, error) {
	endpoint, err := UpstreamUsageEndpoint(c.BaseURL, path, c.Endpoint)
	if err != nil {
		return nil, 0, usageview.ErrUpstreamUsageConfigInvalid.WithCause(err)
	}
	token := ""
	if authenticated {
		token = c.APIKey
	}
	return c.GetURLWithBearer(ctx, endpoint, token, "")
}

// UpstreamUsageEndpoint delegates path joining to the supplied nonnil
// forwarding builder after checking base scheme and host. It checks the
// resulting endpoint again, strips userinfo, query, and fragment, and removes
// trailing slashes. Callers enforce destination policy separately.
func UpstreamUsageEndpoint(base, path string, build func(string, string) string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid base URL")
	}
	// Versioned bases already supply the API prefix. Delegating to the forwarding
	// builder keeps usage paths aligned with the provider base used for model requests.
	endpoint := build(base, path)
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.Scheme == "" || parsedEndpoint.Host == "" {
		return "", errors.New("invalid endpoint URL")
	}
	// Usage adapters authenticate through headers. Remove stored URL credentials
	// and non-path components before this endpoint reaches the query transport.
	parsedEndpoint.User = nil
	parsedEndpoint.RawQuery = ""
	parsedEndpoint.ForceQuery = false
	parsedEndpoint.Fragment = ""
	return strings.TrimRight(parsedEndpoint.String(), "/"), nil
}

// UpstreamUsageStatusEndpoint builds /api/status relative to the management
// root. The root helper removes a recognized trailing OpenAI version segment,
// retains other prefix paths, and validates URL scheme and host.
func UpstreamUsageStatusEndpoint(base string) (string, error) {
	return UpstreamUsageRootEndpoint(base, "/api/status")
}

// UpstreamUsageTokenEndpoint appends a slash to the formatted New API token
// endpoint. Query-free bases produce the required path slash for transports
// that disable redirects. A base ending in a bare question mark retains
// ForceQuery through the root helper, so the appended slash becomes query
// text rather than a path delimiter; callers avoid that base spelling.
func UpstreamUsageTokenEndpoint(base string) (string, error) {
	endpoint, err := UpstreamUsageRootEndpoint(base, "/api/usage/token")
	if err != nil {
		return "", err
	}
	// New API can redirect the token path without its trailing slash. Restore
	// that slash before a credential-bearing query uses a redirect-disabled transport.
	return strings.TrimRight(endpoint, "/") + "/", nil
}

// UpstreamUsageWalletEndpoint builds /user/balance for compatible New API
// wallet queries. The path is fixed by the adapter; the root helper handles
// configured prefix paths and URL component cleanup.
func UpstreamUsageWalletEndpoint(base string) (string, error) {
	return UpstreamUsageRootEndpoint(base, "/user/balance")
}

// UpstreamUsageUserSelfEndpoint builds /api/user/self for a New API user
// wallet query. The caller supplies the separate wallet access token when
// sending this management request through the client.
func UpstreamUsageUserSelfEndpoint(base string) (string, error) {
	return UpstreamUsageRootEndpoint(base, "/api/user/self")
}

// UpstreamUsageRootEndpoint appends a fixed management path after removing a
// recognized final OpenAI version segment. It requires a parseable scheme and
// host and keeps any preceding base path.
//
// Path rewriting clears RawPath so a stale escaped path cannot select the
// earlier endpoint. Userinfo, query contents, and fragments are removed before
// formatting; ForceQuery remains, so a forced empty query can retain its bare
// question mark. Terminal slash characters are trimmed from the formatted URL
// text for the caller to restore when needed. Destination-policy validation
// remains with the request builder.
func UpstreamUsageRootEndpoint(base, path string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid base URL")
	}
	rootPath := strings.TrimRight(parsed.Path, "/")
	// Management paths live above the final API version component. Keep any
	// deployment prefix while removing the recognized suffix used for model forwarding.
	if httpclient.OpenAIBaseURLHasVersionSuffix(rootPath) {
		if index := strings.LastIndex(rootPath, "/"); index >= 0 {
			rootPath = rootPath[:index]
		} else {
			rootPath = ""
		}
	}
	parsed.Path = strings.TrimRight(rootPath, "/") + "/" + strings.TrimLeft(path, "/")
	// A rewritten Path needs matching URL formatting. Clear the retained escape
	// spelling and credential-bearing components so they cannot restore the
	// earlier target.
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return strings.TrimRight(parsed.String(), "/"), nil
}

// UpstreamUsageHTTPError maps an HTTP status to a shared usage-query error,
// with 2xx returning nil. unsupported controls whether 404 and 405 mean
// unsupported adapter; other unmatched statuses mean invalid response.
func UpstreamUsageHTTPError(status int, unsupported bool) error {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return usageview.ErrUpstreamUsageAuthFailed
	case http.StatusTooManyRequests:
		return usageview.ErrUpstreamUsageRateLimited
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		if unsupported {
			return usageview.ErrUpstreamUsageUnsupported
		}
	}
	return usageview.ErrUpstreamUsageInvalidResponse
}

// UpstreamUsageOperationError classifies a transport or body-read failure
// using err and the original nonnil query context. A deadline reported by
// either source selects timeout, so an expired caller still receives that
// category when the transport callback reports a different failure.
//
// Request callbacks can substitute the HTTP request's context. Passing the
// original context here retains caller deadline evidence across that
// substitution. Cancellation and remaining operation failures select the
// request-failed category, while adapters classify successfully read response
// content separately.
func UpstreamUsageOperationError(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return usageview.ErrUpstreamUsageTimeout
	}
	return usageview.ErrUpstreamUsageRequestFailed
}

// GetURL sends a bounded GET to an already-built endpoint using the configured
// request callbacks. authenticated selects APIKey as the bearer token; false
// sends no canonical Authorization value after overrides.
func (c *Client) GetURL(ctx context.Context, endpoint string, authenticated bool) ([]byte, int, error) {
	token := ""
	if authenticated {
		token = c.APIKey
	}
	return c.GetURLWithBearer(ctx, endpoint, token, "")
}

// GetURLWithHeaders sends a bounded GET with adapter-defined headers after
// provider overrides. Blank fixed names or values are skipped, and CR/LF in a
// retained name or value rejects the request.
//
// For each supplied header, case-insensitive map entries are removed before
// its canonical value is set. Callers supply at most one spelling of each
// fixed header name because competing map entries have no iteration priority.
// Canonical Authorization and api-key are removed first; the response status
// remains for adapter validation.
func (c *Client) GetURLWithHeaders(ctx context.Context, endpoint string, fixedHeaders map[string]string) ([]byte, int, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, 0, usageview.ErrUpstreamUsageConfigInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, usageview.ErrUpstreamUsageRequestFailed
	}
	req = req.WithContext(c.Context(req.Context()))
	req.Header.Set("Accept", "application/json")
	c.ApplyHeaders(req.Header)
	// Fixed-header adapters choose their query authentication separately from
	// forwarding credentials. Remove canonical authorization headers before the
	// adapter applies its selected values.
	req.Header.Del("Authorization")
	req.Header.Del("api-key")
	for headerName, headerValue := range fixedHeaders {
		name := strings.TrimSpace(headerName)
		value := strings.TrimSpace(headerValue)
		if name == "" || value == "" {
			continue
		}
		if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return nil, 0, usageview.ErrUpstreamUsageConfigInvalid
		}
		// Header override callbacks can insert map keys with noncanonical casing.
		// Remove every spelling of this fixed name so the adapter value reaches
		// transport.
		for existing := range req.Header {
			if strings.EqualFold(existing, name) {
				delete(req.Header, existing)
			}
		}
		req.Header.Set(name, value)
	}
	return c.readResponse(ctx, req)
}

// GetURLWithBearer sends a bounded management GET with a supplied bearer token
// and optional user ID. It requires configured request callbacks and an
// endpoint with scheme and host.
//
// Provider overrides are applied first, then canonical Authorization and
// New-Api-User entries are replaced by trimmed query credentials. Empty
// arguments remove those canonical entries. Other override headers, including
// api-key, remain present; callers providing noncanonical header map keys
// account for that behavior. The returned HTTP status is interpreted by the
// selected adapter.
func (c *Client) GetURLWithBearer(ctx context.Context, endpoint, bearerToken, userID string) ([]byte, int, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, 0, usageview.ErrUpstreamUsageConfigInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, usageview.ErrUpstreamUsageRequestFailed
	}
	req = req.WithContext(c.Context(req.Context()))
	req.Header.Set("Accept", "application/json")
	c.ApplyHeaders(req.Header)
	// Management queries can use a wallet token instead of the forwarding key.
	// Install that canonical Authorization value after overrides; other header
	// names remain.
	req.Header.Del("Authorization")
	if strings.TrimSpace(bearerToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearerToken))
	}
	// The optional wallet user ID belongs to this query. Clear the canonical
	// override first so an absent argument does not inherit another user identity.
	req.Header.Del("New-Api-User")
	if strings.TrimSpace(userID) != "" {
		req.Header.Set("New-Api-User", strings.TrimSpace(userID))
	}
	return c.readResponse(ctx, req)
}

// readResponse reads a bounded body after a successful transport result and
// returns its HTTP status. It rejects nil responses, nil bodies, and oversized
// payloads; transport and read errors are classified using the original
// context. Every nonnil body reached after a successful Do is closed, with
// close errors discarded; responses returned alongside transport errors remain
// the transport callback's responsibility.
func (c *Client) readResponse(ctx context.Context, req *http.Request) ([]byte, int, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, UpstreamUsageOperationError(ctx, err)
	}
	if resp == nil || resp.Body == nil {
		return nil, 0, usageview.ErrUpstreamUsageInvalidResponse
	}
	// Read one extra byte to distinguish an exactly bounded response from an
	// oversized one, then release the body even when reading failed. Close errors
	// do not replace the payload read result or its operation classification.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, 0, UpstreamUsageOperationError(ctx, readErr)
	}
	if int64(len(body)) > MaxBodyBytes {
		return nil, 0, usageview.ErrUpstreamUsageInvalidResponse
	}
	return body, resp.StatusCode, nil
}
