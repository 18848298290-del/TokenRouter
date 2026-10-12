package provider

import (
	"context"
	"strconv"
	"time"
)

const (
	ErrorPolicyNone            ErrorPolicyResult = iota // No configured policy matched; continue default handling.
	ErrorPolicyCustomSkipped                            // Custom codes are enabled but unmatched; return a generic error.
	ErrorPolicyCustomMatched                            // A custom code matched; stop scheduling.
	ErrorPolicyTempUnscheduled                          // A temporary scheduling-pause rule matched.
	ErrorPolicyPoolBypassed                             // Pool mode bypasses default local state updates; continue classifying the response.
)

// ErrorPolicyResult identifies the applied error policy.
type ErrorPolicyResult int

// UpstreamErrorDecision combines configured policy with provider health handling.
// Callers must inspect Policy to distinguish pool bypass from an unmatched custom code; StopScheduling alone is insufficient.
type UpstreamErrorDecision struct {
	Policy         ErrorPolicyResult
	StopScheduling bool
}

// ShouldReturnGenericError reports an unmatched custom code, which returns a generic 500 without failover.
func (d UpstreamErrorDecision) ShouldReturnGenericError() bool {
	return d.Policy == ErrorPolicyCustomSkipped
}

// ShouldFailover applies configured error policies to the failover decision.
// Unmatched custom codes prevent failover; other unhandled statuses use defaultFailover.
func (d UpstreamErrorDecision) ShouldFailover(provider *Record, statusCode int, defaultFailover bool) bool {
	if d.Policy == ErrorPolicyCustomSkipped {
		return false
	}
	if d.Policy == ErrorPolicyCustomMatched || d.Policy == ErrorPolicyTempUnscheduled {
		return true
	}
	if d.Policy == ErrorPolicyPoolBypassed {
		return defaultFailover || (provider != nil && provider.IsPoolModeRetryableStatus(statusCode))
	}
	return defaultFailover || d.StopScheduling
}

// ShouldFailoverWithDefaults selects separate defaults for ordinary providers and pool-mode errors.
// Configured error policies take precedence over those defaults.
func (d UpstreamErrorDecision) ShouldFailoverWithDefaults(
	provider *Record,
	statusCode int,
	nonPoolDefault bool,
	poolDefault bool,
) bool {
	switch d.Policy {
	case ErrorPolicyCustomSkipped:
		return false
	case ErrorPolicyCustomMatched, ErrorPolicyTempUnscheduled:
		return true
	case ErrorPolicyPoolBypassed:
		return poolDefault || (provider != nil && provider.IsPoolModeRetryableStatus(statusCode))
	default:
		return nonPoolDefault
	}
}

// RetryableOnSameProvider reports whether pool mode permits retrying the status on this provider.
func (d UpstreamErrorDecision) RetryableOnSameProvider(provider *Record, statusCode int) bool {
	return d.Policy == ErrorPolicyPoolBypassed && provider != nil && provider.IsPoolModeRetryableStatus(statusCode)
}

// ErrorDecisionWithoutPersistence evaluates configuration when no health service is available.
// It reads custom-code and pool policies without persisting health state.
func ErrorDecisionWithoutPersistence(provider *Record, statusCode int) UpstreamErrorDecision {
	decision := UpstreamErrorDecision{Policy: ErrorPolicyNone}
	if provider == nil {
		return decision
	}
	if provider.IsCustomErrorCodesEnabled() {
		if provider.ShouldHandleErrorCode(statusCode) {
			decision.Policy = ErrorPolicyCustomMatched
			decision.StopScheduling = true
		} else {
			decision.Policy = ErrorPolicyCustomSkipped
		}
		return decision
	}
	if provider.IsPoolMode() {
		decision.Policy = ErrorPolicyPoolBypassed
	}
	return decision
}

// CheckErrorPolicy evaluates custom codes and temporary scheduling-pause rules.
// Enabled custom codes take precedence over all later checks, including temporary pauses.
func (s *HealthService) CheckErrorPolicy(ctx context.Context, provider *Record, statusCode int, responseBody []byte, model string, escalateRepeated401 bool) ErrorPolicyResult {
	if provider == nil {
		return ErrorPolicyNone
	}
	if provider.IsCustomErrorCodesEnabled() {
		if provider.ShouldHandleErrorCode(statusCode) {
			return ErrorPolicyCustomMatched
		}
		s.options.Info("provider_error_code_skipped", "provider_id", provider.ID, "status_code", statusCode)
		return ErrorPolicyCustomSkipped
	}
	if provider.IsPoolMode() {
		// Pool mode still honors administrator-configured temporary pause rules.
		if s.TryTempUnschedulable(ctx, provider, statusCode, responseBody, false, model) {
			return ErrorPolicyTempUnscheduled
		}
		return ErrorPolicyPoolBypassed
	}
	// Ordinary providers use the global overload rule after provider-specific policy checks.
	if statusCode == 529 {
		return ErrorPolicyCustomMatched
	}
	if s.TryTempUnschedulable(ctx, provider, statusCode, responseBody, escalateRepeated401, model) {
		return ErrorPolicyTempUnscheduled
	}
	return ErrorPolicyNone
}

// ApplyAuthenticationFailure reports an authentication block and attempts to persist provider error status.
func (s *HealthService) ApplyAuthenticationFailure(ctx context.Context, provider *Record, errorMsg string) {
	s.notifyProviderSchedulingBlocked(provider, time.Time{}, "auth_error")
	if err := s.providerRepo.SetError(ctx, provider.ID, errorMsg); err != nil {
		s.options.Warn("provider_set_error_failed", "provider_id", provider.ID, "error", err)
		return
	}
	s.options.Warn("provider_disabled_auth_error", "provider_id", provider.ID, "error", errorMsg)
}

// ApplyCustomErrorCode reports a custom-code block and attempts to persist provider error status.
func (s *HealthService) ApplyCustomErrorCode(ctx context.Context, provider *Record, statusCode int, errorMsg string) {
	msg := "Custom error code " + strconv.Itoa(statusCode) + ": " + errorMsg
	s.notifyProviderSchedulingBlocked(provider, time.Time{}, "custom_error_code")
	if err := s.providerRepo.SetError(ctx, provider.ID, msg); err != nil {
		s.options.Warn("provider_set_error_failed", "provider_id", provider.ID, "status_code", statusCode, "error", err)
		return
	}
	s.options.Warn("provider_disabled_custom_error", "provider_id", provider.ID, "status_code", statusCode, "error", errorMsg)
}

// ApplyOverload handles 529 responses according to cooldown settings.
// The settings determine whether and how long scheduling is paused.
func (s *HealthService) ApplyOverload(ctx context.Context, provider *Record) {
	var settings *OverloadCooldownSettings
	if s.options.OverloadSettings != nil {
		var err error
		settings, err = s.options.OverloadSettings(ctx)
		if err != nil {
			s.options.Warn("overload_settings_read_failed", "provider_id", provider.ID, "error", err)
			settings = nil
		}
	}
	// Use process configuration when runtime settings are unavailable.
	if settings == nil {
		cooldown := s.options.OverloadMinutes
		if cooldown <= 0 {
			cooldown = 10
		}
		settings = &OverloadCooldownSettings{Enabled: true, CooldownMinutes: cooldown}
	}

	if !settings.Enabled {
		s.options.Info("provider_529_ignored", "provider_id", provider.ID, "reason", "overload_cooldown_disabled")
		return
	}

	cooldownMinutes := settings.CooldownMinutes
	if cooldownMinutes <= 0 {
		cooldownMinutes = 10
	}

	until := s.options.Now().Add(time.Duration(cooldownMinutes) * time.Minute)
	s.notifyProviderSchedulingBlocked(provider, until, "529")
	if err := s.providerRepo.SetOverloaded(ctx, provider.ID, until); err != nil {
		s.options.Warn("overload_set_failed", "provider_id", provider.ID, "error", err)
		return
	}

	s.options.Info("provider_overloaded", "provider_id", provider.ID, "until", until)
}
