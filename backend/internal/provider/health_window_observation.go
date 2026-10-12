package provider

import (
	"context"
	"maps"
	"strconv"
	"time"
)

// SessionWindowStore persists session window fields and usage observations.
type SessionWindowStore interface {
	UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error
	UpdateExtra(context.Context, int64, map[string]any) error
}

// QuotaWindowObservation is a parsed upstream quota-window rejection.
type QuotaWindowObservation struct {
	Window        string
	ResetAt       time.Time
	FiveHourReset *time.Time
	Reason        string
}

// SessionWindowObservation carries response window fields and parsed passive usage.
type SessionWindowObservation struct {
	Status  string
	Reset   string
	Passive map[string]any
}

func shouldPersistAnthropicWindowLimit(provider *Record, limit *QuotaWindowObservation, now time.Time) bool {
	if provider == nil || limit == nil || !limit.ResetAt.After(now) {
		return false
	}
	if provider.RateLimitResetAt == nil {
		return true
	}
	if !provider.RateLimitResetAt.After(now) {
		return true
	}
	return limit.ResetAt.After(*provider.RateLimitResetAt)
}

func (s *HealthService) ApplyExhaustedQuotaWindow(ctx context.Context, provider *Record, limit *QuotaWindowObservation, now time.Time) bool {
	if s == nil || s.providerRepo == nil || provider == nil {
		return false
	}
	if limit == nil {
		return false
	}
	if !shouldPersistAnthropicWindowLimit(provider, limit, now) {
		s.options.Info("anthropic_window_rate_limit_kept",
			"provider_id", provider.ID,
			"window", limit.Window,
			"reset_at", limit.ResetAt,
			"existing_reset_at", provider.RateLimitResetAt)
		s.updateAnthropicRejectedSessionWindow(ctx, provider, limit)
		return true
	}

	s.notifyProviderSchedulingBlocked(provider, limit.ResetAt, limit.Reason)
	if err := s.providerRepo.SetRateLimited(ctx, provider.ID, limit.ResetAt); err != nil {
		s.options.Warn("anthropic_window_rate_limit_set_failed",
			"provider_id", provider.ID,
			"window", limit.Window,
			"reset_at", limit.ResetAt,
			"error", err)
		return true
	}
	s.options.Info("anthropic_window_rate_limited",
		"provider_id", provider.ID,
		"window", limit.Window,
		"reset_at", limit.ResetAt,
		"reset_in", time.Until(limit.ResetAt).Truncate(time.Second))
	s.updateAnthropicRejectedSessionWindow(ctx, provider, limit)
	return true
}

func (s *HealthService) updateAnthropicRejectedSessionWindow(ctx context.Context, provider *Record, limit *QuotaWindowObservation) {
	if s == nil || s.providerRepo == nil || provider == nil || limit == nil {
		return
	}
	// Update the five-hour session window even when another temporary pause already applies.
	windowEnd := limit.ResetAt
	if limit.FiveHourReset != nil {
		windowEnd = *limit.FiveHourReset
	}
	windowStart := windowEnd.Add(-5 * time.Hour)
	if err := s.options.SessionWindows.UpdateSessionWindow(ctx, provider.ID, &windowStart, &windowEnd, "rejected"); err != nil {
		s.options.Warn("rate_limit_update_session_window_failed", "provider_id", provider.ID, "error", err)
	}
}

// ApplyFableQuotaWindow applies an observed 7d_oi rejection to the Fable model family.
// A true result means the caller must keep this cooldown model-scoped instead of limiting the whole provider.
func (s *HealthService) ApplyFableQuotaWindow(ctx context.Context, provider *Record, limit *QuotaWindowObservation, passive map[string]any) bool {
	if s == nil || s.providerRepo == nil || provider == nil {
		return false
	}
	if limit == nil {
		return false
	}
	// Persist the exhaustion sample before Fable scheduling stops for this provider.
	// Otherwise its usage display can retain the pre-limit value until the window resets.
	s.PersistPassiveUsage(ctx, provider, passive)
	if err := s.providerRepo.SetModelRateLimit(ctx, provider.ID, AnthropicFableRateLimitKey, limit.ResetAt, limit.Reason); err != nil {
		s.options.Warn("anthropic_fable_window_rate_limit_set_failed",
			"provider_id", provider.ID,
			"scope", AnthropicFableRateLimitKey,
			"reset_at", limit.ResetAt,
			"error", err)
		return true
	}
	s.options.Info("anthropic_fable_window_model_rate_limited",
		"provider_id", provider.ID,
		"scope", AnthropicFableRateLimitKey,
		"reset_at", limit.ResetAt,
		"reset_in", time.Until(limit.ResetAt).Truncate(time.Second))
	return true
}

// UpdateSessionWindow writes observed or estimated five-hour window fields and passive usage in separate operations.
func (s *HealthService) UpdateSessionWindow(ctx context.Context, provider *Record, observation SessionWindowObservation) {
	status := observation.Status
	if status == "" {
		return
	}

	// Estimate a window for an allowed response when the stored window is missing or expired
	// and no usable reset header supplies its end.
	var windowStart, windowEnd *time.Time
	needInitWindow := provider.SessionWindowEnd == nil || s.options.Now().After(*provider.SessionWindowEnd)

	// Prefer an upstream reset timestamp over the local estimate.
	if resetStr := observation.Reset; resetStr != "" {
		if ts, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
			// Values above 1e11 are treated as Unix milliseconds rather than seconds.
			if ts > 1e11 {
				s.options.Warn("provider_session_window_header_millis_detected", "provider_id", provider.ID, "raw_reset", resetStr)
				ts /= 1000
			}
			end := time.Unix(ts, 0)
			// Accept reset times from five hours ago through seven days ahead.
			minAllowed := s.options.Now().Add(-5 * time.Hour)
			maxAllowed := s.options.Now().Add(7 * 24 * time.Hour)
			if end.Before(minAllowed) || end.After(maxAllowed) {
				s.options.Warn("provider_session_window_header_out_of_range", "provider_id", provider.ID, "raw_reset", resetStr, "parsed_end", end)
			} else if needInitWindow || provider.SessionWindowEnd == nil || !end.Equal(*provider.SessionWindowEnd) {
				// Refresh the window when its end changes or the previous window has expired.
				start := end.Add(-5 * time.Hour)
				windowStart = &start
				windowEnd = &end
				s.options.Info("provider_session_window_from_header", "provider_id", provider.ID, "window_start", start, "window_end", end, "status", status)
			}
		} else {
			s.options.Warn("provider_session_window_header_parse_failed", "provider_id", provider.ID, "raw_reset", resetStr, "error", err)
		}
	}

	// For an allowed response with no usable reset, estimate a five-hour window from the current hour.
	if windowEnd == nil && needInitWindow && (status == "allowed" || status == "allowed_warning") {
		now := s.options.Now()
		start := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
		end := start.Add(5 * time.Hour)
		windowStart = &start
		windowEnd = &end
		s.options.Info("provider_session_window_initialized", "provider_id", provider.ID, "window_start", start, "window_end", end, "status", status)
	}

	// Clear usage from the previous window when initializing its replacement.
	if windowEnd != nil && needInitWindow {
		_ = s.options.SessionWindows.UpdateExtra(ctx, provider.ID, map[string]any{
			"session_window_utilization":      nil,
			"passive_usage_7d_utilization":    nil,
			"passive_usage_7d_reset":          nil,
			"passive_usage_7d_oi_utilization": nil,
			"passive_usage_7d_oi_reset":       nil,
			"passive_usage_sampled_at":        nil,
		})
	}

	if err := s.options.SessionWindows.UpdateSessionWindow(ctx, provider.ID, windowStart, windowEnd, status); err != nil {
		s.options.Warn("session_window_update_failed", "provider_id", provider.ID, "error", err)
	}

	// Persist the parsed five-hour, seven-day, and Fable usage fields in one extra update.
	s.PersistPassiveUsage(ctx, provider, observation.Passive)

	// An allowed response restores a currently rate-limited provider through the recovery callback.
	if status == "allowed" && provider.IsRateLimited() && s.options.ClearWindowRateLimit != nil {
		if err := s.options.ClearWindowRateLimit(ctx, provider.ID); err != nil {
			s.options.Warn("rate_limit_clear_failed", "provider_id", provider.ID, "error", err)
		}
	}
}

// PersistPassiveUsage writes nonempty usage fields with the current sampling timestamp.
func (s *HealthService) PersistPassiveUsage(ctx context.Context, value *Record, fields map[string]any) {
	if len(fields) == 0 {
		return
	}
	updates := make(map[string]any, len(fields)+1)
	maps.Copy(updates, fields)
	updates["passive_usage_sampled_at"] = s.options.Now().UTC().Format(time.RFC3339)
	if err := s.options.SessionWindows.UpdateExtra(ctx, value.ID, updates); err != nil {
		s.options.Warn("passive_usage_update_failed", "provider_id", value.ID, "error", err)
	}
}
