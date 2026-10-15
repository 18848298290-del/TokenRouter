package provider

import (
	"context"
	"maps"
	"strconv"
	"time"
)

// health_window_observation.go maintains Anthropic exhausted-window limits,
// session windows, and passive usage observations.
// Anthropic's provider-wide exhausted windows and Fable-only exhausted window
// require different cooldown scopes. A future provider reset is written when it
// extends the supplied record's live window or replaces an absent or expired
// one, while Fable rejection stays under the model-family key. These comparisons
// use the caller's record and do not condition the store write on a fresh
// database observation.
//
// Five-hour session metadata is maintained alongside cooldowns and passive
// usage. A new session window attempts to clear previous sample fields before
// writing window columns, then stores the latest sample; each operation can
// fail independently. Only an exact allowed status reaches the rate-limit
// recovery callback, after those observation writes have been attempted.

// SessionWindowStore writes provider window columns and merges separate usage
// observation patches.
type SessionWindowStore interface {
	// UpdateSessionWindow writes the supplied status and nonnil window bounds for
	// a provider. Nil bounds retain their stored columns, allowing status-only
	// observations to use the same operation.
	UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error
	// UpdateExtra merges a field patch into the provider's Extra data. The
	// PostgreSQL store may remove deprecated keys from the supplied map, so
	// callers pass disposable top-level patches.
	UpdateExtra(context.Context, int64, map[string]any) error
}

// QuotaWindowObservation carries a selected upstream rejection window and its
// reset information.
type QuotaWindowObservation struct {
	Window  string    // rejected upstream window
	ResetAt time.Time // selected window reset
	// FiveHourReset overrides ResetAt when maintaining rejected five-hour session
	// metadata. The upstream adapter shares this pointer, whose value stays
	// immutable during handling.
	FiveHourReset *time.Time
	Reason        string // runtime or model cooldown diagnostic
}

// SessionWindowObservation supplies response status, reset text, and passive
// usage for window maintenance.
type SessionWindowObservation struct {
	// Status gates all window maintenance: empty skips handling, and exact allowed
	// can recover a currently rate-limited provider after observation writes.
	Status string
	// Reset supplies the upstream deadline text. UpdateSessionWindow considers
	// usable values before selecting a local estimate for a missing or expired
	// window.
	Reset string
	// Passive supplies Extra fields for sampling. PersistPassiveUsage copies this
	// map shallowly, so callers keep nested mutable values stable throughout
	// storage.
	Passive map[string]any
}

// shouldPersistAnthropicWindowLimit reports whether a future reset extends the
// provider's observed live window. Nil inputs and resets at or before now are
// rejected; absent or expired provider windows allow any incoming future reset.
//
// When both resets remain future, only a strictly later incoming value is
// accepted. An equal or shorter reset leaves the current observation in place.
// The comparison uses the supplied record, so the later store call does not
// acquire a database condition protecting against another writer.
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

// ApplyExhaustedQuotaWindow handles a selected provider-wide quota rejection
// and reports true once a nonnil observation is accepted. Missing service,
// store, record, or observation returns false; session maintenance requires
// SessionWindows when reached.
//
// A reset extending the record's live window installs runtime blocking before
// its store write. Store failure is logged and still returns true, but skips
// rejected session metadata for that path. A reset kept unchanged, including a
// past observation, still updates rejected session metadata. The caller
// therefore interprets true as handled rejection, independently of whether the
// cooldown changed or persistence succeeded.
func (s *HealthService) ApplyExhaustedQuotaWindow(ctx context.Context, provider *Record, limit *QuotaWindowObservation, now time.Time) bool {
	if s == nil || s.providerRepo == nil || provider == nil {
		return false
	}
	if limit == nil {
		return false
	}
	// Keeping the provider cooldown does not discard rejection metadata. Maintain
	// its five-hour session status even when this observation cannot extend the
	// supplied record's reset.
	if !shouldPersistAnthropicWindowLimit(provider, limit, now) {
		s.options.Info("anthropic_window_rate_limit_kept",
			"provider_id", provider.ID,
			"window", limit.Window,
			"reset_at", limit.ResetAt,
			"existing_reset_at", provider.RateLimitResetAt)
		s.updateAnthropicRejectedSessionWindow(ctx, provider, limit)
		return true
	}

	// Block new local selection before persistence. If the write fails, retain the
	// handled rejection result and skip the following session-window update on
	// this path.
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

// updateAnthropicRejectedSessionWindow writes a rejected five-hour session
// ending at FiveHourReset when present, otherwise ResetAt. Configured
// SessionWindows is required after the input guards; bounds are supplied as
// observed even when past, and store failure is logged.
func (s *HealthService) updateAnthropicRejectedSessionWindow(ctx context.Context, provider *Record, limit *QuotaWindowObservation) {
	if s == nil || s.providerRepo == nil || provider == nil || limit == nil {
		return
	}
	// A longer selected rejection can carry its separate five-hour reset. Use that
	// end for session accounting so a seven-day cooldown does not anchor five-hour
	// usage.
	windowEnd := limit.ResetAt
	if limit.FiveHourReset != nil {
		windowEnd = *limit.FiveHourReset
	}
	// Session accounting spans five hours before the observed end.
	windowStart := windowEnd.Add(-5 * time.Hour)
	// Session metadata is auxiliary to rejection handling. Log a failed write
	// while leaving any earlier provider cooldown and the caller's selected
	// response outcome available.
	if err := s.options.SessionWindows.UpdateSessionWindow(ctx, provider.ID, &windowStart, &windowEnd, "rejected"); err != nil {
		s.options.Warn("rate_limit_update_session_window_failed", "provider_id", provider.ID, "error", err)
	}
}

// ApplyFableQuotaWindow handles a selected Fable-only rejection while keeping
// its cooldown model-scoped. Nil services, stores, records, or observations
// return false; a nonnil observation returns true even when the model-limit write
// fails.
// Callers provide SessionWindows for nonempty passive samples and select a
// usable reset before calling.
//
// Passive usage is attempted before installing the Fable limit, allowing the
// exhaustion sample to be available when subsequent scheduling stops that
// family. A sampling error is logged and does not prevent the model write. A
// model-write error is also logged and remains a handled Fable rejection.
func (s *HealthService) ApplyFableQuotaWindow(ctx context.Context, provider *Record, limit *QuotaWindowObservation, passive map[string]any) bool {
	if s == nil || s.providerRepo == nil || provider == nil {
		return false
	}
	if limit == nil {
		return false
	}
	// Store the exhaustion sample before the model cooldown can stop new Fable
	// observations. Sampling failures are logged while the following model-limit
	// write still proceeds.
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

// UpdateSessionWindow maintains observed five-hour session columns, passive
// samples, and allowed-response recovery through separate writes. An empty
// status skips all work; otherwise callers provide a record, service clock, and
// SessionWindows. Reset text is parsed as Unix seconds, with large values
// interpreted as milliseconds.
//
// An accepted reset can correct an existing end. A missing window or a stored
// end strictly before the service clock permits an hour-based estimate for
// allowed or allowed_warning responses. Initialization first clears old sample
// fields, then writes window columns and the new sample. Write failures do not
// stop later steps. Exact allowed can then invoke ClearWindowRateLimit for a
// record currently rate-limited;
// allowed_warning does not invoke recovery.
func (s *HealthService) UpdateSessionWindow(ctx context.Context, provider *Record, observation SessionWindowObservation) {
	status := observation.Status
	if status == "" {
		return
	}

	var windowStart, windowEnd *time.Time
	needInitWindow := provider.SessionWindowEnd == nil || s.options.Now().After(*provider.SessionWindowEnd)

	// An unusable upstream reset leaves timestamp arguments unset, allowing status
	// persistence and permitted initialization to continue after its diagnostic is
	// logged.
	if resetStr := observation.Reset; resetStr != "" {
		if ts, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
			// Large header timestamps use milliseconds; conversion discards subsecond
			// precision.
			if ts > 1e11 {
				s.options.Warn("provider_session_window_header_millis_detected", "provider_id", provider.ID, "raw_reset", resetStr)
				ts /= 1000
			}
			end := time.Unix(ts, 0)
			// A rejected reset leaves stored bounds available and permits an estimate
			// when initialization is needed.
			minAllowed := s.options.Now().Add(-5 * time.Hour)
			maxAllowed := s.options.Now().Add(7 * 24 * time.Hour)
			if end.Before(minAllowed) || end.After(maxAllowed) {
				s.options.Warn("provider_session_window_header_out_of_range", "provider_id", provider.ID, "raw_reset", resetStr, "parsed_end", end)
			} else if needInitWindow || provider.SessionWindowEnd == nil || !end.Equal(*provider.SessionWindowEnd) {
				// A changed accepted reset corrects the existing estimate. Derive its start
				// from the five-hour duration so subsequent window accounting uses the
				// observed endpoint.
				start := end.Add(-5 * time.Hour)
				windowStart = &start
				windowEnd = &end
				s.options.Info("provider_session_window_from_header", "provider_id", provider.ID, "window_start", start, "window_end", end, "status", status)
			}
		} else {
			s.options.Warn("provider_session_window_header_parse_failed", "provider_id", provider.ID, "raw_reset", resetStr, "error", err)
		}
	}

	// A missing or rejected reset still permits allowed traffic to initialize
	// accounting. Start at the service clock's local hour and estimate a five-hour
	// window.
	if windowEnd == nil && needInitWindow && (status == "allowed" || status == "allowed_warning") {
		now := s.options.Now()
		start := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
		end := start.Add(5 * time.Hour)
		windowStart = &start
		windowEnd = &end
		s.options.Info("provider_session_window_initialized", "provider_id", provider.ID, "window_start", start, "window_end", end, "status", status)
	}

	// A new accounting window must stop presenting previous samples before its
	// columns and fresh sample are written. Ignore clearing errors and continue
	// those independent writes.
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

	// Nil bounds leave the existing columns intact while recording status. A
	// failed window write is logged, and passive sampling and recovery still
	// proceed.
	if err := s.options.SessionWindows.UpdateSessionWindow(ctx, provider.ID, windowStart, windowEnd, status); err != nil {
		s.options.Warn("session_window_update_failed", "provider_id", provider.ID, "error", err)
	}

	s.PersistPassiveUsage(ctx, provider, observation.Passive)

	// After attempting observation writes, exact allowed status can clear the
	// supplied record's live rate limit through recovery. A failed recovery is
	// logged without reversing those observations.
	if status == "allowed" && provider.IsRateLimited() && s.options.ClearWindowRateLimit != nil {
		if err := s.options.ClearWindowRateLimit(ctx, provider.ID); err != nil {
			s.options.Warn("rate_limit_clear_failed", "provider_id", provider.ID, "error", err)
		}
	}
}

// PersistPassiveUsage merges nonempty usage fields with a UTC RFC3339 sample
// timestamp. Nonempty input requires a record, clock, and SessionWindows; its
// top-level map is copied, nested values remain shared, and store errors are
// logged.
func (s *HealthService) PersistPassiveUsage(ctx context.Context, value *Record, fields map[string]any) {
	if len(fields) == 0 {
		return
	}
	// Give the store a disposable top-level patch because it can filter keys.
	// Nested values remain shared; the sampling timestamp replaces any
	// caller-provided timestamp.
	updates := make(map[string]any, len(fields)+1)
	maps.Copy(updates, fields)
	updates["passive_usage_sampled_at"] = s.options.Now().UTC().Format(time.RFC3339)
	// Sampling failure leaves the supplied upstream observation available for
	// separate health decisions. Log it without interrupting the caller's later
	// cooldown or recovery handling.
	if err := s.options.SessionWindows.UpdateExtra(ctx, value.ID, updates); err != nil {
		s.options.Warn("passive_usage_update_failed", "provider_id", value.ID, "error", err)
	}
}
