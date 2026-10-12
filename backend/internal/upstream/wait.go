package upstream

import (
	"context"
	"time"
)

// A retry delay needs to finish either when its timer fires or when the
// request is cancelled. Nonpositive delays finish immediately, before
// consulting the context, so callers can use the same helper for an already-resolved
// wait. Positive delays select between the timer and the supplied
// cancellation signal; simultaneous readiness has no fixed priority.
//
// Timer cleanup runs after either selected outcome. Stopping the timer and
// attempting a nonblocking drain handles the timer having already fired or
// having been consumed by the selected timer case. The helper owns this timer
// for the duration of one call; retry selection and the reason for waiting
// remain with the platform caller.

// WaitContext waits for a positive duration or returns the selected context
// error when cancellation wins. Nonpositive durations return nil before
// accessing ctx, while positive waits require a nonnil context.
//
// Timer and cancellation can become ready together; select determines which
// result is returned, so a ready cancellation does not guarantee an error
// after the timer fires. Cleanup stops the private timer and drains any
// immediately available value before returning from either path.
func WaitContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	// Either wait outcome releases this private timer. A failed stop can mean
	// its value was already selected, so drain without blocking during cleanup.
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	// Request cancellation ends a pending retry delay. Selecting directly from
	// both signals leaves simultaneous timer readiness subject to select choice.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
