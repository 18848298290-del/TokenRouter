package requeststate

import (
	"context"
	"time"
)

// credential_budget.go shares one credential-acquisition deadline across a
// request's provider attempts. An unset deadline starts a fifteen-second
// allowance on first acquisition; later contexts reuse that deadline and remain
// subject to their parent deadlines.

// CredentialBudget carries one request-owned deadline reused by serial Grok
// credential acquisitions. Callers may supply an existing deadline before
// acquisition and serialize access to this mutable value.
type CredentialBudget struct {
	// Deadline is reused across serial acquisitions. Zero starts fifteen seconds
	// from the first acquisition; callers may install a deadline before that call.
	Deadline time.Time // shared credential acquisition deadline
}

// Acquire derives a context limited by the request's shared credential deadline
// and returns its cleanup function. A nil budget returns ctx unchanged with no
// cleanup; an expired budget reports true without deriving another context.
//
// The first serial call initializes Deadline to fifteen seconds from that call,
// and later attempts reuse it. Callers serialize budget access and call the
// returned cleanup when acquisition finishes. The expiration result refers to
// this shared deadline; parent cancellation remains visible through the derived
// context.
func (state *CredentialBudget) Acquire(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	if state == nil {
		return ctx, nil, false
	}
	deadline := state.Deadline
	if deadline.IsZero() {
		// Keep all Grok provider credential attempts inside one fifteen-second request
		// allowance, so switching accounts cannot restart the acquisition budget.
		deadline = time.Now().Add(15 * time.Second)
		state.Deadline = deadline
	}
	if !time.Now().Before(deadline) {
		return ctx, nil, true
	}
	acquireCtx, cancel := context.WithDeadline(ctx, deadline)
	return acquireCtx, cancel, false
}
