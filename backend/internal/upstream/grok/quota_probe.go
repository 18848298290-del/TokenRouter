package grok

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// quota_probe.go sends an active Grok quota probe and records response
// evidence before status handling.
// An active quota request records its response headers before interpreting
// HTTP status. This retains a probe attempt even when no quota headers
// arrive, and treats 429 as an observed outcome. The request uses a caller-
// supplied timeout and closes its response after observation and status
// handling.

// ActiveQuotaOptions supplies the request, timeout, observation sink, and
// transport callbacks for a quota probe.
type ActiveQuotaOptions struct {
	URL string // validated Responses endpoint URL
	// Body is read through the request's existing byte storage. Callers keep
	// this slice immutable for the probe exchange while its transport uses that reader.
	Body       []byte // prepared quota probe JSON
	Token      string `json:"-"` // upstream bearer access token
	ProviderID int64  // provider identifier for warnings
	Model      string // model name for diagnostics
	// Timeout is passed directly to context.WithTimeout. Nonpositive values
	// expire the child context immediately; callers choose a positive budget for
	// useful probes.
	Timeout time.Duration // probe child context duration
	// ApplyHeaders and Do are required for the exchange, followed by required
	// Observe on its response. Observe runs before status classification and
	// response-body closure, so it can retain headerless outcomes as evidence.
	ApplyHeaders func(http.Header)                           // probe header override callback
	Do           func(*http.Request) (*http.Response, error) // quota request transport callback
	Observe      func(*QuotaSnapshot, int)                   // quota observation sink callback
	// MapStatus and Warn are required for HTTP errors other than 429. They
	// receive status diagnostics while response consumption remains bounded by
	// the probe.
	MapStatus func(int) int        // upstream status mapping callback
	Warn      func(string, ...any) // quota warning emission callback
}

// String returns a fixed active-quota options label omitting credential
// values.
func (ActiveQuotaOptions) String() string { return "grok active quota options" }

// GoString returns the credential-free active-quota options label for Go
// formatting.
func (o ActiveQuotaOptions) GoString() string { return o.String() }

// FetchActiveQuota sends the supplied probe body under a child timeout.
// Supply ApplyHeaders, Do, and Observe; error-status handling also requires
// MapStatus and Warn. Do must return a response with a body when its error is
// nil. Callers validate URL and prepare the model body before invoking it.
//
// Observe receives an active_probe snapshot before status handling, including
// headerless outcomes. A 429 then returns nil; other error statuses consume
// up to four KiB and return mapped errors. The body closes after these
// callbacks finish. Timeout cancellation belongs to the child context, so
// transports and callbacks determine how promptly the operation returns.
func FetchActiveQuota(ctx context.Context, options ActiveQuotaOptions) error {
	callCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, options.URL, bytes.NewReader(options.Body))
	if err != nil {
		return infraerrors.Newf(infraerrors.CategoryInternalServer, "GROK_QUOTA_PROBE_REQUEST_BUILD_FAILED", "failed to build upstream request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+options.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	options.ApplyHeaders(req.Header)
	resp, err := options.Do(req)
	if err != nil {
		return infraerrors.Newf(infraerrors.CategoryBadGateway, "GROK_QUOTA_PROBE_REQUEST_FAILED", "upstream probe failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	snapshot := ObserveQuotaHeaders(resp.Header, resp.StatusCode, "active_probe")
	// Publish evidence before status errors return so rejected or headerless
	// probes still contribute their attempt metadata to the caller's quota handling.
	options.Observe(snapshot, resp.StatusCode)
	// The observation already captures throttling. Return success for the probe
	// operation so callers can use that status to update quota state independently.
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil
	}
	if resp.StatusCode >= 400 {
		const reason = "GROK_QUOTA_PROBE_UPSTREAM_ERROR"
		// Error details are status-based, so consume a bounded prefix before closure;
		// a streaming or oversized error body cannot require unbounded discard work here.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		options.Warn("grok_quota_probe_failed", "provider_id", options.ProviderID, "model", options.Model, "status", resp.StatusCode, "reason", reason)
		return infraerrors.Newf(infraerrors.Category(options.MapStatus(resp.StatusCode)), reason, "upstream returned %d for probe model %q", resp.StatusCode, options.Model)
	}
	return nil
}
