package scheduler

import (
	"context"
	"time"
)

// Idle session capacity survives individual successful requests on the same
// Anthropic provider. Admission refreshes or registers activity, while final
// attempt outcome decides whether to retain that idle registration or remove
// it after unserved work.

// SessionBinding holds the provider session and idle-capacity settings used by
// an attempt.
type SessionBinding struct {
	ProviderID int64 // session capacity provider
	// SessionID, Enabled, and Limit jointly select idle admission and
	// cleanup. An empty session, disabled switch, or nonpositive limit
	// bypasses cache access in RegisterSession and FinishSession.
	SessionID string // client session identifier
	Enabled   bool   // idle session gate switch
	Limit     int    // maximum active sessions
	// IdleTimeout supplies the inactivity window to cache registration. The
	// Redis adapter truncates it to whole seconds and substitutes its
	// default when that value is nonpositive.
	IdleTimeout time.Duration // idle activity lifetime
}

// RegisterSession refreshes or admits an enabled session through the supplied
// cache. Missing cache, disabled or nonpositive capacity, and an empty session
// bypass registration; cache failures allow the attempt to proceed.
func RegisterSession(ctx context.Context, cache SessionLimitCache, input SessionBinding) bool {
	if !input.Enabled || input.Limit <= 0 || input.SessionID == "" || cache == nil {
		return true
	}
	allowed, err := cache.RegisterSession(ctx, input.ProviderID, input.SessionID, input.Limit, input.IdleTimeout)
	// A failed cache operation leaves registration unconfirmed. Permit
	// forwarding rather than treating unavailable session storage as a
	// capacity rejection.
	return err != nil || allowed
}

// FinishSession retains a served attempt registration for the idle window and
// unregisters unserved work. Served includes billable partial results;
// disabled bindings, empty sessions, nonpositive capacity, and absent caches
// need no cleanup.
func FinishSession(ctx context.Context, cache SessionLimitCache, input SessionBinding, outcome AttemptOutcome, diagnostics Diagnostics) {
	if outcome.Served || !input.Enabled || input.Limit <= 0 || input.SessionID == "" || cache == nil {
		return
	}
	// Unserved work relinquishes idle capacity immediately. Report cleanup
	// failure through diagnostics; registration expiry remains available
	// when removal cannot be confirmed.
	if err := cache.UnregisterSession(ctx, input.ProviderID, input.SessionID); err != nil {
		diagnostics.event("debug", "session_limit.release_failed", "provider_id", input.ProviderID, "error", err)
	}
}
