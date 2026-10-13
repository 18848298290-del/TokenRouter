package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Pool API Key failures can accumulate across instances even when individual
// requests use the upstream pool's retry policy. Eligible OpenAI pool records
// consult the runtime breaker setting and shared rolling counter; disabled
// settings or an untripped count leave pause publication skipped. The counter
// can clear its window as it reports a trip, before any provider state is
// stored.
//
// A tripped command first attempts the persistent pause with a detached
// three-second storage context. Only a successful store call reaches
// request-record extension, runtime notification and optional cache
// publication. Those later steps keep independent outcomes: the record extends
// its own shorter deadline, the runtime callback chooses its platform/block
// behavior, and cache failure is logged. A successful request leaves rolling
// failure history intact until the accounting window or a trip removes it.

// OpenAIAPIKeyHealthBreakerReason labels stored and cached pause diagnostics
// produced by counted OpenAI pool API Key failures.
const OpenAIAPIKeyHealthBreakerReason = "openai_apikey_health_breaker"

// isOpenAIAPIKeyHealthBreakerProvider reports whether a record is an OpenAI
// pool-mode API Key. Nil records and other platform/type combinations skip
// ApplyAPIKeyHealthFailure before the shared counter is consulted.
func isOpenAIAPIKeyHealthBreakerProvider(provider *Record) bool {
	return provider != nil && provider.Platform == capability.PlatformOpenAI && provider.Type == capability.ProviderTypeAPIKey && provider.IsPoolMode()
}

// ApplyAPIKeyHealthFailure counts an eligible failure and attempts a breaker
// pause for an OpenAI pool API Key. Missing dependencies, false eligibility,
// disabled settings, read errors or an untripped counter return false. Supply a
// stable mutable request record and usable context.
//
// A trip builds diagnostic state and attempts storage in a three-second context
// detached from caller cancellation. Store failure returns false before local
// publication, although rolling accounting may already have consumed its
// window. A successful call extends the request record's earlier deadline,
// notifies the configured blocker and attempts cache storage. Cache failure is
// logged and leaves the result true; that result confirms the successful store
// call, which can retain an already longer persistent pause.
func (s *HealthService) ApplyAPIKeyHealthFailure(ctx context.Context, provider *Record, statusCode int, responseBody []byte, eligible bool) bool {
	if s == nil || s.options.APIKeyHealthCounter == nil || s.options.APIKeyHealthSettings == nil || s.providerRepo == nil || !isOpenAIAPIKeyHealthBreakerProvider(provider) {
		return false
	}
	if !eligible {
		return false
	}
	// Read the enabled policy before touching shared failure history. An
	// unavailable setting leaves this command unselected, and the caller can
	// retain its other response-health decisions.
	settings, err := s.options.APIKeyHealthSettings(ctx)
	if err != nil {
		s.options.APIKeyHealthWarn("openai.apikey_health_breaker_settings_failed", "provider_id", provider.ID, "error", err)
		return false
	}
	if settings == nil || !settings.Enabled {
		return false
	}

	// Shared accounting precedes pause storage and can consume a tripped window.
	// If the cache returns an error, its backend may already have changed while
	// this command still lacks a usable trip result.
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
	// The scalar pause state supplies JSON for persistent and cached diagnostics.
	// If serialization produces no reason, the text fallback retains a
	// readable breaker label and count.
	reasonBytes, _ := json.Marshal(state)
	reason := string(reasonBytes)
	if reason == "" {
		reason = fmt.Sprintf("%s: %d failures in %d minute(s)", OpenAIAPIKeyHealthBreakerReason, count, settings.WindowMinutes)
	}

	// Attempt persistent pause storage before local publication, with a bounded
	// context detached from the failed request. A caller cancellation therefore
	// does not immediately discard this counted trip.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := s.providerRepo.SetTempUnschedulable(persistCtx, provider.ID, until, reason); err != nil {
		s.options.APIKeyHealthWarn("openai.apikey_health_breaker_persist_failed", "provider_id", provider.ID, "error", err)
		return false
	}

	// The successful store call admits local publication. Extend this request
	// snapshot only when its deadline is earlier, then notify runtime state and
	// publish cache data independently.
	if provider.TempUnschedulableUntil == nil || provider.TempUnschedulableUntil.Before(until) {
		provider.TempUnschedulableUntil = &until
		provider.TempUnschedulableReason = reason
	}
	s.notifyProviderSchedulingBlocked(provider, until, OpenAIAPIKeyHealthBreakerReason)
	// A cache write follows successful storage and runtime notification. Its
	// failure leaves those completed effects in place and remains a diagnostic
	// rather than changing the handled result.
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

// ObserveAPIKeyHealthSuccess leaves rolling pool failure history intact after a
// successful request.
func (s *HealthService) ObserveAPIKeyHealthSuccess(context.Context, *Record) {
}
