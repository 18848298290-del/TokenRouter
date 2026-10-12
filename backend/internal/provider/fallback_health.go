package provider

import (
	"context"
	"time"
)

const (
	DefaultRateLimit429CooldownSeconds = 5
	MaxRateLimit429CooldownSeconds     = 7200
)

// RateLimit429CooldownSettings controls the default cooldown for 429 responses.
type RateLimit429CooldownSettings struct {
	// Enabled applies the default cooldown when the upstream reset time cannot be parsed.
	Enabled bool `json:"enabled"`
	// CooldownSeconds is the default cooldown in seconds.
	CooldownSeconds int `json:"cooldown_seconds"`
}

func (s *HealthService) Apply429Fallback(ctx context.Context, provider *Record, reason string) {
	cooldown, enabled := s.Fallback429Cooldown(ctx, provider)
	if !enabled {
		s.options.Info("rate_limit_429_fallback_ignored", "provider_id", provider.ID, "platform", provider.Platform, "reason", reason)
		return
	}

	resetAt := s.options.Now().Add(cooldown)
	s.options.Warn("rate_limit_429_fallback_used", "provider_id", provider.ID, "platform", provider.Platform, "reason", reason, "using_default", cooldown.String())
	s.notifyProviderSchedulingBlocked(provider, resetAt, "429_fallback")
	if err := s.providerRepo.SetRateLimited(ctx, provider.ID, resetAt); err != nil {
		s.options.Warn("rate_limit_set_failed", "provider_id", provider.ID, "error", err)
	}
}

func (s *HealthService) Fallback429Cooldown(ctx context.Context, provider *Record) (time.Duration, bool) {
	if s.options.RateLimit429Settings != nil {
		settings, err := s.options.RateLimit429Settings(ctx)
		if err == nil && settings != nil {
			if !settings.Enabled {
				return 0, false
			}
			seconds := ClampRateLimit429CooldownSeconds(settings.CooldownSeconds)
			return time.Duration(seconds) * time.Second, true
		}
		s.options.Warn("rate_limit_429_settings_read_failed", "provider_id", provider.ID, "error", err)
	}

	seconds := DefaultRateLimit429CooldownSeconds
	seconds = ClampRateLimit429CooldownSeconds(seconds)
	return time.Duration(seconds) * time.Second, true
}

func ClampRateLimit429CooldownSeconds(seconds int) int {
	if seconds < 1 {
		return 1
	}
	if seconds > MaxRateLimit429CooldownSeconds {
		return MaxRateLimit429CooldownSeconds
	}
	return seconds
}

// DefaultRateLimit429CooldownSettings enables a five-second default cooldown.
func DefaultRateLimit429CooldownSettings() *RateLimit429CooldownSettings {
	return &RateLimit429CooldownSettings{
		Enabled:         true,
		CooldownSeconds: 5,
	}
}
