package grok

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

type BillingFetchOptions struct {
	URL          string
	Token        string `json:"-"`
	ProviderID   int64
	Weekly       bool
	MaxAttempts  int
	RetryDelay   time.Duration
	Do           func(*http.Request) (*http.Response, error)
	ApplyHeaders func(http.Header)
	Truncate     func(string, int) string
	MapStatus    func(int) int
	Warn         func(string, ...any)
}

func FetchBilling(ctx context.Context, options BillingFetchOptions) (*BillingSummary, int, error) {
	billingURL := options.URL
	token := options.Token

	for attempt := range options.MaxAttempts {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, billingURL, nil)
		if err != nil {
			return nil, 0, infraerrors.Newf(infraerrors.Category(http.StatusInternalServerError), "GROK_QUOTA_PROBE_REQUEST_BUILD_FAILED", "failed to build billing request: %v", err)
		}
		ApplyCLIBillingHeaders(req, token)
		// Apply the provider header overrides used by forwarding requests.
		options.ApplyHeaders(req.Header)
		resp, requestErr := options.Do(req)

		statusCode := 0
		var bodyBytes []byte
		if requestErr == nil {
			statusCode = resp.StatusCode
			bodyBytes, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
		}

		shouldRetry := requestErr != nil || IsRetryableBillingStatus(statusCode)
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
		if statusCode == http.StatusTooManyRequests {
			return nil, statusCode, nil
		}
		if statusCode >= 400 {
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

func IsRetryableBillingStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// String omits credentials when formatting billing options for logs.
func (BillingFetchOptions) String() string     { return "grok billing options" }
func (o BillingFetchOptions) GoString() string { return o.String() }
