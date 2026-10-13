package httpapi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// TestOpenAIWSIngressEndedByClient_BareNormalClosureIsNotProviderFailure
// supplies the network library's unwrapped normal-close error. It requires
// client-end attribution even though the error is not a gateway close wrapper.
func TestOpenAIWSIngressEndedByClient_BareNormalClosureIsNotProviderFailure(t *testing.T) {
	err := coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "client done"}

	// Classification must also accept the network library's close error type.
	var closeErr *OpenAIWSClientCloseError
	require.False(t, errors.As(err, &closeErr),
		"裸 coderws.CloseError 不是 *OpenAIWSClientCloseError，旧的 errors.As 必然为假")
	require.Equal(t, coderws.StatusNormalClosure, coderws.CloseStatus(err))

	require.True(t, ResponsesWSEndedByClient(err, ResponsesWSCloseInfo(err)))
}

// TestOpenAIWSIngressEndedByClient_WrappedBareNormalClosureIsNotProviderFailure
// wraps a library normal-close error with turn context. Client-end attribution
// must still recognize the normal status through the error chain.
func TestOpenAIWSIngressEndedByClient_WrappedBareNormalClosureIsNotProviderFailure(t *testing.T) {
	err := fmt.Errorf("ingress turn 3: %w",
		coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "client done"})

	require.True(t, ResponsesWSEndedByClient(err, ResponsesWSCloseInfo(err)))
}

// TestOpenAIWSIngressEndedByClient_ClientCancelDuringTurnIsNotProviderFailure
// wraps cancellation with a going-away close code. It requires client-end
// attribution from the cause even though the selected code differs from normal
// closure.
func TestOpenAIWSIngressEndedByClient_ClientCancelDuringTurnIsNotProviderFailure(t *testing.T) {
	err := NewOpenAIWSClientCloseError(
		coderws.StatusGoingAway, "websocket request canceled", context.Canceled)

	// Cancellation counts as a client end even with status 1001 rather than 1000.
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusGoingAway, closeErr.StatusCode())
	require.NotEqual(t, coderws.StatusNormalClosure, closeErr.StatusCode())

	require.True(t, ResponsesWSEndedByClient(err, ResponsesWSCloseInfo(err)))
}

// TestOpenAIWSIngressEndedByClient_GatewayNormalClosureStillRecognised wraps
// deadline expiry with a normal idle close instruction. The explicit normal
// status keeps this gateway control result attributed to the client.
func TestOpenAIWSIngressEndedByClient_GatewayNormalClosureStillRecognised(t *testing.T) {
	err := NewOpenAIWSClientCloseError(
		coderws.StatusNormalClosure, "websocket idle timeout", context.DeadlineExceeded)

	require.True(t, ResponsesWSEndedByClient(err, ResponsesWSCloseInfo(err)))
}
