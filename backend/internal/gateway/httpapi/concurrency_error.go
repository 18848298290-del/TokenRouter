package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Slot admission can fail from a configured limit, a full queue, or an
// interrupted request. Typed scheduler errors retain rate-limit classification,
// while caller cancellation and unknown infrastructure failures receive distinct
// statuses so output adapters can report the observed cause.
//
// Classification follows typed queue and concurrency evidence before examining
// cancellation. A typed timeout remains a concurrency limit, while a plain
// deadline uses unavailable status because it supplies no configured-limit
// evidence. The error's nonempty slot label overrides the caller's label,
// retaining the scheduler's identified resource in the client message.

// Admission status and reason values distinguish caller cancellation from queue
// capacity and concurrency limits in protocol errors and access observations.
const (
	StatusClientClosedRequest = 499 // canceled request status

	GatewayQueueFullCode        = "gateway_queue_full"        // pending queue capacity reached
	GatewayConcurrencyLimitCode = "gateway_concurrency_limit" // active slot limit reached
)

// ConcurrencyErrorResponse returns status, type, code, and message for admission failure.
// Queue errors precede concurrency errors, which can override slotType from their
// nonempty typed field. context.Canceled maps to 499, and other errors, including
// a plain deadline or nil error, receive the unavailable response.
//
// Typed queue or concurrency evidence can appear in an error chain with other
// causes, so those checks run before cancellation. The typed concurrency slot
// identifies the scheduler's rejected resource; an empty field keeps the
// caller's slot label for that same protocol message.
func ConcurrencyErrorResponse(err error, slotType string) (int, string, string, string) {
	if _, ok := errors.AsType[*scheduler.WaitQueueFullError](err); ok {
		return http.StatusTooManyRequests, "rate_limit_error", GatewayQueueFullCode,
			"Too many pending requests, please retry later"
	}

	if concurrencyErr, ok := errors.AsType[*scheduler.ConcurrencyError](err); ok {
		if concurrencyErr.SlotType != "" {
			slotType = concurrencyErr.SlotType
		}
		return http.StatusTooManyRequests, "rate_limit_error", GatewayConcurrencyLimitCode,
			fmt.Sprintf("Concurrency limit exceeded for %s, please retry later", slotType)
	}

	if errors.Is(err, context.Canceled) {
		return StatusClientClosedRequest, "api_error", "", "context canceled"
	}

	// An unknown failure does not establish that a configured limit was reached.
	// Report service unavailability so transport and deadline failures retain that distinction for callers.
	return http.StatusServiceUnavailable, "api_error", "", "Service temporarily unavailable, please retry later"
}
