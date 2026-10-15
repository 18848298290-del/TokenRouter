package provider

import (
	"context"
	"time"
)

// fallback_health.go selects and applies fallback cooldowns for responses
// lacking a usable rate-limit reset.
// A 429 response can lack a usable upstream reset while still requiring a short
// scheduling pause. The fallback policy reads administrator settings when
// available, honors a disabled setting, and otherwise uses the built-in enabled
// five-second policy. Durations are clamped to the supported seconds range
// before a deadline is calculated from the service clock.
//
// The fallback command notifies its configured runtime blocker before calling
// the rate-limit writer. The production writer can replace an existing
// provider-wide reset and publishes the resulting state separately. A logged
// write failure leaves the earlier runtime notification independent of stored
// health. This default policy is also read by model-specific Spark handling,
// which makes its own choice when the policy is disabled.

// Fallback duration values govern reset-less 429 handling; the default is five
// seconds and the maximum accepted policy spans two hours.
const (
	DefaultRateLimit429CooldownSeconds = 5
	MaxRateLimit429CooldownSeconds     = 7200
)

// RateLimit429CooldownSettings supplies the enabled switch and default duration
// for reset-less 429 health handling.
type RateLimit429CooldownSettings struct {
	// Enabled and CooldownSeconds govern the provider fallback deadline together.
	// Disabled settings suppress that command; enabled durations are clamped
	// before conversion from seconds.
	Enabled         bool `json:"enabled"`          // provider fallback enabled
	CooldownSeconds int  `json:"cooldown_seconds"` // seconds
}

// Apply429Fallback attempts a provider-wide rate-limit deadline when the caller
// lacks an upstream reset. Supply a usable service and record; the policy
// resolver selects a clamped duration or a disabled result. A disabled policy
// logs and returns.
//
// An enabled deadline uses the service clock, then reaches the configured
// blocker before storage. SetRateLimited can replace a stored window and
// publishes separately. Persistence failures are logged and returned as no
// value, so the caller cannot infer a stored update from completion of this
// command.
func (s *HealthService) Apply429Fallback(ctx context.Context, provider *Record, reason string) {
	cooldown, enabled := s.Fallback429Cooldown(ctx, provider)
	if !enabled {
		s.options.Info("rate_limit_429_fallback_ignored", "provider_id", provider.ID, "platform", provider.Platform, "reason", reason)
		return
	}

	resetAt := s.options.Now().Add(cooldown)
	s.options.Warn("rate_limit_429_fallback_used", "provider_id", provider.ID, "platform", provider.Platform, "reason", reason, "using_default", cooldown.String())
	// SetRateLimited replaces the provider-wide observation and publishes it
	// separately; notify the configured blocker first so its process-local effect
	// can precede that storage work.
	s.notifyProviderSchedulingBlocked(provider, resetAt, "429_fallback")
	if err := s.providerRepo.SetRateLimited(ctx, provider.ID, resetAt); err != nil {
		s.options.Warn("rate_limit_set_failed", "provider_id", provider.ID, "error", err)
	}
}

// Fallback429Cooldown returns a clamped duration and whether fallback health
// handling is enabled. A successful runtime setting can disable it; missing,
// nil or failed settings use the built-in five-second enabled default. Nil
// results and failed reads are logged.
func (s *HealthService) Fallback429Cooldown(ctx context.Context, provider *Record) (time.Duration, bool) {
	if s.options.RateLimit429Settings != nil {
		settings, err := s.options.RateLimit429Settings(ctx)
		if err == nil && settings != nil {
			if !settings.Enabled {
				return 0, false
			}
			seconds := ClampRateLimit429CooldownSeconds(settings.CooldownSeconds)
			// Provider and Spark callers use these policy seconds as a deadline offset.
			return time.Duration(seconds) * time.Second, true
		}
		s.options.Warn("rate_limit_429_settings_read_failed", "provider_id", provider.ID, "error", err)
	}

	// Missing or failed runtime reads keep the reset-less response on the built-in
	// enabled policy, so optional settings storage cannot remove all fallback
	// duration information.
	seconds := DefaultRateLimit429CooldownSeconds
	seconds = ClampRateLimit429CooldownSeconds(seconds)
	// Convert built-in seconds to the duration form shared with loaded settings.
	return time.Duration(seconds) * time.Second, true
}

// ClampRateLimit429CooldownSeconds clamps policy duration to one through 7200
// seconds. Values outside that range select its nearest bound, allowing both
// runtime decoding and fallback resolution to share the supported duration
// limits.
func ClampRateLimit429CooldownSeconds(seconds int) int {
	if seconds < 1 {
		return 1
	}
	if seconds > MaxRateLimit429CooldownSeconds {
		return MaxRateLimit429CooldownSeconds
	}
	return seconds
}

// DefaultRateLimit429CooldownSettings returns a fresh enabled policy with a
// five-second duration.
func DefaultRateLimit429CooldownSettings() *RateLimit429CooldownSettings {
	return &RateLimit429CooldownSettings{
		Enabled:         true,
		CooldownSeconds: 5,
	}
}
