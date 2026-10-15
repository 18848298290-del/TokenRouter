package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// audit.go executes external moderation requests through the selected proxy
// and decodes successful responses.
// Moderation HTTP exchanges return response status separately from transport
// and decoding failures. Proxy resolution precedes the request, failed
// responses contribute a bounded diagnostic prefix, and successful bodies
// decode into the core response shape supplied by the caller.

// AuditClient posts JSON input to a moderation endpoint through direct or
// selected proxy transport.
type AuditClient struct {
	client *http.Client // instrumented direct HTTP client
}

// NewAuditClient constructs the request executor with an instrumented
// http.DefaultTransport and no client timeout. It performs no request or
// readiness check; callers supply the request context that limits each direct
// HTTP exchange.
func NewAuditClient() *AuditClient { return &AuditClient{client: timing.InstrumentClient(nil)} }

// Execute posts raw JSON using key as a bearer credential and calls the
// required proxy resolver before choosing a client. An empty proxy URL uses the
// bound direct client, with http.DefaultClient when that pointer is nil;
// resolver or proxy-client errors stop the request.
//
// The response body is closed on every received reply. Non-2xx replies return
// status and at most 512 body bytes even if reading fails; 2xx replies decode
// output and return its decode error. The supplied context controls the HTTP
// request, while the resolver applies its own captured context.
func (s *AuditClient) Execute(ctx context.Context, endpoint, key string, raw []byte, proxy func() (string, error), output any) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	url, err := proxy()
	if err != nil {
		return 0, nil, err
	}
	client := s.client
	if url != "" {
		client, err = httpclient.GetClient(httpclient.Options{ProxyURL: url})
		if err != nil {
			return 0, nil, fmt.Errorf("build moderation proxy client: %w", err)
		}
	}
	// A manually constructed executor can lack the instrumented client; use the
	// standard default so direct requests still have an HTTP transport to execute.
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	// Release the received response body after either diagnostic capture or JSON
	// decoding; closure errors do not replace the exchange or decode result.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Capture a small diagnostic prefix for HTTP failures; partial bytes survive
		// a read error, and core failure records later select generic public messages.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, body, nil
	}
	err = json.NewDecoder(resp.Body).Decode(output)
	return resp.StatusCode, nil, err
}
