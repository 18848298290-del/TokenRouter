package httpapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// TestBillingErrorDetails_MapsGroupRPMExceededToTooManyRequests supplies the
// group RPM limit error. It requires 429, the shared rate code and a positive
// retry budget bounded by one minute.
func TestBillingErrorDetails_MapsGroupRPMExceededToTooManyRequests(t *testing.T) {
	status, code, msg, retryAfter := BillingErrorDetails(scheduler.ErrGroupRPMExceeded)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "rate_limit_exceeded", code)
	require.NotEmpty(t, msg)
	require.Greater(t, retryAfter, 0, "RPM exceeded should return positive Retry-After")
	require.LessOrEqual(t, retryAfter, 60)
}

// TestBillingErrorDetails_MapsUserRPMExceededToTooManyRequests supplies the
// user RPM limit error. It protects 429 classification and the positive seconds
// remaining within the current minute.
func TestBillingErrorDetails_MapsUserRPMExceededToTooManyRequests(t *testing.T) {
	status, code, msg, retryAfter := BillingErrorDetails(scheduler.ErrUserRPMExceeded)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "rate_limit_exceeded", code)
	require.NotEmpty(t, msg)
	require.Greater(t, retryAfter, 0, "RPM exceeded should return positive Retry-After")
	require.LessOrEqual(t, retryAfter, 60)
}

// TestBillingErrorDetails_APIKeyRateLimitStillMaps supplies all three Key
// rolling-window limit errors. Each must retain the shared rate-limit code and
// 429 status used for client retry handling.
func TestBillingErrorDetails_APIKeyRateLimitStillMaps(t *testing.T) {
	// API key window limits use the same rate-limit classification as RPM limits.
	for _, err := range []error{apikey.ErrAPIKeyRateLimit5hExceeded, apikey.ErrAPIKeyRateLimit1dExceeded, apikey.ErrAPIKeyRateLimit7dExceeded} {
		status, code, _, _ := BillingErrorDetails(err)
		require.Equal(t, http.StatusTooManyRequests, status, "status for %v", err)
		require.Equal(t, "rate_limit_exceeded", code)
	}
}

// TestBillingErrorDetails_BillingServiceUnavailableMapsTo503 supplies an
// unavailable funding-service error during request admission. It requires the
// service-error code and 503, with no RPM Retry-After value.
func TestBillingErrorDetails_BillingServiceUnavailableMapsTo503(t *testing.T) {
	status, code, _, retryAfter := BillingErrorDetails(billing.ErrBillingServiceUnavailable)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "billing_service_error", code)
	require.Equal(t, 0, retryAfter, "non-RPM errors should not set Retry-After")
}

// TestBillingErrorDetails_UnknownErrorFallsBackTo403 supplies insufficient
// balance outside the specialized service and rate cases. It requires the
// generic billing code, 403 status and a nonempty safe message.
func TestBillingErrorDetails_UnknownErrorFallsBackTo403(t *testing.T) {
	status, code, msg, _ := BillingErrorDetails(billing.ErrInsufficientBalance)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "billing_error", code)
	require.NotEmpty(t, msg)
}
