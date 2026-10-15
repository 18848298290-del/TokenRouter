package provider

import (
	"context"
	"time"
)

// grok_limit_persistence.go persists Grok rate-limit observations and requests
// conditional recovery of observed windows.
// Grok rate-limit observations can arrive after the originating request is
// cancelled or after another request has extended the pause. Persistence uses a
// detached write budget and optional later-only updates; recovery asks the
// store to compare both observed timestamps.

// GrokRateLimitWriter persists provider-wide Grok rate-limit deadlines by
// record ID.
type GrokRateLimitWriter interface {
	// SetRateLimited writes the supplied deadline for a provider ID.
	// Implementations supporting later-only updates expose
	// grokRateLimitExtensionWriter so persistence can avoid shortening an
	// existing pause on that path.
	SetRateLimited(context.Context, int64, time.Time) error
}

// grokRateLimitExtensionWriter supports extending an existing stored deadline
// after another observation.
type grokRateLimitExtensionWriter interface {
	// SetRateLimitedIfLater extends the provider deadline when the new
	// reset is
	// later. A nil error also permits an unchanged row when its stored
	// deadline
	// already covers the observation.
	SetRateLimitedIfLater(context.Context, int64, time.Time) error
}

// grokRateLimitRecoveryWriter clears deadlines conditionally against the
// previously observed rate-limit timestamps.
type grokRateLimitRecoveryWriter interface {
	// ClearRateLimitIfObserved clears a provider pause matching the
	// supplied
	// limited-at and reset-at timestamps. False reports a comparison miss
	// so a
	// newer rate-limit observation can remain stored.
	ClearRateLimitIfObserved(context.Context, int64, time.Time, time.Time) (bool, error)
}

// PersistGrokRateLimit normalizes and attempts to persist a Grok deadline for a
// positive provider ID. Its five-second budget ignores caller cancellation; an
// extension-capable writer is preferred, and write failure calls the supplied
// warning function.
func PersistGrokRateLimit(ctx context.Context, writer GrokRateLimitWriter, value *Record, reset time.Time, warn func(string, ...any)) {
	if writer == nil || value == nil || value.ID <= 0 {
		return
	}
	reset = NormalizeGrokRateLimitResetAt(value, reset, time.Now())
	base := context.Background()
	if ctx != nil {
		// Request cancellation can follow a received rate-limit
		// response;
		// detach it so the bounded scheduling-state write can finish.
		base = context.WithoutCancel(ctx)
	}
	stateCtx, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	var err error
	// Prefer a monotonic deadline update when supported; the basic writer
	// path
	// leaves comparison policy to its implementation.
	if extending, ok := writer.(grokRateLimitExtensionWriter); ok {
		err = extending.SetRateLimitedIfLater(stateCtx, value.ID, reset)
	} else {
		err = writer.SetRateLimited(stateCtx, value.ID, reset)
	}
	if err != nil {
		warn("persist_grok_rate_limit_failed", "provider_id", value.ID, "reset_at", reset.UTC(), "error", err)
	}
}

// ClearGrokRateLimitAfterRecovery attempts a clear when the writer supports
// observed-timestamp comparison and both timestamps exist. Cancellation skips
// the attempt; write errors call warn, and a comparison miss is accepted as a
// no-op. The writer receives ID and both observed timestamps; this call
// supplies no complete credential-identity comparison.
func ClearGrokRateLimitAfterRecovery(ctx context.Context, writer GrokRateLimitWriter, value *Record, warn func(string, ...any)) {
	if writer == nil || value == nil || value.RateLimitedAt == nil || value.RateLimitResetAt == nil || ctx.Err() != nil {
		return
	}
	recovery, ok := writer.(grokRateLimitRecoveryWriter)
	if !ok {
		return
	}
	// Compare both observed timestamps so a replaced timestamp pair
	// survives
	// this clear; complete credentials are outside the values passed to the
	// writer.
	_, err := recovery.ClearRateLimitIfObserved(ctx, value.ID, *value.RateLimitedAt, *value.RateLimitResetAt)
	if err != nil {
		warn("grok_rate_limit_recovery_clear_failed", "provider_id", value.ID, "error", err)
	}
}
