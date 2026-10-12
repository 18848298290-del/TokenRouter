package provider

import (
	"strings"
	"time"
)

// Model cooldown windows live under exact scope keys in provider Extra. Readers
// parse the stored reset and compare it with the record's clock, treating
// missing or invalid observations as inactive. SetModelRateLimitSnapshot changes
// one local payload and retains other compatible scope entries for later checks.
//
// Admission normally rejects a supplied key whose reset is still in the future.
// Antigravity's overage setting changes that decision while its credit-exhaustion
// scope remains inactive. The credit check participates when an active supplied
// key needs the exception; this helper does not enumerate other scopes or make
// an otherwise inactive key list depend on unrelated credit observations.

// ModelRateLimitResetAt reads the exact scope's RFC3339 reset timestamp from
// Extra. Missing records, absent or incorrectly typed data, empty scopes, and
// invalid timestamps return nil so callers treat the window as unobserved.
func (r *Record) ModelRateLimitResetAt(scope string) *time.Time {
	if r == nil || r.Extra == nil || scope == "" {
		return nil
	}
	rawLimits, ok := r.Extra["model_rate_limits"].(map[string]any)
	if !ok {
		return nil
	}
	rawLimit, ok := rawLimits[scope].(map[string]any)
	if !ok {
		return nil
	}
	resetAtRaw, ok := rawLimit["rate_limit_reset_at"].(string)
	if !ok || strings.TrimSpace(resetAtRaw) == "" {
		return nil
	}
	resetAt, err := time.Parse(time.RFC3339, resetAtRaw)
	if err != nil {
		// An invalid timestamp supplies no usable reset observation. Return nil so
		// clock-based queries use their missing-window result for this scope.
		return nil
	}
	return &resetAt
}

// SetModelRateLimitSnapshot replaces one scope's local cooldown payload on a
// mutable record. Nil records and blank scopes are ignored; nonempty scope
// spelling is retained as supplied, and a trimmed nonempty reason is added.
// Callers use a private record or synchronize its map mutation. A stored
// model_rate_limits value typed as map[string]any must be nonnil before mutation.
//
// Existing compatible scope entries remain in the model_rate_limits map, while
// an incompatible container is replaced before the new payload is stored. The
// UTC timestamps use RFC3339 second precision for subsequent reset lookup and
// clock comparison. This mutation does not persist the record or publish its
// new state.
func SetModelRateLimitSnapshot(provider *Record, scope string, resetAt time.Time, reason string, now time.Time) {
	if provider == nil || strings.TrimSpace(scope) == "" {
		return
	}
	if provider.Extra == nil {
		provider.Extra = make(map[string]any)
	}
	limits, ok := provider.Extra["model_rate_limits"].(map[string]any)
	if !ok {
		// An incompatible container cannot accept a per-scope payload. Install a
		// writable dictionary so this observation replaces unusable window data
		// before the scope is stored.
		limits = make(map[string]any)
		provider.Extra["model_rate_limits"] = limits
	}
	// Store whole-second UTC timestamps to match the persisted Extra format.
	payload := map[string]any{
		"rate_limited_at":     now.UTC().Format(time.RFC3339),
		"rate_limit_reset_at": resetAt.UTC().Format(time.RFC3339),
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		payload["reason"] = reason
	}
	limits[scope] = payload
}

// ModelRateLimitActive reports whether the exact scope resets after the
// record's current clock. Missing or invalid window data returns false, and
// equality with the reset time makes the cooldown inactive.
func (r *Record) ModelRateLimitActive(key string) bool {
	reset := r.ModelRateLimitResetAt(key)
	return reset != nil && r.now().Before(*reset)
}

// ModelRateLimitRemaining returns the positive time until the exact scope
// resets, using the record's clock. Missing, invalid, and expired windows
// return zero, keeping an absent observation separate from a negative duration.
func (r *Record) ModelRateLimitRemaining(key string) time.Duration {
	reset := r.ModelRateLimitResetAt(key)
	if reset == nil {
		return 0
	}
	remaining := reset.Sub(r.now())
	if remaining > 0 {
		return remaining
	}
	return 0
}

// ModelRateLimitAllows reports whether the supplied scope keys permit model
// execution on this record. Nil records return false, while keys with no active
// window permit execution; unrelated cooldowns are considered only when their
// keys are supplied.
//
// The first active supplied key normally rejects execution. Antigravity
// overages can admit it when allow_overages is enabled and the
// credits-exhausted scope has no active window. An active credits scope matters
// to that exception; an empty or entirely inactive key list returns true
// without invoking the overage exception.
func (r *Record) ModelRateLimitAllows(keys []string) bool {
	if r == nil {
		return false
	}
	for _, key := range keys {
		if r.ModelRateLimitActive(key) {
			// A matching model window can be served through enabled overages while
			// credits have no active exhausted window, allowing this supplied-key check
			// to accept that route.
			if r.Platform == PlatformAntigravity && r.IsOveragesEnabled() && !r.ModelRateLimitActive(CreditsExhaustedKey) {
				return true
			}
			return false
		}
	}
	return true
}
