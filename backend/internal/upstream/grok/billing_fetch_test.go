package grok

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIsRetryableGrokBillingStatus compares transient gateway statuses with
// authentication, entitlement, and throttling statuses. It asserts that 502,
// 503, and 504 are retryable while the other listed statuses are not.
func TestIsRetryableGrokBillingStatus(t *testing.T) {
	tests := []struct {
		status int  // upstream response HTTP status
		want   bool // expected billing retry decision
	}{
		{status: http.StatusBadGateway, want: true},
		{status: http.StatusServiceUnavailable, want: true},
		{status: http.StatusGatewayTimeout, want: true},
		{status: http.StatusUnauthorized, want: false},
		{status: http.StatusForbidden, want: false},
		{status: http.StatusTooManyRequests, want: false},
	}

	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.status), func(t *testing.T) {
			require.Equal(t, tt.want, IsRetryableBillingStatus(tt.status))
		})
	}
}
