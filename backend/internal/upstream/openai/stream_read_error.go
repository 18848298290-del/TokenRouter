package openai

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// stream_read_error.go classifies upstream stream interruptions and wraps
// their causes with stable client error details.
// Stream interruption after output begins can leave usage incomplete and still
// require a stable client error. Typed wrappers retain the internal cause while
// exposing fixed messages for HTTP/2 failure, ordinary interruption, or clean
// EOF before a terminal signal.
//
// Cancellation, caller deadlines, local body limits, and already-ended contexts
// are excluded from upstream transport classification. That distinction keeps
// local request termination from being attributed to provider health. The
// classifier recognizes HTTP/2 errors by text where the concrete transport
// error cannot be referenced directly, and callers can inspect the preserved
// cause through errors.Unwrap.

// Client codes distinguish HTTP/2 transport interruption, generic read failure,
// and clean truncation so downstream stream errors retain stable diagnostic categories.
const (
	// OpenAIUpstreamHTTP2StreamErrorCode identifies an upstream HTTP/2 stream failure after the request starts.
	OpenAIUpstreamHTTP2StreamErrorCode = "upstream_http2_stream_error"
	OpenAIUpstreamStreamReadErrorCode  = "upstream_stream_read_error"
	// OpenAIUpstreamStreamTruncatedCode identifies clean EOF before any SSE terminal signal.
	// A clean EOF carries no transport error, so it needs a separate failure classification.
	OpenAIUpstreamStreamTruncatedCode = "upstream_stream_truncated"
)

// ErrOpenAIUpstreamStreamTruncated supplies a failure cause for clean EOF before
// a terminal signal, which otherwise carries no transport error for response classification.
var ErrOpenAIUpstreamStreamTruncated = errors.New("upstream stream ended before any terminal chunk")

// UpstreamStreamReadError retains the cause and classified client details for
// interrupted output.
type UpstreamStreamReadError struct {
	cause         error  // underlying stream read failure
	clientCode    string // stable downstream error code
	clientMessage string // fixed downstream error message
}

// Error formats the retained cause with stream-usage interruption context.
func (e *UpstreamStreamReadError) Error() string {
	return fmt.Sprintf("stream usage incomplete: %v", e.cause)
}

// Unwrap returns the internal stream-read cause for error inspection.
func (e *UpstreamStreamReadError) Unwrap() error { return e.cause }

// NewUpstreamStreamReadError retains err and stores its classified
// client-facing code and message.
func NewUpstreamStreamReadError(err error) error {
	code, message := ClassifyUpstreamStreamReadError(err)
	return &UpstreamStreamReadError{
		cause:         err,
		clientCode:    code,
		clientMessage: message,
	}
}

// ShouldClassifyUpstreamStreamReadError reports whether a failure represents
// upstream transport interruption. Nil, cancellation, deadlines, local limit
// errors, and failures after any supplied context ends return false; callers
// apply health effects only to eligible failures.
func ShouldClassifyUpstreamStreamReadError(err error, localBodyLimit error, contexts ...context.Context) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, localBodyLimit) {
		return false
	}
	for _, ctx := range contexts {
		if ctx != nil && ctx.Err() != nil {
			return false
		}
	}
	return true
}

// OpenAIUpstreamStreamReadErrorDetails extracts fixed client details from a
// wrapped stream-read error. Unsupported or nil typed values return empty
// details and false, letting callers retain their ordinary error response path.
func OpenAIUpstreamStreamReadErrorDetails(err error) (code, message string, ok bool) {
	var streamErr *UpstreamStreamReadError
	if !errors.As(err, &streamErr) || streamErr == nil {
		return "", "", false
	}
	return streamErr.clientCode, streamErr.clientMessage, true
}

// ClassifyUpstreamStreamReadError selects fixed client details from a read
// cause. The truncation sentinel precedes HTTP/2 text recognition; nil or other
// causes receive the generic stream-interruption classification while wrappers
// can retain the internal error.
func ClassifyUpstreamStreamReadError(err error) (code, message string) {
	if err != nil {
		if errors.Is(err, ErrOpenAIUpstreamStreamTruncated) {
			return OpenAIUpstreamStreamTruncatedCode, "Upstream response stream ended before completion"
		}
		lower := strings.ToLower(err.Error())
		// net/http's HTTP/2 stream error type is unexported, so match its transport error text.
		// Return a fixed client message while retaining the original error internally.
		if strings.Contains(lower, "stream error: stream id ") ||
			(strings.Contains(lower, "http2:") && strings.Contains(lower, "stream")) {
			return OpenAIUpstreamHTTP2StreamErrorCode, "Upstream HTTP/2 stream failed"
		}
	}
	return OpenAIUpstreamStreamReadErrorCode, "Upstream response stream was interrupted"
}
