package grok

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// billing_fetch.go fetches Grok billing summaries with bounded attempts and
// preserves HTTP status evidence.
// Billing probes can retain an HTTP status even when no summary is available.
// Transport failures and selected gateway statuses retry within the
// configured attempt count; 429 is returned as quota evidence. Responses from
// nil-error transport exchanges are read with a cap and closed before any
// retry delay.

// BillingFetchOptions supplies endpoint identity, retry policy, and callbacks
// for one billing window.
type BillingFetchOptions struct {
	URL        string // validated billing endpoint URL
	Token      string `json:"-"` // upstream bearer access token
	ProviderID int64  // provider identifier for warnings
	Weekly     bool   // weekly probe warning label
	// MaxAttempts and RetryDelay bound sequential retries for transient exchange
	// failures. Nonpositive attempts perform no request, and ctx can interrupt
	// each retry delay or a context-aware transport exchange.
	MaxAttempts int           // maximum request attempt count
	RetryDelay  time.Duration // delay between retry attempts
	// Do and ApplyHeaders are required for every attempted exchange. Do returns
	// a response with a readable body when its error is nil; ApplyHeaders runs
	// after the pinned billing identity has been installed.
	Do           func(*http.Request) (*http.Response, error) // billing request transport callback
	ApplyHeaders func(http.Header)                           // provider header override callback
	// Truncate, MapStatus, and Warn are required when an HTTP error other than
	// 429 reaches status handling. The excerpt is trimmed and capped for the
	// mapped error and warning.
	Truncate  func(string, int) string // failed response excerpt callback
	MapStatus func(int) int            // upstream status mapping callback
	Warn      func(string, ...any)     // billing warning emission callback
}

// FetchBilling queries one billing endpoint under ctx using MaxAttempts and
// RetryDelay. Callers validate URL before sending the token and supply Do and
// ApplyHeaders. Error-status handling also requires Truncate, MapStatus, and
// Warn; Do must return a nonnil response and body on success. Nonpositive
// MaxAttempts performs no exchange and returns a request failure.
//
// Transport errors and 502, 503, or 504 retry when attempts remain, with
// cancellation interrupting the delay. Reads are capped at one MiB and their
// errors are ignored before parsing; each successful exchange body is closed.
// A 429 returns its status with nil summary and nil error. Other error
// statuses are mapped and logged through callbacks; valid success JSON can
// still yield a nil summary when billing config supplies no usable evidence.
func FetchBilling(ctx context.Context, options BillingFetchOptions) (*BillingSummary, int, error) {
	billingURL := options.URL
	token := options.Token

	for attempt := range options.MaxAttempts {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, billingURL, nil)
		if err != nil {
			return nil, 0, infraerrors.Newf(infraerrors.Category(http.StatusInternalServerError), "GROK_QUOTA_PROBE_REQUEST_BUILD_FAILED", "failed to build billing request: %v", err)
		}
		ApplyCLIBillingHeaders(req, token)
		// Provider overrides run after the billing identity, allowing the caller's
		// permitted header settings to match those used for forwarding to this endpoint.
		options.ApplyHeaders(req.Header)
		resp, requestErr := options.Do(req)

		statusCode := 0
		var bodyBytes []byte
		if requestErr == nil {
			statusCode = resp.StatusCode
			// Read a bounded body before closing this attempt so retrying releases its
			// response resource. Partial bytes remain available when the read reports an error.
			bodyBytes, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
		}

		shouldRetry := requestErr != nil || IsRetryableBillingStatus(statusCode)
		// Delay transient failures only while another attempt remains; cancellation
		// ends the wait with the last HTTP status available to the probe caller.
		if shouldRetry && attempt+1 < options.MaxAttempts {
			timer := time.NewTimer(options.RetryDelay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil, statusCode, infraerrors.Newf(infraerrors.Category(http.StatusBadGateway), "GROK_QUOTA_PROBE_REQUEST_FAILED", "billing request failed: %v", ctx.Err())
			}
			continue
		}

		if requestErr != nil {
			return nil, 0, infraerrors.Newf(infraerrors.Category(http.StatusBadGateway), "GROK_QUOTA_PROBE_REQUEST_FAILED", "billing request failed: %v", requestErr)
		}
		// A rate-limit response is an observed quota outcome that callers can record,
		// so return its status without requiring a billing payload or synthesizing an error.
		if statusCode == http.StatusTooManyRequests {
			return nil, statusCode, nil
		}
		if statusCode >= 400 {
			// Bound the failure excerpt passed to warnings and errors.
			bodyText := options.Truncate(strings.TrimSpace(string(bodyBytes)), 240)
			options.Warn("grok_quota_billing_failed", "provider_id", options.ProviderID, "weekly", options.Weekly, "status", statusCode, "body", bodyText)
			return nil, statusCode, infraerrors.Newf(infraerrors.Category(options.MapStatus(statusCode)), "GROK_QUOTA_PROBE_UPSTREAM_ERROR", "billing returned %d: %s", statusCode, bodyText)
		}
		payload, err := ParseBillingPayload(bodyBytes)
		if err != nil {
			return nil, statusCode, infraerrors.Newf(infraerrors.Category(http.StatusBadGateway), "GROK_QUOTA_BILLING_PARSE_ERROR", "failed to parse billing body: %v", err)
		}
		return BuildBillingSummary(payload.Config), statusCode, nil
	}
	return nil, 0, infraerrors.New(infraerrors.Category(http.StatusBadGateway), "GROK_QUOTA_PROBE_REQUEST_FAILED", "billing request failed")
}

// IsRetryableBillingStatus reports whether status is 502, 503, or 504 for
// billing probe retries.
func IsRetryableBillingStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// String returns a fixed billing-options label that omits credential values.
func (BillingFetchOptions) String() string { return "grok billing options" }

// GoString returns the same credential-free billing-options label for Go
// formatting.
func (o BillingFetchOptions) GoString() string { return o.String() }
