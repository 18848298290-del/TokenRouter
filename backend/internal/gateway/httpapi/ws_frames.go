package httpapi

import (
	"context"

	coderws "github.com/coder/websocket"

	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
)

var _ gatewayws.ClientSocket = WSClientFrames{}

// WSClientFrames adapts coder/websocket frame and close enums to gateway integers.
type WSClientFrames struct{ Conn *coderws.Conn }

func (c WSClientFrames) Read(ctx context.Context) (int, []byte, error) {
	typ, body, err := c.Conn.Read(ctx)
	return int(typ), body, err
}

// Write returns the network write result synchronously so the ingress executor
// can handle connection closure and cancellation.
func (c WSClientFrames) Write(ctx context.Context, typ int, body []byte) error {
	return c.Conn.Write(ctx, coderws.MessageType(typ), body)
}

func (c WSClientFrames) Close(status int, reason string) error {
	return c.Conn.Close(coderws.StatusCode(status), reason)
}
func (c WSClientFrames) CloseNow() error { return c.Conn.CloseNow() }
