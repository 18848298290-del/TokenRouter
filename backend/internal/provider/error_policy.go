package provider

import (
	"context"
	"strconv"
	"time"
)

// error_policy.go classifies upstream failures under administrator policy and
// applies authentication and overload health updates.
// Administrator code lists and pool mode change which upstream failures request
// failover or provider health updates. A custom-code skip suppresses switching,
// while a matching code or temporary pause selects failover. Pool bypass
// retains the upstream pool retry-status rule, so forwarding cannot infer its
// action from the scheduling-stop flag alone.
//
// The policy checker gives custom codes precedence and can apply configured
// temporary pauses before returning its result. Authentication, custom-code and
// overload commands synchronously notify the configured blocker before calling
// storage. Those repository calls publish persistent health separately; logged
// failures can leave runtime notification and stored state with different
// outcomes for the current attempt and later selection.

// Policy results tell forwarding whether administrator codes matched, a pause was
// applied, or pool mode delegates later health classification to its upstream.
const (
	ErrorPolicyNone            ErrorPolicyResult = iota // No configured policy matched; continue default handling.
	ErrorPolicyCustomSkipped                            // Custom codes are enabled but unmatched; return a generic error.
	ErrorPolicyCustomMatched                            // A custom code or direct 529 check matched; request failover.
	ErrorPolicyTempUnscheduled                          // A temporary scheduling-pause rule matched.
	ErrorPolicyPoolBypassed                             // Pool mode bypasses default local state updates; continue classifying the response.
)

// ErrorPolicyResult distinguishes administrator policy actions from default
// health classification.
type ErrorPolicyResult int

// UpstreamErrorDecision holds policy classification and the scheduling effect
// of one upstream failure.
type UpstreamErrorDecision struct {
	// Policy and StopScheduling carry policy classification and a separate health
	// effect. Forwarding helpers give configured skips and pool retry rules
	// precedence over the stop flag.
	Policy         ErrorPolicyResult // selected upstream error policy
	StopScheduling bool              // health requested scheduling stop
}

// ShouldReturnGenericError reports whether an unmatched custom code selects a
// generic error.
func (d UpstreamErrorDecision) ShouldReturnGenericError() bool {
	return d.Policy == ErrorPolicyCustomSkipped
}

// ShouldFailover combines configured policy, pool retry status and the ordinary
// caller default. Unmatched custom codes prevent failover, while matched codes
// and temporary pauses require it. Callers pass the provider that produced this
// response.
//
// Pool bypass uses defaultFailover or the provider pool retry-status rule. Other
// policies use that default or StopScheduling. Keeping the policy result
// separate prevents a false scheduling-stop bit from erasing the special
// no-failover decision for unmatched administrator codes.
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

// ShouldFailoverWithDefaults selects caller defaults after applying the
// configured policy result. Unmatched custom codes prevent switching, and
// matched codes or temporary pauses require it. Callers supply independently
// computed pool and non-pool defaults for this response.
//
// Pool bypass accepts poolDefault or the provider pool retry-status rule.
// Remaining results use nonPoolDefault directly; this helper does not add
// StopScheduling in that branch. That distinction lets the caller retain its
// endpoint-specific default classification.
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

// RetryableOnSameProvider reports whether a pool-bypassed response matches the
// provider retry-status rule. Nil providers or other policy results return
// false, leaving same-provider retry distinct from the failover decision.
func (d UpstreamErrorDecision) RetryableOnSameProvider(provider *Record, statusCode int) bool {
	return d.Policy == ErrorPolicyPoolBypassed && provider != nil && provider.IsPoolModeRetryableStatus(statusCode)
}

// ErrorDecisionWithoutPersistence classifies custom-code and pool policy when
// health storage is unavailable. Nil records yield ErrorPolicyNone. Custom-code
// configuration takes precedence: a matched status sets StopScheduling, while
// an unmatched status selects CustomSkipped and leaves that bit false.
//
// Pool mode returns PoolBypassed after custom-code handling has been skipped.
// Ordinary records retain None. The caller can use the same forwarding decision
// helpers even though this classifier performs no health command or
// temporary-rule write.
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

// CheckErrorPolicy applies administrator policy before default upstream health
// classification. Nil records return none, and enabled custom codes return
// matched or skipped before any pause rule is tested. The caller decides whether
// a returned match needs a later health command.
//
// Pool mode still applies configured temporary pauses with repeated-401
// escalation disabled. Ordinary 529 responses select matched before temporary
// rules, while other ordinary responses can apply those rules using the supplied
// escalation flag and model. A temporary match may already have attempted its
// health write.
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
		// Configured pause rules still define local admission in pool mode. Disable
		// repeated-401 escalation here because the upstream pool retains
		// responsibility for ordinary credential recovery.
		if s.TryTempUnschedulable(ctx, provider, statusCode, responseBody, false, model) {
			return ErrorPolicyTempUnscheduled
		}
		return ErrorPolicyPoolBypassed
	}
	// Direct policy callers interpret ordinary 529 as matched here. Production
	// default handling dispatches ordinary 529 through ApplyOverload before
	// reaching this policy helper.
	if statusCode == 529 {
		return ErrorPolicyCustomMatched
	}
	if s.TryTempUnschedulable(ctx, provider, statusCode, responseBody, escalateRepeated401, model) {
		return ErrorPolicyTempUnscheduled
	}
	return ErrorPolicyNone
}

// ApplyAuthenticationFailure notifies the configured scheduling blocker and
// attempts to store the provider error message. Supply a usable service and
// provider record. Storage failure is logged after the notification and returns
// without reporting persistent success.
//
// The blocker receives a zero deadline and the auth_error reason; its
// implementation decides platform eligibility and any default runtime lifetime.
// The store owns error status, scheduling effects and publication. This command
// neither rolls back an earlier notification nor returns storage failure to its
// caller.
func (s *HealthService) ApplyAuthenticationFailure(ctx context.Context, provider *Record, errorMsg string) {
	// The repository also disables the stored scheduling switch and publishes
	// health separately. Notify first so the configured blocker can act before
	// those storage and publication operations complete.
	s.notifyProviderSchedulingBlocked(provider, time.Time{}, "auth_error")
	if err := s.providerRepo.SetError(ctx, provider.ID, errorMsg); err != nil {
		s.options.Warn("provider_set_error_failed", "provider_id", provider.ID, "error", err)
		return
	}
	s.options.Warn("provider_disabled_auth_error", "provider_id", provider.ID, "error", errorMsg)
}

// ApplyCustomErrorCode prefixes the upstream message with its matched status and
// attempts a persistent provider error. Call after the configured code policy
// selects a match. The configured blocker is notified first, so its runtime
// effect can survive a later store failure.
//
// Storage owns the resulting error status and scheduling publication. This
// command logs both persistence failures and completed custom-error updates,
// returning no error to its caller. The caller retains the already-selected
// failover action independently of that persistent write outcome.
func (s *HealthService) ApplyCustomErrorCode(ctx context.Context, provider *Record, statusCode int, errorMsg string) {
	msg := "Custom error code " + strconv.Itoa(statusCode) + ": " + errorMsg
	// The repository also disables the stored scheduling switch and publishes
	// health separately. Notify first so the configured blocker can act before
	// those storage and publication operations complete.
	s.notifyProviderSchedulingBlocked(provider, time.Time{}, "custom_error_code")
	if err := s.providerRepo.SetError(ctx, provider.ID, msg); err != nil {
		s.options.Warn("provider_set_error_failed", "provider_id", provider.ID, "status_code", statusCode, "error", err)
		return
	}
	s.options.Warn("provider_disabled_custom_error", "provider_id", provider.ID, "status_code", statusCode, "error", errorMsg)
}

// ApplyOverload reads runtime policy and attempts a provider overload deadline
// for HTTP 529. A disabled policy leaves health unchanged. Missing or failed
// runtime reads use enabled process configuration, with ten minutes selected
// when its duration is nonpositive.
//
// An enabled nonpositive loaded duration also uses ten minutes. The configured
// blocker is notified before persistence, so a store failure can leave its
// runtime effect installed. The caller receives no error; logging distinguishes
// ignored policy, failed persistence and a completed overload update.
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
	// Missing readers, nil results and failed reads reach the same process-policy
	// path. An unavailable runtime policy therefore still gives overload handling
	// an enabled cooldown.
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

	// Loaded or process policy supplies minutes; resolve the absolute stored
	// overload deadline from the health service's clock.
	until := s.options.Now().Add(time.Duration(cooldownMinutes) * time.Minute)
	// SetOverloaded replaces the stored deadline and then publishes its state. The
	// blocker runs before that work; a logged store failure leaves any earlier
	// runtime notification in effect.
	s.notifyProviderSchedulingBlocked(provider, until, "529")
	if err := s.providerRepo.SetOverloaded(ctx, provider.ID, until); err != nil {
		s.options.Warn("overload_set_failed", "provider_id", provider.ID, "error", err)
		return
	}

	s.options.Info("provider_overloaded", "provider_id", provider.ID, "until", until)
}
