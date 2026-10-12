package httpapi

import (
	"context"
	"errors"
	"time"

	coderws "github.com/coder/websocket"

	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
)

// ReadOpenAIWSClientMessage reads through the gateway client loop and converts client close-frame errors.
func ReadOpenAIWSClientMessage(ctx context.Context, conn *coderws.Conn, timeout time.Duration, status coderws.StatusCode, reason string) (coderws.MessageType, []byte, error) {
	return ReadOpenAIWSClientMessageWithTimeoutStart(ctx, conn, timeout, status, reason, nil, nil)
}

func ReadOpenAIWSClientMessageWithTimeoutStart(ctx context.Context, conn *coderws.Conn, timeout time.Duration, status coderws.StatusCode, reason string, start <-chan struct{}, active func() bool) (coderws.MessageType, []byte, error) {
	var input gatewayws.ClientConn
	if conn != nil {
		input = WSClientFrames{Conn: conn}
	}
	typ, body, err := gatewayws.ReadClientMessageWithTimeoutStart(ctx, input, timeout, int(status), reason, start, active)
	if closeErr, ok := errors.AsType[*gatewayws.ClientCloseError](err); ok {
		err = NewOpenAIWSClientCloseError(coderws.StatusCode(closeErr.Status), closeErr.Reason, closeErr.Cause)
	}
	return coderws.MessageType(typ), body, err
}
