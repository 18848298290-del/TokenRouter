package creative

import (
	"errors"
	"fmt"
)

// execution_error.go classifies creative execution failures for public
// messages and worker retry decisions.
// Creative execution failures carry a public message and a worker retry
// decision. HTTP failures replace supplied upstream text with status-class
// messages, while image retrieval failures end generation attempts so a failed
// download cannot request another image.

// CreativeUpstreamError describes an upstream execution failure and whether the
// worker may retry.
type CreativeUpstreamError struct {
	// Code overrides the worker status-derived code when nonempty, identifying
	// image retrieval failures separately from a failed generation request.
	Code string // optional persisted execution code
	// StatusCode retains the supplied upstream status. Zero identifies failures
	// without a response status; Retryable independently controls the worker.
	StatusCode int    // upstream HTTP status number
	Message    string // caller supplied public message
	// Retryable permits another execution attempt subject to the worker attempt
	// limit; a false value sends the task toward funding release.
	Retryable bool // worker retry permission flag
}

// CreativeImageResultError identifies a failed image retrieval after the
// generation response. The worker terminates this attempt using code and
// message as supplied, so callers provide text suitable for public task
// metadata.
func CreativeImageResultError(code, message string) *CreativeUpstreamError {
	return &CreativeUpstreamError{Code: code, Message: message, Retryable: false}
}

// Error formats the status and public message for an execution failure. A nil
// receiver returns the fixed creative upstream error text, allowing logging of
// that receiver.
func (e *CreativeUpstreamError) Error() string {
	if e == nil {
		return "creative upstream error"
	}
	return fmt.Sprintf("creative upstream error status=%d: %s", e.StatusCode, e.Message)
}

// CreativeNonRetryableError formats a failure that stops worker execution
// retries. It retains the supplied formatted message, so callers exclude
// credentials and private upstream details before constructing the error.
func CreativeNonRetryableError(format string, args ...any) *CreativeUpstreamError {
	return &CreativeUpstreamError{StatusCode: 0, Message: fmt.Sprintf(format, args...), Retryable: false}
}

// CreativeHTTPStatusError classifies connection, rate-limit, and server
// failures for worker retries. Status zero, 429, and values at least 500
// permit retries; message is discarded in favor of fixed public status-class
// text.
func CreativeHTTPStatusError(statusCode int, message string) *CreativeUpstreamError {
	retryable := statusCode == 0 || statusCode == 429 || statusCode >= 500
	// Worker error metadata reaches the user API. Discard the supplied upstream
	// message here so prompts, private addresses, or credentials in that text are
	// replaced by the public status-class description.
	publicMessage := "creative platform request failed"
	switch {
	case statusCode == 0:
		publicMessage = "creative platform connection failed"
	case statusCode == 429:
		publicMessage = "creative platform rate limited"
	case statusCode >= 500:
		publicMessage = "creative platform unavailable"
	case statusCode >= 400:
		publicMessage = "creative platform rejected request"
	}
	return &CreativeUpstreamError{StatusCode: statusCode, Message: publicMessage, Retryable: retryable}
}

// IsRetryableCreativeError returns the embedded retry decision when err wraps
// CreativeUpstreamError. Every other nonnil error permits the worker to apply
// its attempt limit; nil reports false.
//
// Callers represent no error with a nil interface and keep matched upstream
// error pointers nonnil. A typed nil upstream error matches errors.AsType but
// panics when this method reads its Retryable field.
func IsRetryableCreativeError(err error) bool {
	if upstreamErr, ok := errors.AsType[*CreativeUpstreamError](err); ok {
		return upstreamErr.Retryable
	}
	// Generic transport and serialization errors carry no explicit retry decision.
	// Let the worker count these failures and stop them at its configured attempt
	// limit before moving the task to funding release recovery.
	return err != nil
}
