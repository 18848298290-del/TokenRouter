package httpapi

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

// A WS going-away status can describe either client termination or an upstream
// session failure depending on its cause. These scenarios combine close
// presentation with provider-reporting checks so a status alone does not hide
// stalled or abnormal upstream work.

// TestOpenAIWSIngressEndedByClient_GoingAwayWithoutCancellationStillReported
// wraps an upstream-going-away cause without a canceled context. The close
// remains an upstream failure: client-ending classification is false and
// provider reporting is true.
func TestOpenAIWSIngressEndedByClient_GoingAwayWithoutCancellationStillReported(t *testing.T) {
	err := gatewayhttp.NewOpenAIWSClientCloseError(
		coderws.StatusGoingAway, "upstream going away", errors.New("upstream closed session"))

	require.False(t, gatewayhttp.ResponsesWSEndedByClient(err, gatewayhttp.ResponsesWSCloseInfo(err)))
	require.True(t, gatewayws.EntryShouldReportFailure(err), "真实上游故障仍须归因提供商")
}

// TestOpenAIWSIngressEndedByClient_AbnormalClosuresStillReportProviderFailure
// compares policy, internal, abnormal, generic-read and untyped deadline
// failures. Each remains outside clean client termination and continues to
// count as provider failure.
func TestOpenAIWSIngressEndedByClient_AbnormalClosuresStillReportProviderFailure(t *testing.T) {
	cases := []struct {
		name string // failure scenario diagnostic label
		err  error  // wrapped upstream failure input
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
			// This deadline has no normal inter-turn close wrapper, so classification
			// retains an upstream stall rather than treating the session as clean client exit.
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
