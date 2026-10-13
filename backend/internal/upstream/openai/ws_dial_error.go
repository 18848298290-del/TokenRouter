package openai

import (
	"errors"
	"fmt"
	"net/http"
)

// WebSocket acquisition failures distinguish a full queue from an unavailable
// explicitly selected connection. Handshake failures retain HTTP status,
// headers, and captured body alongside the dial cause, allowing recovery code
// to inspect task and access evidence after the handshake response has been
// closed.

// These sentinels separate queue-capacity rejection from unavailable connection
// continuation so acquisition callers can select the appropriate WebSocket recovery path.
var (
	ErrOpenAIWSConnQueueFull            = errors.New("openai ws connection queue full")
	ErrOpenAIWSPreferredConnUnavailable = errors.New("openai ws preferred connection unavailable")
)

// WSDialError retains captured handshake evidence and the underlying connection
// failure.
type WSDialError struct {
	StatusCode      int         // captured handshake HTTP status
	ResponseHeaders http.Header // captured handshake response headers
	ResponseBody    []byte      // captured handshake body bytes
	Err             error       // underlying WebSocket dial failure
}

// Error formats the handshake failure status and retained cause for
// diagnostics. Nil receivers return empty text, and missing positive status
// uses the cause-only form when no HTTP status is available.
func (e *WSDialError) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("openai ws dial failed: status=%d err=%v", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("openai ws dial failed: %v", e.Err)
}

// Unwrap returns the retained dial cause or nil for nil receivers. Error
// inspection can therefore match underlying transport failures while callers
// still retain captured handshake fields on the wrapper.
func (e *WSDialError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
