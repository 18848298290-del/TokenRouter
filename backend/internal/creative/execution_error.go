package creative

import (
	"errors"
	"fmt"
)

// CreativeUpstreamError describes an upstream execution failure and whether the worker may retry.
type CreativeUpstreamError struct {
	Code       string
	StatusCode int
	Message    string
	Retryable  bool
}

// CreativeImageResultError marks result retrieval failure as non-retryable to prevent repeated generation.
func CreativeImageResultError(code, message string) *CreativeUpstreamError {
	return &CreativeUpstreamError{Code: code, Message: message, Retryable: false}
}

func (e *CreativeUpstreamError) Error() string {
	if e == nil {
		return "creative upstream error"
	}
	return fmt.Sprintf("creative upstream error status=%d: %s", e.StatusCode, e.Message)
}

// CreativeNonRetryableError creates an execution error that the worker must not retry.
func CreativeNonRetryableError(format string, args ...any) *CreativeUpstreamError {
	return &CreativeUpstreamError{StatusCode: 0, Message: fmt.Sprintf(format, args...), Retryable: false}
}

// CreativeHTTPStatusError marks status 0, 429, and codes of at least 500 as retryable.
func CreativeHTTPStatusError(statusCode int, message string) *CreativeUpstreamError {
	retryable := statusCode == 0 || statusCode == 429 || statusCode >= 500
	// Upstream bodies may contain prompts, internal URLs, or credentials; expose a fixed status-class message.
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

// IsRetryableCreativeError reports whether an error permits a bounded worker retry.
func IsRetryableCreativeError(err error) bool {
	if upstreamErr, ok := errors.AsType[*CreativeUpstreamError](err); ok {
		return upstreamErr.Retryable
	}
	// Unknown errors, including transport and serialization failures, rely on the worker retry limit.
	return err != nil
}
