package ws

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// A pooled turn can fail before output, after output, or because its
// continuation response disappeared. The stored stage and downstream-write flag
// let ingress separate reconnectable read/write failures from cancellation and
// from the one permitted missing-response recovery.
//
// Provider failover also needs the request for the current logical turn.
// CurrentTurnFailoverError copies that replay payload at construction and again
// when extracted, so a replacement provider cannot accidentally receive the
// session's first request or later mutations of a retry buffer. Error wrappers
// retain causes for errors.Is and errors.As, while the missing-response
// classifier additionally requires that no downstream frame has been written.

// IngressStagePreviousResponseNotFound identifies the continuation lookup
// failure used by ingress to select its bounded replay path before any
// downstream output.
const IngressStagePreviousResponseNotFound = "previous_response_not_found"

// FallbackError labels a WebSocket attempt failure for transport fallback
// classification.
type FallbackError struct {
	Reason string // transport fallback classification label
	Err    error  // wrapped attempt failure cause
}

// IngressTurnError retains the failed I/O stage and turn output state.
type IngressTurnError struct {
	// stage and wroteDownstream jointly govern replay eligibility. Only
	// write_upstream or read_upstream failures before output can use the generic
	// turn retry path.
	stage           string // failed turn operation label
	cause           error  // underlying turn failure cause
	wroteDownstream bool   // successful downstream output observed
}

// CurrentTurnFailoverError carries a replacement-provider payload for the
// failed logical turn.
type CurrentTurnFailoverError struct {
	cause error // underlying provider failover cause
	// retryPayload is copied at construction and extraction. An empty payload
	// still identifies a current-turn failure, but EntryNextAttemptMessage rejects
	// it for provider replay.
	retryPayload []byte // current turn replay bytes
}

// Error formats the trimmed fallback reason and available cause for
// diagnostics. A nil receiver returns empty, and an absent cause leaves the
// reason alone in the fallback label.
func (e *FallbackError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return fmt.Sprintf("openai ws fallback: %s", strings.TrimSpace(e.Reason))
	}
	return fmt.Sprintf("openai ws fallback: %s: %v", strings.TrimSpace(e.Reason), e.Err)
}

// Unwrap exposes the underlying transport-fallback cause to standard
// error-chain inspection. A nil receiver or absent Err returns nil; the
// fallback reason remains available on the wrapper itself.
func (e *FallbackError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// WrapFallback retains err with a trimmed fallback reason for later transport
// classification. It creates a wrapper even when err is nil, so reason-based
// preemption recognition can still inspect the label.
func WrapFallback(reason string, err error) error {
	return &FallbackError{Reason: strings.TrimSpace(reason), Err: err}
}

// Error returns the underlying current-turn cause text for entry diagnostics. A
// nil receiver or absent cause returns the generic failover label; replay bytes
// remain available through CurrentTurnRetryPayload.
func (e *CurrentTurnFailoverError) Error() string {
	if e == nil || e.cause == nil {
		return "openai websocket current-turn failover"
	}
	return e.cause.Error()
}

// Unwrap exposes the underlying current-turn cause for errors.Is and errors.As.
// A nil receiver returns nil; callers extract replay bytes separately to
// distinguish recognized later-turn failures from initial-attempt failures.
func (e *CurrentTurnFailoverError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// NewCurrentTurnFailoverError retains cause and a copied current-turn replay
// payload. Empty bytes still produce a wrapper; RunEntry treats a recognized
// wrapper with no payload as unsafe to replay.
func NewCurrentTurnFailoverError(cause error, retryPayload []byte) error {
	return &CurrentTurnFailoverError{
		cause:        cause,
		retryPayload: append([]byte(nil), retryPayload...),
	}
}

// CurrentTurnRetryPayload extracts a copied replay payload from a recognized
// failover wrapper. Its boolean reports wrapper recognition even when the
// stored bytes are empty; callers separately decide whether replay is safe.
func CurrentTurnRetryPayload(err error) ([]byte, bool) {
	var retryErr *CurrentTurnFailoverError
	if !errors.As(err, &retryErr) || retryErr == nil {
		return nil, false
	}
	return append([]byte(nil), retryErr.retryPayload...), true
}

// Error returns the underlying I/O failure text for diagnostics. A nil receiver
// returns empty, and an absent cause returns the trimmed stage used by ingress
// recovery classification.
func (e *IngressTurnError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause == nil {
		return strings.TrimSpace(e.stage)
	}
	return e.cause.Error()
}

// Unwrap exposes the stored I/O failure to standard error-chain inspection. A
// nil receiver returns nil; the wrapper separately retains stage and
// downstream-write state for ingress retry decisions.
func (e *IngressTurnError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// WrapIngressTurnError records stage and downstream output alongside a nonnil
// cause. A nil cause returns nil; callers set wroteDownstream from successful
// client writes so ingress can decide whether replay is permitted.
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

// IsIngressTurnRetryable reports whether a wrapped upstream read or write
// failed before output. Cancellation and deadline causes return false; the
// caller enforces the separate one-retry budget and strict-affinity
// restriction.
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

// IngressTurnRetryReason returns the stored failure stage used in retry
// diagnostics. An unrecognized wrapper or empty stage returns unknown; that
// label alone does not establish eligibility for replay.
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

// IsPreviousResponseNotFound recognizes the dedicated missing-continuation
// stage before downstream output. It does not inspect the cause for
// cancellation; IngressSession applies its recovery setting, per-turn budget,
// and tool-context restrictions separately.
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
