package provider

import (
	"context"
	"time"
)

type GrokRateLimitWriter interface {
	SetRateLimited(context.Context, int64, time.Time) error
}

type grokRateLimitExtensionWriter interface {
	SetRateLimitedIfLater(context.Context, int64, time.Time) error
}

type grokRateLimitRecoveryWriter interface {
	ClearRateLimitIfObserved(context.Context, int64, time.Time, time.Time) (bool, error)
}

// PersistGrokRateLimit normalizes the deadline and writes within an independent five-second budget, preferring
// an extension-only write.
func PersistGrokRateLimit(ctx context.Context, writer GrokRateLimitWriter, value *Record, reset time.Time, warn func(string, ...any)) {
	if writer == nil || value == nil || value.ID <= 0 {
		return
	}
	reset = NormalizeGrokRateLimitResetAt(value, reset, time.Now())
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	stateCtx, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	var err error
	if extending, ok := writer.(grokRateLimitExtensionWriter); ok {
		err = extending.SetRateLimitedIfLater(stateCtx, value.ID, reset)
	} else {
		err = writer.SetRateLimited(stateCtx, value.ID, reset)
	}
	if err != nil {
		warn("persist_grok_rate_limit_failed", "provider_id", value.ID, "reset_at", reset.UTC(), "error", err)
	}
}

// ClearGrokRateLimitAfterRecovery clears a limit only when its stored timestamps match the observed ones.
func ClearGrokRateLimitAfterRecovery(ctx context.Context, writer GrokRateLimitWriter, value *Record, warn func(string, ...any)) {
	if writer == nil || value == nil || value.RateLimitedAt == nil || value.RateLimitResetAt == nil || ctx.Err() != nil {
		return
	}
	recovery, ok := writer.(grokRateLimitRecoveryWriter)
	if !ok {
		return
	}
	_, err := recovery.ClearRateLimitIfObserved(ctx, value.ID, *value.RateLimitedAt, *value.RateLimitResetAt)
	if err != nil {
		warn("grok_rate_limit_recovery_clear_failed", "provider_id", value.ID, "error", err)
	}
}
