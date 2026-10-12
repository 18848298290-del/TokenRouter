package ws

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const IngressStagePreviousResponseNotFound = "previous_response_not_found"

// FallbackError marks a WS failure eligible for HTTP fallback before downstream output.
type FallbackError struct {
	Reason string
	Err    error
}

type IngressTurnError struct {
	stage           string
	cause           error
	wroteDownstream bool
}

// CurrentTurnFailoverError carries the current request for replay on a replacement provider.
type CurrentTurnFailoverError struct {
	cause        error
	retryPayload []byte
}

func (e *FallbackError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return fmt.Sprintf("openai ws fallback: %s", strings.TrimSpace(e.Reason))
	}
	return fmt.Sprintf("openai ws fallback: %s: %v", strings.TrimSpace(e.Reason), e.Err)
}

func (e *FallbackError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func WrapFallback(reason string, err error) error {
	return &FallbackError{Reason: strings.TrimSpace(reason), Err: err}
}

func (e *CurrentTurnFailoverError) Error() string {
	if e == nil || e.cause == nil {
		return "openai websocket current-turn failover"
	}
	return e.cause.Error()
}

func (e *CurrentTurnFailoverError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func NewCurrentTurnFailoverError(cause error, retryPayload []byte) error {
	return &CurrentTurnFailoverError{
		cause:        cause,
		retryPayload: append([]byte(nil), retryPayload...),
	}
}

// CurrentTurnRetryPayload returns an owned copy of the current turn's provider-failover payload.
func CurrentTurnRetryPayload(err error) ([]byte, bool) {
	var retryErr *CurrentTurnFailoverError
	if !errors.As(err, &retryErr) || retryErr == nil {
		return nil, false
	}
	return append([]byte(nil), retryErr.retryPayload...), true
}

func (e *IngressTurnError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause == nil {
		return strings.TrimSpace(e.stage)
	}
	return e.cause.Error()
}

func (e *IngressTurnError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func WrapIngressTurnError(stage string, cause error, wroteDownstream bool) error {
	if cause == nil {
		return nil
	}
	return &IngressTurnError{
		stage:           strings.TrimSpace(stage),
		cause:           cause,
		wroteDownstream: wroteDownstream,
	}
}

func IsIngressTurnRetryable(err error) bool {
	var turnErr *IngressTurnError
	if !errors.As(err, &turnErr) || turnErr == nil {
		return false
	}
	if errors.Is(turnErr.cause, context.Canceled) || errors.Is(turnErr.cause, context.DeadlineExceeded) {
		return false
	}
	if turnErr.wroteDownstream {
		return false
	}
	switch turnErr.stage {
	case "write_upstream", "read_upstream":
		return true
	default:
		return false
	}
}

func IngressTurnRetryReason(err error) string {
	var turnErr *IngressTurnError
	if !errors.As(err, &turnErr) || turnErr == nil {
		return "unknown"
	}
	if turnErr.stage == "" {
		return "unknown"
	}
	return turnErr.stage
}

func IsPreviousResponseNotFound(err error) bool {
	var turnErr *IngressTurnError
	if !errors.As(err, &turnErr) || turnErr == nil {
		return false
	}
	if strings.TrimSpace(turnErr.stage) != IngressStagePreviousResponseNotFound {
		return false
	}
	return !turnErr.wroteDownstream
}
