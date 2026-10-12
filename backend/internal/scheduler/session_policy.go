package scheduler

import (
	"context"
	"time"
)

// SessionBinding contains the idle-session settings managed by this scheduling attempt.
type SessionBinding struct {
	ProviderID  int64
	SessionID   string
	Enabled     bool
	Limit       int
	IdleTimeout time.Duration
}

// RegisterSession refreshes or admits a session within capacity and allows requests on cache errors.
func RegisterSession(ctx context.Context, cache SessionLimitCache, input SessionBinding) bool {
	if !input.Enabled || input.Limit <= 0 || input.SessionID == "" || cache == nil {
		return true
	}
	allowed, err := cache.RegisterSession(ctx, input.ProviderID, input.SessionID, input.Limit, input.IdleTimeout)
	return err != nil || allowed
}

// FinishSession retains served sessions for their idle window and unregisters unserved sessions.
func FinishSession(ctx context.Context, cache SessionLimitCache, input SessionBinding, outcome AttemptOutcome, diagnostics Diagnostics) {
	if outcome.Served || !input.Enabled || input.Limit <= 0 || input.SessionID == "" || cache == nil {
		return
	}
	if err := cache.UnregisterSession(ctx, input.ProviderID, input.SessionID); err != nil {
		diagnostics.event("debug", "session_limit.release_failed", "provider_id", input.ProviderID, "error", err)
	}
}
