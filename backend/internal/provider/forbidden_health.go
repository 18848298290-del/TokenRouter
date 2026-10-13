package provider

import (
	"context"
	"fmt"
	"time"
)

// A classified non-HTML 403 can request a temporary pause or an account error
// under administrator policy. Disabled cooldown handling enters the
// authentication-failure command immediately. Enabled threshold accounting
// accumulates failures in the cache window; a missing or failed counter also
// selects authentication failure because the core lacks a usable shared count.
//
// Below threshold, the command builds a temporary reason and notifies its
// configured blocker before storage. The persistent pause and runtime
// notification have separate outcomes, and a failed temporary write falls back
// to authentication failure. Threshold counting is optional: disabling it
// resets prior history before the temporary pause. Successful recovery uses
// ResetForbiddenCounter to clear that same history independently of stored
// health.

// Built-in 403 policy values govern temporary pause duration and the
// count/window defaults used when administrator values are nonpositive.
const (
	OpenAI403CooldownMinutesDefault      = 10
	OpenAI403DisableThresholdDefault     = 3
	OpenAI403CounterWindowMinutesDefault = 180
)

// OpenAI403CooldownSettings supplies temporary-pause and shared-count policy
// for classified provider 403 failures.
type OpenAI403CooldownSettings struct {
	// Enabled and CooldownMinutes select temporary handling together.
	// ApplyForbidden routes disabled handling to authentication failure and
	// replaces an enabled nonpositive duration with its built-in cooldown.
	Enabled         bool `json:"enabled"`          // temporary handling enabled
	CooldownMinutes int  `json:"cooldown_minutes"` // minutes
	// ErrorOnThresholdEnabled, ThresholdCount and ThresholdWindowMinutes govern
	// shared counting together. A reached count selects authentication failure;
	// disabling this control clears prior counter history before a temporary
	// pause. ApplyForbidden substitutes built-in count/window defaults when their
	// respective values are nonpositive.
	ErrorOnThresholdEnabled bool `json:"error_on_threshold_enabled"` // counted account errors
	ThresholdCount          int  `json:"threshold_count"`            // 403 trip count
	ThresholdWindowMinutes  int  `json:"threshold_window_minutes"`   // minutes
}

// OpenAI403CounterCache supplies shared provider 403 counts and independent
// history reset operations.
type OpenAI403CounterCache interface {
	// IncrementOpenAI403Count records a failure and returns the shared count used
	// by ApplyForbidden. The production Redis adapter starts a fixed expiration
	// on the first increment with a one-minute minimum and retains it for later
	// failures.
	IncrementOpenAI403Count(ctx context.Context, providerID int64, windowMinutes int) (int64, error)
	// ResetOpenAI403Count clears the selected provider's failure history,
	// including an already absent counter. Errors remain available to the health
	// core, which logs incomplete cleanup and continues its selected health
	// action.
	ResetOpenAI403Count(ctx context.Context, providerID int64) error
}

// ApplyForbidden requests a scheduling stop for a classified 403 using current
// administrator policy. Supply a usable service and record. Disabled cooldown
// policy, unavailable threshold accounting and a reached threshold select
// authentication failure; nonpositive count, window and duration values use
// their built-in defaults.
//
// A subthreshold count remains in shared history while a temporary pause is
// attempted. Disabling counting clears prior history. Runtime blocking precedes
// the temporary write, and a failed write then attempts authentication failure.
// The method returns true for each handled path, including logged persistence
// failures; this result is a health decision rather than confirmation of an
// affected storage row.
func (s *HealthService) ApplyForbidden(ctx context.Context, provider *Record, msg string) (shouldDisable bool) {
	settings := s.ForbiddenSettings(ctx, provider.ID)
	if !settings.Enabled {
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true
	}

	thresholdCount := settings.ThresholdCount
	if thresholdCount <= 0 {
		thresholdCount = OpenAI403DisableThresholdDefault
	}
	thresholdWindowMinutes := settings.ThresholdWindowMinutes
	if thresholdWindowMinutes <= 0 {
		thresholdWindowMinutes = OpenAI403CounterWindowMinutesDefault
	}

	var count int64
	if settings.ErrorOnThresholdEnabled {
		// Threshold handling needs shared history to establish a subthreshold result.
		// Missing accounting therefore enters authentication failure instead of
		// claiming a verified temporary-only outcome.
		if s.options.ForbiddenCounter == nil {
			s.ApplyAuthenticationFailure(ctx, provider, msg)
			return true
		}

		var err error
		count, err = s.options.ForbiddenCounter.IncrementOpenAI403Count(ctx, provider.ID, thresholdWindowMinutes)
		// A failed increment cannot establish the shared count and may already have
		// reached Redis. Keep that failure visible, then select the
		// authentication-failure command.
		if err != nil {
			s.options.Warn("openai_403_increment_failed", "provider_id", provider.ID, "error", err)
			s.ApplyAuthenticationFailure(ctx, provider, msg)
			return true
		}

		if count >= int64(thresholdCount) {
			msg = fmt.Sprintf("%s | consecutive_403=%d/%d", msg, count, thresholdCount)
			s.ApplyAuthenticationFailure(ctx, provider, msg)
			return true
		}
	} else {
		// Clearing disabled threshold history prevents older counted failures from
		// immediately escalating a later re-enabled policy; temporary cooldown
		// handling continues independently after this best-effort reset.
		s.ResetForbiddenCounter(ctx, provider.ID)
	}

	cooldownMinutes := settings.CooldownMinutes
	if cooldownMinutes <= 0 {
		cooldownMinutes = OpenAI403CooldownMinutesDefault
	}

	// Use cooldown minutes against the health clock. This pause deadline is
	// separate from the counter's first-increment counting window.
	until := s.options.Now().Add(time.Duration(cooldownMinutes) * time.Minute)
	platformLabel := "OpenAI"
	if provider.IsCNProvider() {
		platformLabel = provider.Platform
	}
	reason := fmt.Sprintf("%s 403 temporary cooldown: %s", platformLabel, msg)
	if settings.ErrorOnThresholdEnabled {
		reason = fmt.Sprintf("%s 403 temporary cooldown (%d/%d): %s", platformLabel, count, thresholdCount, msg)
	}
	// Publish the runtime notification before the persistent pause call. Counter
	// history survives this phase, and a failed pause attempts account-error
	// handling after the notification has already run.
	s.notifyProviderSchedulingBlocked(provider, until, "openai_403_temp")
	if err := s.providerRepo.SetTempUnschedulable(ctx, provider.ID, until, reason); err != nil {
		s.options.Warn("openai_403_set_temp_unschedulable_failed", "provider_id", provider.ID, "error", err)
		s.ApplyAuthenticationFailure(ctx, provider, msg)
		return true
	}

	s.options.Warn(
		"openai_403_temp_unschedulable",
		"provider_id", provider.ID,
		"until", until,
		"count", count,
		"threshold", thresholdCount,
		"threshold_window_minutes", thresholdWindowMinutes,
		"error_on_threshold_enabled", settings.ErrorOnThresholdEnabled,
	)
	return true
}

// ForbiddenSettings returns loaded 403 policy or fresh defaults when settings
// are unavailable. Read errors are logged, and a loaded nonpositive cooldown is
// replaced in that returned pointer; remaining count defaults are resolved by
// ApplyForbidden.
func (s *HealthService) ForbiddenSettings(ctx context.Context, providerID int64) *OpenAI403CooldownSettings {
	settings := DefaultOpenAI403CooldownSettings()
	if settings.CooldownMinutes <= 0 {
		settings.CooldownMinutes = OpenAI403CooldownMinutesDefault
	}
	if s == nil || s.options.ForbiddenSettings == nil {
		return settings
	}

	loaded, err := s.options.ForbiddenSettings(ctx)
	// A storage failure leaves the built-in policy available for this response.
	// Log the reader error before returning defaults so failed reads remain
	// distinguishable from saved settings.
	if err != nil {
		s.options.Warn("openai_403_settings_read_failed", "provider_id", providerID, "error", err)
		return settings
	}
	if loaded == nil {
		return settings
	}
	if loaded.CooldownMinutes <= 0 {
		loaded.CooldownMinutes = settings.CooldownMinutes
	}
	return loaded
}

// ResetForbiddenCounter clears shared 403 history when a cache and positive
// provider ID are available. Nil services or absent counters are skipped;
// deletion errors are logged and returned as no value.
func (s *HealthService) ResetForbiddenCounter(ctx context.Context, providerID int64) {
	if s == nil || s.options.ForbiddenCounter == nil || providerID <= 0 {
		return
	}
	// Recovery and disabled-threshold callers continue after deletion fails.
	// Retain the diagnostic because uncleared shared history can still contribute
	// to a later counted health decision.
	if err := s.options.ForbiddenCounter.ResetOpenAI403Count(ctx, providerID); err != nil {
		s.options.Warn("openai_403_reset_failed", "provider_id", providerID, "error", err)
	}
}

// DefaultOpenAI403CooldownSettings returns a fresh enabled ten-minute pause
// policy with a three-failure threshold in a 180-minute counter window.
func DefaultOpenAI403CooldownSettings() *OpenAI403CooldownSettings {
	return &OpenAI403CooldownSettings{
		Enabled:                 true,
		CooldownMinutes:         OpenAI403CooldownMinutesDefault,
		ErrorOnThresholdEnabled: true,
		ThresholdCount:          OpenAI403DisableThresholdDefault,
		ThresholdWindowMinutes:  OpenAI403CounterWindowMinutesDefault,
	}
}
