package httpapi

import (
	"fmt"
	"strings"

	coderws "github.com/coder/websocket"
)

// ws_close_error.go represents a selected WebSocket close instruction together
// with its underlying termination cause.
// Control-driven WebSocket termination carries a chosen status and reason
// alongside its underlying cancellation or timeout error. Keeping the
// instruction separate lets callers present closure and still use wrapped-error
// matching for the cause that ended the read.

// OpenAIWSClientCloseError requests that the client connection close with the
// supplied WebSocket status and reason, while retaining the underlying error.
type OpenAIWSClientCloseError struct {
	statusCode coderws.StatusCode // selected client close code
	reason     string             // stored close instruction text
	err        error              // wrapped control termination cause
}

// NewOpenAIWSClientCloseError constructs a client close instruction retaining
// the supplied cause. It trims reason and returns an error wrapper; the reader
// controller or caller performs the actual connection closure separately.
func NewOpenAIWSClientCloseError(statusCode coderws.StatusCode, reason string, err error) error {
	return &OpenAIWSClientCloseError{
		statusCode: statusCode,
		reason:     strings.TrimSpace(reason),
		err:        err,
	}
}

// Error formats the close instruction and optional cause for diagnostics. A nil
// receiver returns empty; an absent cause still retains the numeric status and
// trimmed reason in its message.
func (e *OpenAIWSClientCloseError) Error() string {
	if e == nil {
		return ""
	}
	if e.err == nil {
		return fmt.Sprintf("openai ws client close: %d %s", int(e.statusCode), strings.TrimSpace(e.reason))
	}
	return fmt.Sprintf("openai ws client close: %d %s: %v", int(e.statusCode), strings.TrimSpace(e.reason), e.err)
}

// Unwrap returns the underlying cause for wrapped-error matching by callers. A
// nil receiver or absent cause returns nil while the stored status and reason
// remain separate close-instruction data.
func (e *OpenAIWSClientCloseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// StatusCode returns the selected client WebSocket close code for presentation.
// A nil receiver uses StatusInternalError so the caller has a defined error
// close instruction when no wrapper is available.
func (e *OpenAIWSClientCloseError) StatusCode() coderws.StatusCode {
	if e == nil {
		return coderws.StatusInternalError
	}
	return e.statusCode
}

// Reason returns the trimmed client close instruction text from this wrapper. A
// nil receiver returns empty, allowing callers to inspect missing close
// evidence without dereferencing the wrapper.
func (e *OpenAIWSClientCloseError) Reason() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.reason)
}
