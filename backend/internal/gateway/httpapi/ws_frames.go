package httpapi

import (
	"context"

	coderws "github.com/coder/websocket"

	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
)

// Gateway WebSocket orchestration uses integer frame and close codes at its
// transport interface. The coder/websocket adapter converts those values while
// preserving synchronous read, write and close results, so the controller can
// decide cancellation and cleanup using the returned errors.

// The interface assertion checks that the transport adapter covers ingress
// frame writes and close operations in addition to controlled client reads.
var _ gatewayws.ClientSocket = WSClientFrames{}

// WSClientFrames adapts coder/websocket frame and close enums to gateway integers.
type WSClientFrames struct {
	Conn *coderws.Conn // required bound WebSocket connection
}

// Read waits synchronously for a frame from the bound WebSocket. The connection
// is required, and cancellation and received close evidence are returned from
// coder/websocket with the frame type converted to a gateway integer.
func (c WSClientFrames) Read(ctx context.Context) (int, []byte, error) {
	typ, body, err := c.Conn.Read(ctx)
	return int(typ), body, err
}

// Write sends a frame synchronously through the bound WebSocket connection. It
// returns the transport result so ingress can handle cancellation or closure;
// the caller retains ownership of the supplied payload.
func (c WSClientFrames) Write(ctx context.Context, typ int, body []byte) error {
	return c.Conn.Write(ctx, coderws.MessageType(typ), body)
}

// Close delegates the close handshake with the supplied gateway status and
// reason. The bound connection is required; coder/websocket completes its
// transport shutdown after the handshake and exposes any failure to the caller.
func (c WSClientFrames) Close(status int, reason string) error {
	return c.Conn.Close(coderws.StatusCode(status), reason)
}

// CloseNow terminates the bound transport and waits for library goroutines to
// finish. It requires a connection and returns the library result, allowing the
// gateway read controller to join an outstanding frame read.
func (c WSClientFrames) CloseNow() error { return c.Conn.CloseNow() }
