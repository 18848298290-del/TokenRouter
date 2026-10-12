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

const (
	defaultClaudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

	// Default usage-query User-Agent from captured CLI traffic.
	defaultUsageUserAgent = "claude-code/2.1.7"
)

type UsageClient struct {
	UsageURL          string
	AllowPrivateHosts bool
	DoTLS             func(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error)
}

// UsageFetchOptions supplies credentials and transport identity for a usage query.
type UsageFetchOptions struct {
	AccessToken string                  // OAuth access token
	ProxyURL    string                  // Optional proxy URL.
	ProviderID  int64                   // Separates provider connection pools.
	TLSProfile  *tlsfingerprint.Profile // Nil disables TLS fingerprint selection.
	Fingerprint *Fingerprint            // Cached request fingerprint; supplies the User-Agent.
}

// NewUsageClient creates a usage client with an optional TLS fingerprint transport.
func NewUsageClient(doTLS func(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error)) *UsageClient {
	return &UsageClient{UsageURL: defaultClaudeUsageURL, DoTLS: doTLS}
}

// FetchUsageWithOptions queries OAuth usage using the selected UA and TLS profile.
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

	// Prefer the cached fingerprint UA when available.
	userAgent := defaultUsageUserAgent
	if opts.Fingerprint != nil && opts.Fingerprint.UserAgent != "" {
		userAgent = opts.Fingerprint.UserAgent
	}
	req.Header.Set("User-Agent", userAgent)

	var resp *http.Response

	// TLS fingerprinting requires both a profile and a transport implementation.
	if opts.TLSProfile != nil && s.DoTLS != nil {
		resp, err = s.DoTLS(req, opts.ProxyURL, opts.ProviderID, 0, opts.TLSProfile)
		if err != nil {
			return nil, fmt.Errorf("request with TLS fingerprint failed: %w", err)
		}
	} else {
		// Use the ordinary HTTP client when TLS fingerprinting is unavailable.
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
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
