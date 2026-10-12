package requeststate

import (
	"context"
	"time"
)

// CredentialBudget shares the deadline from the first credential acquisition across provider attempts.
// A nil budget leaves the caller's context unchanged.
type CredentialBudget struct{ Deadline time.Time }

func (state *CredentialBudget) Acquire(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	if state == nil {
		return ctx, nil, false
	}
	deadline := state.Deadline
	if deadline.IsZero() {
		deadline = time.Now().Add(15 * time.Second)
		state.Deadline = deadline
	}
	if !time.Now().Before(deadline) {
		return ctx, nil, true
	}
	acquireCtx, cancel := context.WithDeadline(ctx, deadline)
	return acquireCtx, cancel, false
}
