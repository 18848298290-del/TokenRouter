package openai

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	// OpenAIUpstreamHTTP2StreamErrorCode identifies an upstream HTTP/2 stream failure after the request starts.
	OpenAIUpstreamHTTP2StreamErrorCode = "upstream_http2_stream_error"
	OpenAIUpstreamStreamReadErrorCode  = "upstream_stream_read_error"
	// OpenAIUpstreamStreamTruncatedCode identifies clean EOF before any SSE terminal signal.
	// A clean EOF carries no transport error, so it needs a separate failure classification.
	OpenAIUpstreamStreamTruncatedCode = "upstream_stream_truncated"
)

// ErrOpenAIUpstreamStreamTruncated marks EOF before an SSE terminal signal with no read error.
var ErrOpenAIUpstreamStreamTruncated = errors.New("upstream stream ended before any terminal chunk")

type UpstreamStreamReadError struct {
	cause         error
	clientCode    string
	clientMessage string
}

func (e *UpstreamStreamReadError) Error() string {
	return fmt.Sprintf("stream usage incomplete: %v", e.cause)
}

func (e *UpstreamStreamReadError) Unwrap() error { return e.cause }

func NewUpstreamStreamReadError(err error) error {
	code, message := ClassifyUpstreamStreamReadError(err)
	return &UpstreamStreamReadError{
		cause:         err,
		clientCode:    code,
		clientMessage: message,
	}
}

// ShouldClassifyUpstreamStreamReadError reports whether to classify a read failure as upstream transport failure.
// It excludes cancellation, deadlines, local body limits, and errors after any supplied context ends.
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

// OpenAIUpstreamStreamReadErrorDetails returns the stored client-facing code and message, if present.
func OpenAIUpstreamStreamReadErrorDetails(err error) (code, message string, ok bool) {
	var streamErr *UpstreamStreamReadError
	if !errors.As(err, &streamErr) || streamErr == nil {
		return "", "", false
	}
	return streamErr.clientCode, streamErr.clientMessage, true
}

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
