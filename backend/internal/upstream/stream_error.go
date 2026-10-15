package upstream

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
)

// stream_error.go reduces upstream stream failures to client-facing error
// labels.
// Network error strings can include the local address and the upstream
// destination in a client-facing disconnect message. Known causes are reduced
// to stable labels, and unrecognized errors receive a generic category while
// callers retain the original error for diagnostics.

// SanitizeStreamError returns a client-facing category for a stream failure,
// or empty for nil. Known causes are matched through wrapping before
// net.OpError timeout or operation labels; other errors receive a generic
// connection label. Callers can log the original error separately. A
// net.OpError operation string is carried through verbatim, so callers
// constructing such errors supply an operation label suitable for client
// output.
func SanitizeStreamError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected EOF"
	case errors.Is(err, io.EOF):
		return "EOF"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset by peer"
	case errors.Is(err, syscall.ECONNABORTED):
		return "connection aborted"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "connection timed out"
	case errors.Is(err, syscall.EPIPE):
		return "broken pipe"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	}
	if netErr, ok := errors.AsType[*net.OpError](err); ok {
		if netErr.Timeout() {
			if netErr.Op != "" {
				return netErr.Op + " timeout"
			}
			return "i/o timeout"
		}
		if netErr.Op != "" {
			return netErr.Op + " network error"
		}
	}
	// Arbitrary error strings can expose connection details. Use one generic
	// client label when neither a known cause nor a usable network operation applies.
	return "upstream connection error"
}
