package httpapi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSIngressEndedByClient_BareNormalClosureIsNotProviderFailure(t *testing.T) {
	err := coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "client done"}

	// Classification must also accept the network library's close error type.
	var closeErr *OpenAIWSClientCloseError
	require.False(t, errors.As(err, &closeErr),
		"裸 coderws.CloseError 不是 *OpenAIWSClientCloseError，旧的 errors.As 必然为假")
	require.Equal(t, coderws.StatusNormalClosure, coderws.CloseStatus(err))

	require.True(t, ResponsesWSEndedByClient(err, ResponsesWSCloseInfo(err)))
}

func TestOpenAIWSIngressEndedByClient_WrappedBareNormalClosureIsNotProviderFailure(t *testing.T) {
	err := fmt.Errorf("ingress turn 3: %w",
		coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "client done"})

	require.True(t, ResponsesWSEndedByClient(err, ResponsesWSCloseInfo(err)))
}

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

func TestOpenAIWSIngressEndedByClient_GatewayNormalClosureStillRecognised(t *testing.T) {
	err := NewOpenAIWSClientCloseError(
		coderws.StatusNormalClosure, "websocket idle timeout", context.DeadlineExceeded)

	require.True(t, ResponsesWSEndedByClient(err, ResponsesWSCloseInfo(err)))
}
