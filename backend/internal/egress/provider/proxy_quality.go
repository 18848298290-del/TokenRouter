package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
)

// proxy_quality.go probes model endpoints through a proxy and classifies their
// reachability and challenge responses.
// Unauthenticated quality probes classify target reachability from response
// status and a bounded challenge preview. Recognized Cloudflare challenges in
// 403/429 responses classify before acceptable statuses. Other allowed responses
// pass, while unaccepted 429 responses warn and other unexpected statuses fail
// the assessment.
//
// Targets run sequentially through a shared proxy client with per-request
// timeout settings. Received responses measure elapsed time through headers, use
// a bounded body prefix, and close that body. Failed exchanges retain attempt
// time after client.Do errors; request-construction failures keep zero latency.
// Oversized payloads are truncated for classification, so later body evidence
// can remain outside inspection. Endpoint failures become items and later
// targets still run; client construction failure returns the batch-level error.

// ProxyQualityHTTP probes targets sequentially through a shared client and
// records each result.
type ProxyQualityHTTP struct{}

// ProbeTargets probes the supplied endpoints sequentially through a shared
// proxy client and returns one item per target. Client construction failure
// returns an error; individual endpoint failures stay in the result sequence so
// later targets still run.
func (ProxyQualityHTTP) ProbeTargets(ctx context.Context, proxyURL string, targets []egress.ProxyQualityTarget) ([]egress.ProxyQualityCheckItem, error) {
	client, err := httpclient.GetClient(httpclient.Options{ProxyURL: proxyURL, Timeout: 15 * time.Second, ResponseHeaderTimeout: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	out := make([]egress.ProxyQualityCheckItem, 0, len(targets))
	// Keep per-endpoint failures as assessment items so another reachable
	// platform can still contribute evidence to this proxy's quality summary.
	for _, target := range targets {
		out = append(out, RunProxyQualityTarget(ctx, client, target))
	}
	return out, nil
}

// RunProxyQualityTarget sends one unauthenticated target request and returns
// its observed quality classification. It records response status and header
// latency, reads a bounded body prefix, and closes the received body before
// returning.
//
// Challenge evidence has precedence over allowed statuses, with
// IsCloudflareChallengeResponse applying its own smaller preview limit within
// the retained body. Membership in AllowedStatuses passes even for expected
// authentication errors; an unmatched 429 warns, and other unexpected statuses
// fail. Read errors fail too. Body overflow is truncated for classification, so
// later challenge text can remain outside the inspected evidence.
func RunProxyQualityTarget(ctx context.Context, client *http.Client, target egress.ProxyQualityTarget) egress.ProxyQualityCheckItem {
	item := egress.ProxyQualityCheckItem{
		Target: target.Target,
	}

	req, err := http.NewRequestWithContext(ctx, target.Method, target.URL, nil)
	if err != nil {
		item.Status = "fail"
		item.Message = fmt.Sprintf("failed to build request: %v", err)
		return item
	}
	req.Header.Set("Accept", "application/json,text/html,*/*")
	req.Header.Set("User-Agent", egress.ProxyQualityClientUserAgent)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		item.Status = "fail"
		item.LatencyMs = time.Since(start).Milliseconds()
		item.Message = fmt.Sprintf("request failed: %v", err)
		return item
	}
	defer func() { _ = resp.Body.Close() }()
	item.LatencyMs = time.Since(start).Milliseconds()
	item.HTTPStatus = resp.StatusCode

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, egress.ProxyQualityMaxBodyBytes+1))
	if readErr != nil {
		item.Status = "fail"
		item.Message = fmt.Sprintf("failed to read response: %v", readErr)
		return item
	}
	// Quality classification accepts a bounded prefix rather than a complete
	// payload. The extra byte detects overflow so only the configured preview is
	// inspected.
	if int64(len(body)) > egress.ProxyQualityMaxBodyBytes {
		body = body[:egress.ProxyQualityMaxBodyBytes]
	}

	// Challenge evidence disqualifies the response even if its status is
	// allowlisted for unauthenticated reachability; record that classification
	// before status lookup.
	if egress.IsCloudflareChallengeResponse(resp.StatusCode, resp.Header, body) {
		item.Status = "challenge"
		item.CFRay = egress.ExtractCloudflareRayID(resp.Header, body)
		item.Message = "Cloudflare challenge detected"
		return item
	}

	if _, ok := target.AllowedStatuses[resp.StatusCode]; ok {
		// Allowed statuses establish reachability. Unauthenticated probes can expect
		// 401 or 405 responses, which pass the connectivity check without a score
		// penalty.
		item.Status = "pass"
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			item.Message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		} else {
			item.Message = fmt.Sprintf("HTTP %d (target reachable)", resp.StatusCode)
		}
		return item
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		item.Status = "warn"
		item.Message = "Target returned 429, possible rate limiting"
		return item
	}

	item.Status = "fail"
	item.Message = fmt.Sprintf("unexpected status code: %d", resp.StatusCode)
	return item
}
