package provider

import (
	"context"
	"fmt"
	"time"
)

const (
	OpenAI403CooldownMinutesDefault      = 10
	OpenAI403DisableThresholdDefault     = 3
	OpenAI403CounterWindowMinutesDefault = 180
)

// OpenAI403CooldownSettings configures scheduling pauses and error thresholds for HTTP 403 responses.
type OpenAI403CooldownSettings struct {
	// Enabled selects temporary cooldowns for supported providers after a 403 response.
	Enabled bool `json:"enabled"`
	// CooldownMinutes is the cooldown duration in minutes.
	CooldownMinutes int `json:"cooldown_minutes"`
	// ErrorOnThresholdEnabled marks the provider as errored when the window reaches the threshold.
	ErrorOnThresholdEnabled bool `json:"error_on_threshold_enabled"`
	// ThresholdCount is the number of 403 responses needed to trigger the error state.
	ThresholdCount int `json:"threshold_count"`
	// ThresholdWindowMinutes is the counting window in minutes.
	ThresholdWindowMinutes int `json:"threshold_window_minutes"`
}

// OpenAI403CounterCache counts provider 403 responses within a window and supports resets on success.
type OpenAI403CounterCache interface {
	// IncrementOpenAI403Count atomically increments the count and returns its new value.
	IncrementOpenAI403Count(ctx context.Context, providerID int64, windowMinutes int) (int64, error)
	// ResetOpenAI403Count clears the provider's counter.
	ResetOpenAI403Count(ctx context.Context, providerID int64) error
}

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
		if s.options.ForbiddenCounter == nil {
			s.ApplyAuthenticationFailure(ctx, provider, msg)
			return true
		}

		var err error
		count, err = s.options.ForbiddenCounter.IncrementOpenAI403Count(ctx, provider.ID, thresholdWindowMinutes)
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
		s.ResetForbiddenCounter(ctx, provider.ID)
	}

	cooldownMinutes := settings.CooldownMinutes
	if cooldownMinutes <= 0 {
		cooldownMinutes = OpenAI403CooldownMinutesDefault
	}

	until := s.options.Now().Add(time.Duration(cooldownMinutes) * time.Minute)
	platformLabel := "OpenAI"
	if provider.IsCNProvider() {
		platformLabel = provider.Platform
	}
	reason := fmt.Sprintf("%s 403 temporary cooldown: %s", platformLabel, msg)
	if settings.ErrorOnThresholdEnabled {
		reason = fmt.Sprintf("%s 403 temporary cooldown (%d/%d): %s", platformLabel, count, thresholdCount, msg)
	}
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

func (s *HealthService) ForbiddenSettings(ctx context.Context, providerID int64) *OpenAI403CooldownSettings {
	settings := DefaultOpenAI403CooldownSettings()
	if settings.CooldownMinutes <= 0 {
		settings.CooldownMinutes = OpenAI403CooldownMinutesDefault
	}
	if s == nil || s.options.ForbiddenSettings == nil {
		return settings
	}

	loaded, err := s.options.ForbiddenSettings(ctx)
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

func (s *HealthService) ResetForbiddenCounter(ctx context.Context, providerID int64) {
	if s == nil || s.options.ForbiddenCounter == nil || providerID <= 0 {
		return
	}
	if err := s.options.ForbiddenCounter.ResetOpenAI403Count(ctx, providerID); err != nil {
		s.options.Warn("openai_403_reset_failed", "provider_id", providerID, "error", err)
	}
}

// DefaultOpenAI403CooldownSettings enables a ten-minute cooldown and an error threshold of three responses in
// 180 minutes.
func DefaultOpenAI403CooldownSettings() *OpenAI403CooldownSettings {
	return &OpenAI403CooldownSettings{
		Enabled:                 true,
		CooldownMinutes:         OpenAI403CooldownMinutesDefault,
		ErrorOnThresholdEnabled: true,
		ThresholdCount:          OpenAI403DisableThresholdDefault,
		ThresholdWindowMinutes:  OpenAI403CounterWindowMinutesDefault,
	}
}
