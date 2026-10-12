package httpapi

// Close-attribution scenarios check HTTP close classification against WS provider-failure reporting.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
)

// TestOpenAIWSIngressEndedByClient_GoingAwayWithoutCancellationStillReported checks provider reporting for an upstream-initiated close.
func TestOpenAIWSIngressEndedByClient_GoingAwayWithoutCancellationStillReported(t *testing.T) {
	err := gatewayhttp.NewOpenAIWSClientCloseError(
		coderws.StatusGoingAway, "upstream going away", errors.New("upstream closed session"))

	require.False(t, gatewayhttp.ResponsesWSEndedByClient(err, gatewayhttp.ResponsesWSCloseInfo(err)))
	require.True(t, gatewayws.EntryShouldReportFailure(err), "真实上游故障仍须归因提供商")
}

// TestOpenAIWSIngressEndedByClient_AbnormalClosuresStillReportProviderFailure checks abnormal-close attribution.
func TestOpenAIWSIngressEndedByClient_AbnormalClosuresStillReportProviderFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "upstream_policy_violation",
			err: gatewayhttp.NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation, "upstream websocket authentication failed",
				errors.New("upstream rejected credentials")),
		},
		{
			name: "upstream_internal_error",
			err: gatewayhttp.NewOpenAIWSClientCloseError(
				coderws.StatusInternalError, "upstream websocket proxy failed", nil),
		},
		{
			name: "bare_abnormal_closure",
			err:  coderws.CloseError{Code: coderws.StatusAbnormalClosure, Reason: "connection reset"},
		},
		{
			name: "generic_read_failure",
			err:  errors.New("upstream websocket read failed"),
		},
		{
			// A deadline outside the inter-turn idle timeout counts as a stalled request.
			name: "deadline_without_normal_close",
			err:  fmt.Errorf("upstream stalled: %w", context.DeadlineExceeded),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, gatewayhttp.ResponsesWSEndedByClient(tc.err, gatewayhttp.ResponsesWSCloseInfo(tc.err)))
			require.True(t, gatewayws.EntryShouldReportFailure(tc.err))
		})
	}
}
