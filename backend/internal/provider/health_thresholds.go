package provider

import (
	"context"
	"time"
)

// ApplyProviderSchedulingThreshold evaluates configured platform usage thresholds.
// It pauses a provider until the matching window resets and reports true for a new
// or identical existing provider pause. Model-scoped Fable pauses are handled separately.
func (s *HealthService) ApplyProviderSchedulingThreshold(ctx context.Context, provider *Record) bool {
	if s == nil || s.options.HasThresholdSettings == nil || !s.options.HasThresholdSettings() || s.providerRepo == nil || provider == nil || provider.ID <= 0 {
		return false
	}
	if !provider.IsActive() || !provider.Schedulable {
		return false
	}

	now := s.options.Now().UTC()
	thresholds := s.options.Thresholds(ctx)
	decision := EvaluateProviderSchedulingThreshold(provider, thresholds, now)
	if !decision.ShouldPause || decision.Until == nil || !decision.Until.After(now) {
		s.applyAnthropicFableSchedulingThreshold(ctx, provider, thresholds, now)
		return false
	}

	reason := BuildDetailedProviderSchedulingThresholdReason(ProviderSchedulingThresholdReasonInput{
		Platform:         decision.Platform,
		Window:           decision.Window,
		Scope:            decision.Scope,
		ThresholdPercent: decision.ThresholdPercent,
		UsedPercent:      decision.UsedPercent,
		Until:            *decision.Until,
		Now:              now,
	})

	if providerHasSameSchedulingThresholdPause(provider, *decision.Until, reason) {
		return true
	}
	if !provider.IsSchedulable() {
		return false
	}

	provider.TempUnschedulableUntil = CloneThresholdTime(decision.Until)
	provider.TempUnschedulableReason = reason
	s.notifyProviderSchedulingBlocked(provider, *decision.Until, "provider_scheduling_threshold")

	if err := s.providerRepo.SetTempUnschedulable(ctx, provider.ID, *decision.Until, reason); err != nil {
		s.options.Warn("provider_scheduling_threshold_set_temp_unsched_failed",
			"provider_id", provider.ID,
			"platform", decision.Platform,
			"window", decision.Window,
			"scope", decision.Scope,
			"threshold_percent", decision.ThresholdPercent,
			"used_percent", decision.UsedPercent,
			"until", decision.Until.UTC(),
			"error", err)
	} else if s.tempUnschedCache != nil {
		if state := tempUnschedStateFromStoredReason(reason, decision.Until.Unix()); state != nil {
			if err := s.tempUnschedCache.SetTempUnsched(ctx, provider.ID, state); err != nil {
				s.options.Warn("provider_scheduling_threshold_cache_set_failed", "provider_id", provider.ID, "error", err)
			}
		}
	}

	s.options.Info("provider_scheduling_threshold_temp_unschedulable",
		"provider_id", provider.ID,
		"platform", decision.Platform,
		"window", decision.Window,
		"scope", decision.Scope,
		"threshold_percent", decision.ThresholdPercent,
		"used_percent", decision.UsedPercent,
		"until", decision.Until.UTC())
	return true
}

func (s *HealthService) applyAnthropicFableSchedulingThreshold(ctx context.Context, provider *Record, thresholds map[string]int, now time.Time) {
	decision := EvaluateAnthropicFableSchedulingThreshold(provider, thresholds, now)
	if !decision.ShouldPause || decision.Until == nil || !decision.Until.After(now) {
		return
	}
	if reset := provider.ModelRateLimitResetAt(AnthropicFableRateLimitKey); reset != nil && s.options.Now().Before(*reset) {
		return
	}

	reason := BuildDetailedProviderSchedulingThresholdReason(ProviderSchedulingThresholdReasonInput{
		Platform:         decision.Platform,
		Window:           decision.Window,
		Scope:            decision.Scope,
		ThresholdPercent: decision.ThresholdPercent,
		UsedPercent:      decision.UsedPercent,
		Until:            *decision.Until,
		Now:              now,
	})
	SetModelRateLimitSnapshot(provider, AnthropicFableRateLimitKey, *decision.Until, reason, now)
	if err := s.providerRepo.SetModelRateLimit(ctx, provider.ID, AnthropicFableRateLimitKey, *decision.Until, reason); err != nil {
		s.options.Warn("anthropic_fable_scheduling_threshold_set_model_limit_failed",
			"provider_id", provider.ID,
			"threshold_percent", decision.ThresholdPercent,
			"used_percent", decision.UsedPercent,
			"until", decision.Until.UTC(),
			"error", err)
		return
	}

	s.options.Info("anthropic_fable_scheduling_threshold_model_limited",
		"provider_id", provider.ID,
		"scope", AnthropicFableRateLimitKey,
		"threshold_percent", decision.ThresholdPercent,
		"used_percent", decision.UsedPercent,
		"until", decision.Until.UTC())
}

func providerHasSameSchedulingThresholdPause(provider *Record, until time.Time, reason string) bool {
	if provider == nil || provider.TempUnschedulableUntil == nil {
		return false
	}
	if provider.TempUnschedulableUntil.UTC().Unix() != until.UTC().Unix() {
		return false
	}

	existing, ok := parseTempUnschedReasonPayload(provider.TempUnschedulableReason)
	if !ok || existing.Source != ProviderSchedulingThresholdReasonSource {
		return false
	}
	next, ok := parseTempUnschedReasonPayload(reason)
	if !ok || next.Source != ProviderSchedulingThresholdReasonSource {
		return false
	}

	existing.TriggeredAtUnix = 0
	next.TriggeredAtUnix = 0
	return existing == next
}
