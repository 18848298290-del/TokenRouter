package provider

import (
	"strings"
	"time"
)

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
		return nil
	}
	return &resetAt
}

func SetModelRateLimitSnapshot(provider *Record, scope string, resetAt time.Time, reason string, now time.Time) {
	if provider == nil || strings.TrimSpace(scope) == "" {
		return
	}
	if provider.Extra == nil {
		provider.Extra = make(map[string]any)
	}
	limits, ok := provider.Extra["model_rate_limits"].(map[string]any)
	if !ok {
		limits = make(map[string]any)
		provider.Extra["model_rate_limits"] = limits
	}
	payload := map[string]any{
		"rate_limited_at":     now.UTC().Format(time.RFC3339),
		"rate_limit_reset_at": resetAt.UTC().Format(time.RFC3339),
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		payload["reason"] = reason
	}
	limits[scope] = payload
}

// ModelRateLimitActive checks the model cooldown using the record's clock.
func (r *Record) ModelRateLimitActive(key string) bool {
	reset := r.ModelRateLimitResetAt(key)
	return reset != nil && r.now().Before(*reset)
}

// ModelRateLimitRemaining returns zero when the model cooldown is absent or expired.
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

// ModelRateLimitAllows checks model cooldowns, allowing Antigravity overages while credits remain.
func (r *Record) ModelRateLimitAllows(keys []string) bool {
	if r == nil {
		return false
	}
	for _, key := range keys {
		if r.ModelRateLimitActive(key) {
			if r.Platform == PlatformAntigravity && r.IsOveragesEnabled() && !r.ModelRateLimitActive(CreditsExhaustedKey) {
				return true
			}
			return false
		}
	}
	return true
}
