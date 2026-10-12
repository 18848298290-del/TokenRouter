package provider

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

func TestQoderGatewayErrorDetailsUsesAPIErrorMessage(t *testing.T) {
	err := fmt.Errorf("forward qoder: %w", &qoder.APIError{
		StatusCode: http.StatusForbidden,
		Code:       "101",
		Message:    "Signature invalid",
	})

	status, errType, message, ok := qoderErrorDetailsForContract(err)
	require.True(t, ok)
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "upstream_error", errType)
	require.Equal(t, "Qoder upstream error 101: Signature invalid", message)
}

func TestQoderGatewayErrorDetailsKeepsEntitlementDeniedAsForbidden(t *testing.T) {
	status, errType, message, ok := qoderErrorDetailsForContract(&qoder.APIError{
		StatusCode: http.StatusForbidden,
		Code:       "112",
		Message:    "model not available for this provider",
	})

	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "upstream_error", errType)
	require.Equal(t, "Qoder upstream error 112: model not available for this provider", message)
}

func TestQoderGatewayErrorDetailsMapsRateLimit(t *testing.T) {
	status, errType, message, ok := qoderErrorDetailsForContract(&qoder.APIError{
		StatusCode: http.StatusTooManyRequests,
		Message:    "Too many requests",
	})

	require.True(t, ok)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "rate_limit_error", errType)
	require.Equal(t, "Too many requests", message)
}

func TestQoderGatewayErrorDetailsMapsAgentLimitToRateLimit(t *testing.T) {
	status, errType, message, ok := qoderErrorDetailsForContract(&qoder.APIError{
		StatusCode:          http.StatusBadGateway,
		Code:                "115",
		Message:             `{"agentLimitResetTime":1783841289162}`,
		AgentLimitResetTime: 1783841289162,
	})

	require.True(t, ok)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "rate_limit_error", errType)
	require.Equal(t, "Qoder agent limit reached; resets at 2026-07-12 15:28:09 Asia/Shanghai", message)
}

// qoderErrorDetailsForContract returns the status, kind, message, and recognition flag for assertions.
func qoderErrorDetailsForContract(err error) (int, string, string, bool) {
	value := DescribeQoderError(err)
	return value.Status, value.Kind, value.Message, value.Recognized
}
