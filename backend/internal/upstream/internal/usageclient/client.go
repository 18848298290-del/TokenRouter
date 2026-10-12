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

const MaxBodyBytes = 512 * 1024

type Client struct{ *usagecontract.Request }

func New(input *usagecontract.Request) *Client { return &Client{Request: input} }
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

func UpstreamUsageEndpoint(base, path string, build func(string, string) string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid base URL")
	}
	// Versioned bases such as /v1, /v4, and /v1beta already supply the API prefix.
	// Reuse the forwarding URL builder so usage queries do not append a second
	// version segment to the same provider base URL.
	endpoint := build(base, path)
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.Scheme == "" || parsedEndpoint.Host == "" {
		return "", errors.New("invalid endpoint URL")
	}
	// Callers validate the base URL. Strip non-path components again so stored
	// userinfo, query strings, or fragments cannot leak into the query endpoint.
	parsedEndpoint.User = nil
	parsedEndpoint.RawQuery = ""
	parsedEndpoint.ForceQuery = false
	parsedEndpoint.Fragment = ""
	return strings.TrimRight(parsedEndpoint.String(), "/"), nil
}

func UpstreamUsageStatusEndpoint(base string) (string, error) {
	return UpstreamUsageRootEndpoint(base, "/api/status")
}

// UpstreamUsageTokenEndpoint retains the New API token endpoint trailing slash
// to avoid redirects that the usage client intentionally rejects.
func UpstreamUsageTokenEndpoint(base string) (string, error) {
	endpoint, err := UpstreamUsageRootEndpoint(base, "/api/usage/token")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(endpoint, "/") + "/", nil
}

// UpstreamUsageWalletEndpoint builds the fixed /user/balance path used by New API forks.
// The path is defined by the adapter rather than administrator configuration.
func UpstreamUsageWalletEndpoint(base string) (string, error) {
	return UpstreamUsageRootEndpoint(base, "/user/balance")
}

// UpstreamUsageUserSelfEndpoint builds the New API /api/user/self endpoint.
// Callers authenticate this endpoint with a user access token.
func UpstreamUsageUserSelfEndpoint(base string) (string, error) {
	return UpstreamUsageRootEndpoint(base, "/api/user/self")
}

// UpstreamUsageRootEndpoint strips a trailing OpenAI version segment for
// management APIs such as New API that live under the site root.
func UpstreamUsageRootEndpoint(base, path string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid base URL")
	}
	rootPath := strings.TrimRight(parsed.Path, "/")
	if httpclient.OpenAIBaseURLHasVersionSuffix(rootPath) {
		if index := strings.LastIndex(rootPath, "/"); index >= 0 {
			rootPath = rootPath[:index]
		} else {
			rootPath = ""
		}
	}
	parsed.Path = strings.TrimRight(rootPath, "/") + "/" + strings.TrimLeft(path, "/")
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return strings.TrimRight(parsed.String(), "/"), nil
}

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

func UpstreamUsageOperationError(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return usageview.ErrUpstreamUsageTimeout
	}
	return usageview.ErrUpstreamUsageRequestFailed
}

func (c *Client) GetURL(ctx context.Context, endpoint string, authenticated bool) ([]byte, int, error) {
	token := ""
	if authenticated {
		token = c.APIKey
	}
	return c.GetURLWithBearer(ctx, endpoint, token, "")
}

// GetURLWithHeaders sends a GET with adapter-defined headers applied after provider overrides.
// Fixed headers take precedence so the adapter controls authentication and team identity.
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
	// Replace provider-supplied authentication with the adapter credentials.
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
		// Remove case variants before setting each protected header.
		for existing := range req.Header {
			if strings.EqualFold(existing, name) {
				delete(req.Header, existing)
			}
		}
		req.Header.Set(name, value)
	}
	return c.readResponse(ctx, req)
}

// GetURLWithBearer sends a management GET with the supplied bearer token and optional user ID.
// The user ID is sent in the adapter-defined New-Api-User header.
func (c *Client) GetURLWithBearer(ctx context.Context, endpoint, bearerToken, userID string) ([]byte, int, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, 0, usageview.ErrUpstreamUsageConfigInvalid
	}
	// endpoint already contains the complete management path.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, usageview.ErrUpstreamUsageRequestFailed
	}
	req = req.WithContext(c.Context(req.Context()))
	req.Header.Set("Accept", "application/json")
	c.ApplyHeaders(req.Header)
	// Set the query credentials after provider header overrides.
	req.Header.Del("Authorization")
	if strings.TrimSpace(bearerToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearerToken))
	}
	req.Header.Del("New-Api-User")
	if strings.TrimSpace(userID) != "" {
		req.Header.Set("New-Api-User", strings.TrimSpace(userID))
	}
	return c.readResponse(ctx, req)
}

// readResponse reads a bounded usage response and closes its body, including on read failure.
func (c *Client) readResponse(ctx context.Context, req *http.Request) ([]byte, int, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, UpstreamUsageOperationError(ctx, err)
	}
	if resp == nil || resp.Body == nil {
		return nil, 0, usageview.ErrUpstreamUsageInvalidResponse
	}
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
