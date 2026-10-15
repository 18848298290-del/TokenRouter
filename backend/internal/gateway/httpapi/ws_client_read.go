package httpapi

import (
	"context"
	"errors"
	"time"

	coderws "github.com/coder/websocket"

	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
)

// ws_client_read.go adapts WebSocket client reads and control-driven close
// errors to the gateway read controller.
// Client reads need a live transport reader while a control event delivers a
// close handshake. The gateway read controller owns timeout arming,
// cancellation and reader joining; the HTTP adapter translates its integer
// frame types and selected close instruction into coder/websocket values.

// ReadOpenAIWSClientMessage waits for one client frame using the gateway read
// controller. A positive timeout starts immediately; control cancellation or
// expiry delivers the chosen close instruction and joins the reader before
// returning its wrapped cause.
func ReadOpenAIWSClientMessage(ctx context.Context, conn *coderws.Conn, timeout time.Duration, status coderws.StatusCode, reason string) (coderws.MessageType, []byte, error) {
	return ReadOpenAIWSClientMessageWithTimeoutStart(ctx, conn, timeout, status, reason, nil, nil)
}

// ReadOpenAIWSClientMessageWithTimeoutStart adapts coder/websocket to
// gateway-controlled client reading. Nil connections return a read error, and
// nil contexts use Background. The activity callback gates timer arming and
// rearming from start notifications; control closure finishes the background
// read before this adapter translates its close error.
func ReadOpenAIWSClientMessageWithTimeoutStart(ctx context.Context, conn *coderws.Conn, timeout time.Duration, status coderws.StatusCode, reason string, start <-chan struct{}, active func() bool) (coderws.MessageType, []byte, error) {
	var input gatewayws.ClientConn
	if conn != nil {
		input = WSClientFrames{Conn: conn}
	}
	typ, body, err := gatewayws.ReadClientMessageWithTimeoutStart(ctx, input, timeout, int(status), reason, start, active)
	// Retain the selected close instruction and cause in the HTTP wrapper so
	// ingress can present closure and still match cancellation through errors.Is.
	if closeErr, ok := errors.AsType[*gatewayws.ClientCloseError](err); ok {
		err = NewOpenAIWSClientCloseError(coderws.StatusCode(closeErr.Status), closeErr.Reason, closeErr.Cause)
	}
	return coderws.MessageType(typ), body, err
}
