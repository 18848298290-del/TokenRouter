package upstream

import "context"

// frame.go defines the connection interface and message kinds used to relay
// realtime media frames.
// Realtime media exchanges carry text and binary messages through a platform-neutral
// connection. The message kind retains the WebSocket wire value while
// each implementation owns how request cancellation and connection closure
// affect its frame operations.

// These message kinds match WebSocket data opcodes so realtime adapters can
// relay text and binary payloads.
const (
	FrameText   FrameKind = 1 // UTF-8 text message
	FrameBinary FrameKind = 2 // binary message payload
)

// FrameKind identifies the wire message format of one realtime frame.
type FrameKind int

// FrameConn exchanges realtime message payloads through a platform-owned
// connection implementation.
type FrameConn interface {
	// ReadFrame reads one message kind and payload using the operation context.
	// Implementations define their cancellation and payload ownership rules;
	// callers handle read errors before interpreting the returned message.
	ReadFrame(context.Context) (FrameKind, []byte, error)
	// WriteFrame sends one payload with its wire message kind and operation
	// context. Callers handle the returned error before assuming delivery and
	// follow the implementation's concurrent-write requirements.
	WriteFrame(context.Context, FrameKind, []byte) error
	// Close releases the connection resources owned by the frame implementation.
	// Callers arrange closure when relaying ends and use the concrete implementation's
	// rules for concurrent operations and repeated closes.
	Close() error
}
