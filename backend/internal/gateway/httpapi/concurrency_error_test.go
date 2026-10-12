package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// A canceled client, configured concurrency timeout, and failed slot store need
// different error responses even when each prevents admission. These fixtures
// verify the classification tuple used by HTTP adapters, including typed slot
// names that take precedence over the caller's label.

// TestConcurrencyErrorResponse compares typed limit errors with cancellation,
// deadline, and store failures. Every case must produce the asserted status,
// error type, reason code, and client message for that admission cause.
func TestConcurrencyErrorResponse(t *testing.T) {
	tests := []struct {
		name     string // admission failure scenario
		err      error  // supplied failure cause
		slotType string // caller's slot label
		// wantStatus, wantType, wantCode, and wantMessage form the expected protocol
		// classification tuple. Empty codes distinguish cancellation and infrastructure failures from configured capacity rejections.
		wantStatus  int    // expected HTTP status
		wantType    string // expected protocol type
		wantCode    string // expected reason code
		wantMessage string // expected client message
	}{
		{
			name:        "true concurrency timeout remains rate limit",
			err:         &ConcurrencyError{SlotType: "provider", IsTimeout: true},
			slotType:    "user",
			wantStatus:  http.StatusTooManyRequests,
			wantType:    "rate_limit_error",
			wantCode:    GatewayConcurrencyLimitCode,
			wantMessage: "Concurrency limit exceeded for provider, please retry later",
		},
		{
			name:        "wait queue full is rate limit",
			err:         &WaitQueueFullError{SlotType: "user"},
			slotType:    "user",
			wantStatus:  http.StatusTooManyRequests,
			wantType:    "rate_limit_error",
			wantCode:    GatewayQueueFullCode,
			wantMessage: "Too many pending requests, please retry later",
		},
		{
			name:        "client cancellation is not classified as concurrency limit",
			err:         context.Canceled,
			slotType:    "user",
			wantStatus:  StatusClientClosedRequest,
			wantType:    "api_error",
			wantMessage: "context canceled",
		},
		{
			name:        "deadline exceeded is service unavailable",
			err:         context.DeadlineExceeded,
			slotType:    "user",
			wantStatus:  http.StatusServiceUnavailable,
			wantType:    "api_error",
			wantMessage: "Service temporarily unavailable, please retry later",
		},
		{
			name:        "redis acquire error is service unavailable",
			err:         errors.New("redis unavailable"),
			slotType:    "user",
			wantStatus:  http.StatusServiceUnavailable,
			wantType:    "api_error",
			wantMessage: "Service temporarily unavailable, please retry later",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, errType, code, message := ConcurrencyErrorResponse(tt.err, tt.slotType)
			require.Equal(t, tt.wantStatus, status)
			require.Equal(t, tt.wantType, errType)
			require.Equal(t, tt.wantCode, code)
			require.Equal(t, tt.wantMessage, message)
		})
	}
}
