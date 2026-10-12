package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// OpenAIAPIKeyHealthBreakerReason identifies circuit-breaker pauses in stored and cached state.
const OpenAIAPIKeyHealthBreakerReason = "openai_apikey_health_breaker"

func isOpenAIAPIKeyHealthBreakerProvider(provider *Record) bool {
	return provider != nil && provider.Platform == capability.PlatformOpenAI && provider.Type == capability.ProviderTypeAPIKey && provider.IsPoolMode()
}

func (s *HealthService) ApplyAPIKeyHealthFailure(ctx context.Context, provider *Record, statusCode int, responseBody []byte, eligible bool) bool {
	if s == nil || s.options.APIKeyHealthCounter == nil || s.options.APIKeyHealthSettings == nil || s.providerRepo == nil || !isOpenAIAPIKeyHealthBreakerProvider(provider) {
		return false
	}
	if !eligible {
		return false
	}
	settings, err := s.options.APIKeyHealthSettings(ctx)
	if err != nil {
		s.options.APIKeyHealthWarn("openai.apikey_health_breaker_settings_failed", "provider_id", provider.ID, "error", err)
		return false
	}
	if settings == nil || !settings.Enabled {
		return false
	}

	count, tripped, err := s.options.APIKeyHealthCounter.RecordOpenAIAPIKeyHealthFailure(ctx, provider.ID, settings.WindowMinutes, settings.FailureThreshold)
	if err != nil {
		s.options.APIKeyHealthWarn("openai.apikey_health_breaker_record_failed", "provider_id", provider.ID, "error", err)
		return false
	}
	if !tripped {
		return false
	}

	now := s.options.Now()
	until := now.Add(time.Duration(settings.CooldownMinutes) * time.Minute)
	state := &TempUnschedState{
		UntilUnix:            until.Unix(),
		TriggeredAtUnix:      now.Unix(),
		StatusCode:           statusCode,
		MatchedKeyword:       OpenAIAPIKeyHealthBreakerReason,
		RuleIndex:            -1,
		ErrorMessage:         TruncateTempUnschedMessage(responseBody, TempUnschedMessageMaxBytes),
		TriggerCount:         count,
		TriggerThreshold:     settings.FailureThreshold,
		TriggerWindowMinutes: settings.WindowMinutes,
	}
	reasonBytes, _ := json.Marshal(state)
	reason := string(reasonBytes)
	if reason == "" {
		reason = fmt.Sprintf("%s: %d failures in %d minute(s)", OpenAIAPIKeyHealthBreakerReason, count, settings.WindowMinutes)
	}

	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := s.providerRepo.SetTempUnschedulable(persistCtx, provider.ID, until, reason); err != nil {
		s.options.APIKeyHealthWarn("openai.apikey_health_breaker_persist_failed", "provider_id", provider.ID, "error", err)
		return false
	}

	if provider.TempUnschedulableUntil == nil || provider.TempUnschedulableUntil.Before(until) {
		provider.TempUnschedulableUntil = &until
		provider.TempUnschedulableReason = reason
	}
	s.notifyProviderSchedulingBlocked(provider, until, OpenAIAPIKeyHealthBreakerReason)
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(persistCtx, provider.ID, state); err != nil {
			s.options.APIKeyHealthWarn("openai.apikey_health_breaker_cache_failed", "provider_id", provider.ID, "error", err)
		}
	}
	s.options.APIKeyHealthWarn("openai.apikey_health_breaker_tripped",
		"provider_id", provider.ID,
		"failure_count", count,
		"failure_threshold", settings.FailureThreshold,
		"window_minutes", settings.WindowMinutes,
		"cooldown_minutes", settings.CooldownMinutes,
		"upstream_status", statusCode,
		"until", until,
	)
	return true
}

func (s *HealthService) ObserveAPIKeyHealthSuccess(context.Context, *Record) {
	// Failures accumulate in their rolling window; success leaves the count intact and needs no cache access.
}
