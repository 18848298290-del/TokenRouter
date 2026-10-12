package provider

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
)

type remoteClient struct {
	httpClient *http.Client
}

// remoteClientError retains a proxy initialization failure.
// Every fetch returns that failure, preventing an unintended direct connection.
type remoteClientError struct {
	err error
}

func (c *remoteClientError) FetchCatalog(_ context.Context, _, _ string) ([]byte, string, bool, error) {
	return nil, "", false, c.err
}

// NewRemoteClient creates a remote catalog client.
// An empty proxyURL uses a direct connection; supported proxies are HTTP, HTTPS, SOCKS5, and SOCKS5H.
// When proxy initialization fails, allowDirectOnProxyError controls the result:
//   - false returns a client that reports the initialization error.
//   - true permits a direct connection.
func NewRemoteClient(proxyURL string, allowDirectOnProxyError bool) RemoteClient {
	// Proxy initialization errors may be written to server logs below.
	// Keep proxy credentials redacted in errors returned by httpclient.GetClient.
	sharedClient, err := httpclient.GetClient(httpclient.Options{
		Timeout:  30 * time.Second,
		ProxyURL: proxyURL,
	})
	if err != nil {
		if strings.TrimSpace(proxyURL) != "" && !allowDirectOnProxyError {
			slog.Warn("proxy client init failed, all requests will fail", "service", "pricing", "error", err)
			return &remoteClientError{err: fmt.Errorf("proxy client init failed and direct fallback is disabled; set security.proxy_fallback.allow_direct_on_error=true to allow fallback: %w", err)}
		}
		sharedClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &remoteClient{
		httpClient: sharedClient,
	}
}

// FetchCatalog sends an ETag validator and reports an unchanged body on HTTP 304.
func (c *remoteClient) FetchCatalog(ctx context.Context, url, etag string) ([]byte, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024+1))
	if len(body) > 32*1024*1024 {
		return nil, "", false, fmt.Errorf("model catalog exceeds 32 MiB")
	}
	return body, resp.Header.Get("ETag"), false, err
}
