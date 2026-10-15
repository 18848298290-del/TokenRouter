package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// health_rules.go matches configured temporary-pause rules and applies
// stream-timeout threshold actions. Configured temporary pauses match an HTTP
// status and keyword against a bounded response prefix. Matching retains
// parsed rule order, and handling stops at the first rule whose trigger
// returns true. A supplied model narrows non-401 failures to a model cooldown;
// authentication failures still use provider-wide health.
//
// Provider-wide triggers notify runtime blocking before storage and publish
// cached pause metadata afterward. These steps can fail separately, so a false
// result can follow an installed runtime block. Stream timeouts first select
// an enabled action and compare shared counts with its threshold; unavailable
// counting contributes one failure, while counter reset is attempted only
// after the primary health write succeeds.

// TempUnschedBodyMaxBytes and TempUnschedMessageMaxBytes cap response
// inspection and retained diagnostics separately when temporary pause rules are
// handled.
const (
	TempUnschedBodyMaxBytes    = 64 << 10 // inspected body bytes
	TempUnschedMessageMaxBytes = 2048     // retained diagnostic bytes
)

// HealthStore supplies provider reads and independent writes of persistent
// health state. Production writes select by provider ID; temporary pauses also
// compare their deadline. These calls carry no credential observation, so
// conditional refresh publication uses separate writer interfaces.
type HealthStore interface {
	// GetByID reads the provider record used to recover omitted health metadata.
	GetByID(context.Context, int64) (*Record, error)
	// SetError writes error status and the supplied diagnostic for one provider.
	// The production writer also sets schedulable=false, so later message/status
	// cleanup leaves scheduling eligibility dependent on that retained switch.
	SetError(context.Context, int64, string) error
	// SetOverloaded replaces the overload deadline for the selected provider.
	// The production writer uses the supplied time unchanged, so a later call can
	// shorten a previously recorded overload window.
	SetOverloaded(context.Context, int64, time.Time) error
	// SetRateLimited replaces the provider-wide reset used by scheduling checks.
	// The production writer records a new observation time and can shorten an
	// existing window when the supplied reset is earlier.
	SetRateLimited(context.Context, int64, time.Time) error
	// SetTempUnschedulable attempts a provider pause with its diagnostic reason.
	// The PostgreSQL implementation retains an equal or longer existing pause;
	// that case and an absent row return nil, so success need not mean replacement.
	SetTempUnschedulable(context.Context, int64, time.Time, string) error
	// SetModelRateLimit writes one named model or scope cooldown with optional
	// reason text. The PostgreSQL implementation returns nil for an empty scope,
	// otherwise replacing that scope while retaining other scopes and Extra keys.
	// It considers only the first reason and stores its trimmed nonblank text.
	SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error
}

// HealthOptions supplies health policy readers, observation stores, and runtime
// callbacks.
type HealthOptions struct {
	// APIKeyHealthCounter and APIKeyHealthSettings jointly enable rolling API Key
	// failure handling. ApplyAPIKeyHealthFailure skips its breaker when either
	// dependency is absent.
	APIKeyHealthCounter OpenAIAPIKeyHealthCache // shared rolling failures
	// breaker policy reader
	APIKeyHealthSettings func(context.Context) (*OpenAIAPIKeyHealthBreakerSettings, error)
	// APIKeyHealthWarn receives breaker diagnostics. NewHealthService uses Warn
	// when this callback is absent, allowing separate breaker logging when
	// configured.
	APIKeyHealthWarn func(string, ...any)
	// UnauthorizedCooldownMinutes supplies the OAuth 401 pause duration.
	// ApplyUnauthorized selects ten minutes when this value is nonpositive.
	UnauthorizedCooldownMinutes int
	// InvalidateUnauthorizedToken optionally removes cached authentication before
	// OAuth eligibility is checked. Failures are logged and leave later health
	// handling available.
	InvalidateUnauthorizedToken func(context.Context, *Record) error
	// SessionWindows stores window columns and Extra observations. Configure it
	// for reached window, passive usage, Codex snapshot, or Antigravity
	// force-refresh writes.
	SessionWindows SessionWindowStore
	// ClearWindowRateLimit optionally recovers a live provider limit after exact
	// allowed session observations. Its error is logged after observation writes
	// have been attempted.
	ClearWindowRateLimit func(context.Context, int64) error
	// RateLimit429Settings reads reset-less 429 policy. Missing, nil, or failed
	// reads keep Fallback429Cooldown's built-in enabled policy available.
	RateLimit429Settings func(context.Context) (*RateLimit429CooldownSettings, error)
	// CNIntervalMinutes is the configured monitor interval used by
	// CNBalanceCooldownDuration to derive the insufficient-balance cooldown and
	// resolve its defaults.
	CNIntervalMinutes int
	// ForbiddenCounter supplies shared OpenAI 403 counts. With threshold handling
	// enabled, an absent counter selects authentication failure rather than a
	// verified subthreshold pause.
	ForbiddenCounter OpenAI403CounterCache
	// ForbiddenSettings reads OpenAI 403 policy. Missing or failed reads select
	// defaults; HealthService.ForbiddenSettings normalizes a nonpositive loaded
	// cooldown by mutating the returned settings pointer.
	ForbiddenSettings func(context.Context) (*OpenAI403CooldownSettings, error)
	// OverloadSettings takes precedence over OverloadMinutes when its read
	// succeeds with a nonnil policy. Missing or failed reads select the process
	// duration, with nonpositive durations resolved by ApplyOverload.
	OverloadMinutes int // process overload minutes
	// runtime overload policy
	OverloadSettings func(context.Context) (*OverloadCooldownSettings, error)
	// HasThresholdSettings gates evaluation through Thresholds. When the
	// availability callback returns true, Thresholds must be callable and supplies
	// the platform defaults for the current evaluation.
	// threshold reader availability
	HasThresholdSettings func() bool
	// platform threshold values
	Thresholds func(context.Context) map[string]int
	// Now timestamps health observations and deadlines. NewHealthService supplies
	// the default clock when this callback is absent.
	Now func() time.Time
	// Warn and Info receive health diagnostics at their respective levels.
	// NewHealthService independently replaces either missing callback with a no-op
	// before storing options.
	Warn, Info func(string, ...any) // health diagnostic callbacks
	// StreamSettings returns timeout policy and reader availability. An available
	// successful read must return a nonnil settings pointer for
	// HandleStreamTimeout.
	StreamSettings func(context.Context) (*StreamTimeoutSettings, error, bool)
	// Block optionally receives synchronous runtime scheduling notifications.
	// Individual health methods determine whether it runs before or after their
	// persistent write.
	Block func(*Record, time.Time, string)
	// TimeoutCounter provides shared stream-timeout history and reset operations.
	// Without it, HandleStreamTimeout evaluates each enabled observation as one
	// failure.
	TimeoutCounter TimeoutCounterCache
}

// HealthService applies shared pause rules and platform health policy to
// classified observations.
type HealthService struct {
	// providerRepo performs the primary health writes. Callers configure it for
	// reached commands; guarded methods return early when the store is absent.
	providerRepo HealthStore
	// tempUnschedCache optionally publishes provider pause metadata after
	// successful storage. Cache failures are logged separately from the already
	// completed primary write.
	tempUnschedCache TempUnschedCache
	// timeoutCounterCache retains options.TimeoutCounter from construction.
	// Timeout handling uses it for counting and best-effort reset after successful
	// primary health writes.
	timeoutCounterCache TimeoutCounterCache
	options             HealthOptions // normalized health dependencies
}

// TempUnschedulableRuleMatch identifies a parsed matching rule and its first
// usable keyword.
type TempUnschedulableRuleMatch struct {
	// Rule holds parsed rule data, including its keyword slice. Copies of this
	// match share that slice, so copy it before changing keyword entries
	// independently.
	Rule           TempUnschedulableRule // parsed matching rule
	RuleIndex      int                   // index among parsed valid rules
	MatchedKeyword string                // first matched keyword text
}

// NewHealthService stores the supplied health dependencies and normalizes its
// diagnostic callbacks. Missing Now uses time.Now, missing Warn and Info become
// no-ops, and missing APIKeyHealthWarn uses Warn. Optional stores and policy
// readers retain their supplied values.
func NewHealthService(store HealthStore, cache TempUnschedCache, options HealthOptions) *HealthService {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Warn == nil {
		options.Warn = func(string, ...any) {}
	}
	if options.APIKeyHealthWarn == nil {
		options.APIKeyHealthWarn = options.Warn
	}
	if options.Info == nil {
		options.Info = func(string, ...any) {}
	}
	return &HealthService{providerRepo: store, tempUnschedCache: cache, timeoutCounterCache: options.TimeoutCounter, options: options}
}

// notifyProviderSchedulingBlocked passes the record, deadline, and reason to an
// optional synchronous Block callback. A missing callback skips notification;
// individual health commands determine its ordering relative to persistence.
func (s *HealthService) notifyProviderSchedulingBlocked(v *Record, until time.Time, reason string) {
	if s.options.Block != nil {
		s.options.Block(v, until, reason)
	}
}

// firstRequestedModel returns the trimmed first supplied model, or empty when
// the slice is empty. A blank first entry remains empty even when later entries
// contain usable model names.
func firstRequestedModel(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

// MatchTempUnschedulableRules returns every status-and-keyword match in parsed
// rule order for an enabled provider. Missing inputs return nil, and inspection
// uses at most TempUnschedBodyMaxBytes of the response. RuleIndex addresses the
// valid parsed rule list; each match retains its first keyword's trimmed
// spelling.
func MatchTempUnschedulableRules(provider *Record, statusCode int, responseBody []byte) []TempUnschedulableRuleMatch {
	if provider == nil || !provider.IsTempUnschedulableEnabled() || statusCode <= 0 || len(responseBody) == 0 {
		return nil
	}
	rules := provider.GetTempUnschedulableRules()
	if len(rules) == 0 {
		return nil
	}
	body := responseBody
	// Keywords beyond the inspected response prefix cannot trigger rules.
	if len(body) > TempUnschedBodyMaxBytes {
		body = body[:TempUnschedBodyMaxBytes]
	}
	bodyLower := strings.ToLower(string(body))
	matches := make([]TempUnschedulableRuleMatch, 0, 1)
	for idx, rule := range rules {
		if rule.ErrorCode != statusCode || len(rule.Keywords) == 0 {
			continue
		}
		matchedKeyword := MatchTempUnschedKeyword(bodyLower, rule.Keywords)
		if matchedKeyword == "" {
			continue
		}
		matches = append(matches, TempUnschedulableRuleMatch{Rule: rule, RuleIndex: idx, MatchedKeyword: matchedKeyword})
	}
	return matches
}

// WasTempUnschedByStatusCode reports whether JSON pause metadata records the
// requested positive status code. Blank, plain-text, or malformed reasons
// return false; the stored deadline is not checked when recognizing a prior
// status.
func WasTempUnschedByStatusCode(reason string, statusCode int) bool {
	if statusCode <= 0 {
		return false
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return false
	}

	var state TempUnschedState
	// A legacy or unreadable diagnostic cannot establish a previous HTTP status.
	// Return false so repeated-status escalation remains unselected for that
	// reason.
	if err := json.Unmarshal([]byte(reason), &state); err != nil {
		return false
	}
	return state.StatusCode == statusCode
}

// MatchTempUnschedKeyword returns the first nonblank keyword contained in an
// already lowercased body. It trims and lowercases each keyword for comparison
// but returns its trimmed original spelling; missing matches return empty.
func MatchTempUnschedKeyword(bodyLower string, keywords []string) string {
	if bodyLower == "" {
		return ""
	}
	for _, keyword := range keywords {
		k := strings.TrimSpace(keyword)
		if k == "" {
			continue
		}
		if strings.Contains(bodyLower, strings.ToLower(k)) {
			return k
		}
	}
	return ""
}

// TruncateTempUnschedMessage caps retained response bytes before trimming
// surrounding whitespace. Empty input or a nonpositive limit returns empty; the
// byte cut can split UTF-8 because this helper does not repair character
// boundaries.
func TruncateTempUnschedMessage(body []byte, maxBytes int) string {
	if maxBytes <= 0 || len(body) == 0 {
		return ""
	}
	// Leading whitespace consumes the byte budget even though the final message
	// removes that whitespace after truncation.
	if len(body) > maxBytes {
		body = body[:maxBytes]
	}
	return strings.TrimSpace(string(body))
}

// TriggerTempUnschedulable applies one positive-duration rule and reports its
// handled result. Nil records and nonpositive durations return false; other
// calls require a configured store and clock. The first trimmed requested model
// narrows non-401 responses to a model cooldown, whose write failure is logged
// but still returns true.
//
// Provider-wide handling notifies runtime blocking before its pause write. A
// failed primary write returns false and can leave that notification installed;
// a successful call attempts optional cached metadata, whose failure does not
// change true. Structured reason encoding falls back to the bounded diagnostic
// if needed, retaining readable state for persistence.
func (s *HealthService) TriggerTempUnschedulable(ctx context.Context, provider *Record, rule TempUnschedulableRule, ruleIndex int, statusCode int, matchedKeyword string, responseBody []byte, requestedModel ...string) bool {
	if provider == nil {
		return false
	}
	if rule.DurationMinutes <= 0 {
		return false
	}

	// Use one clock sample for the trigger timestamp and minute-based deadline.
	now := s.options.Now()
	until := now.Add(time.Duration(rule.DurationMinutes) * time.Minute)

	state := &TempUnschedState{
		UntilUnix:       until.Unix(),
		TriggeredAtUnix: now.Unix(),
		StatusCode:      statusCode,
		MatchedKeyword:  matchedKeyword,
		RuleIndex:       ruleIndex,
		ErrorMessage:    TruncateTempUnschedMessage(responseBody, TempUnschedMessageMaxBytes),
	}

	// Keep the bounded diagnostic available if structured reason encoding cannot
	// supply text. That plain-text fallback remains readable but does not carry a
	// status for repeated-401 recognition.
	reason := ""
	if raw, err := json.Marshal(state); err == nil {
		reason = string(raw)
	}
	if reason == "" {
		reason = strings.TrimSpace(state.ErrorMessage)
	}

	// A supplied model confines non-authentication failures to that provider/model
	// pair. A 401 bypasses this scope because authentication failure affects every
	// model using those credentials.
	modelKey := firstRequestedModel(requestedModel)
	if modelKey != "" && statusCode != 401 {
		if err := s.providerRepo.SetModelRateLimit(ctx, provider.ID, modelKey, until, reason); err != nil {
			s.options.Warn("temp_unsched_model_rate_limit_set_failed", "provider_id", provider.ID, "model", modelKey, "error", err)
			// A matched model rule still ends rule handling when its write fails. Return
			// true here so the failure does not acquire a provider-wide pause.
			return true
		}
		s.options.Info("provider_model_temp_unschedulable", "provider_id", provider.ID, "model", modelKey, "until", until, "rule_index", ruleIndex, "status_code", statusCode)
		return true
	}

	// Notify runtime blocking before the primary write. Store failure returns
	// false while the notification can remain installed, and successful
	// persistence permits the following cache publication.
	s.notifyProviderSchedulingBlocked(provider, until, "temp_unschedulable")
	if err := s.providerRepo.SetTempUnschedulable(ctx, provider.ID, until, reason); err != nil {
		s.options.Warn("temp_unsched_set_failed", "provider_id", provider.ID, "error", err)
		return false
	}

	// Publish cached metadata after primary storage succeeds. A failed cache
	// update is logged while the persisted health result remains handled for this
	// attempt.
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(ctx, provider.ID, state); err != nil {
			s.options.Warn("temp_unsched_cache_set_failed", "provider_id", provider.ID, "error", err)
		}
	}

	s.options.Info("provider_temp_unschedulable", "provider_id", provider.ID, "until", until, "rule_index", ruleIndex, "status_code", statusCode)
	return true
}

// TryTempUnschedulable evaluates enabled rules until one trigger reports true.
// Nil or disabled providers return false. With escalateRepeated401 enabled,
// recognized prior 401 metadata returns false before rule matching so the
// caller can apply its default authentication action.
//
// A nonempty snapshot reason is used directly; only an empty reason permits a
// store lookup, whose error or missing record leaves prior status unrecognized.
// Matching rules keep their parsed order. A provider-wide write failure can
// leave runtime blocking installed while allowing a later rule to run, whereas
// a model-scoped write failure returns true and ends this loop.
func (s *HealthService) TryTempUnschedulable(ctx context.Context, provider *Record, statusCode int, responseBody []byte, escalateRepeated401 bool, requestedModel ...string) bool {
	if provider == nil {
		return false
	}
	if !provider.IsTempUnschedulableEnabled() {
		return false
	}
	// Recognized previous 401 metadata gives the caller its default authentication
	// path on repetition. Pool callers can disable this check to keep applying
	// their configured pause.
	if escalateRepeated401 && statusCode == 401 {
		reason := provider.TempUnschedulableReason
		// An omitted snapshot reason permits a persistent lookup. Read failure or a
		// missing row leaves recognition unavailable, so configured rule handling can
		// still proceed.
		if reason == "" {
			if dbAcc, err := s.providerRepo.GetByID(ctx, provider.ID); err == nil && dbAcc != nil {
				reason = dbAcc.TempUnschedulableReason
			}
		}
		if WasTempUnschedByStatusCode(reason, statusCode) {
			s.options.Info("401_escalated_to_error", "provider_id", provider.ID,
				"reason", "previous temp-unschedulable was also 401")
			return false
		}
	}
	// A failed provider-wide trigger leaves later configured matches eligible for
	// another attempt. Its earlier runtime notification remains even when this
	// loop continues.
	for _, match := range MatchTempUnschedulableRules(provider, statusCode, responseBody) {
		if s.TriggerTempUnschedulable(ctx, provider, match.Rule, match.RuleIndex, statusCode, match.MatchedKeyword, responseBody, firstRequestedModel(requestedModel)) {
			return true
		}
	}

	return false
}

// HandleStreamTimeout reports whether enabled timeout policy successfully
// applies its selected health action. Missing or unavailable settings, read
// failure, disabled policy, and action none return false before counting. An
// available successful settings read supplies a nonnil policy.
//
// An absent or failed counter contributes one timeout for this decision.
// Otherwise shared fixed-window history is compared with ThresholdCount, and a
// subthreshold count leaves health unchanged. Reaching the threshold dispatches
// temporary pause or error handling; unknown actions return false after
// counting. Those handlers reset history only after a successful primary health
// write.
func (s *HealthService) HandleStreamTimeout(ctx context.Context, provider *Record, model string) bool {
	if provider == nil {
		return false
	}

	// Read policy before touching shared timeout history, so disabled or
	// unavailable policy cannot accumulate observations that later contribute to
	// an enabled action.
	if s.options.StreamSettings == nil {
		s.options.Warn("stream_timeout_setting_service_missing", "provider_id", provider.ID)
		return false
	}
	settings, err, available := s.options.StreamSettings(ctx)
	if !available {
		s.options.Warn("stream_timeout_setting_service_missing", "provider_id", provider.ID)
		return false
	}
	if err != nil {
		s.options.Warn("stream_timeout_get_settings_failed", "provider_id", provider.ID, "error", err)
		return false
	}

	if !settings.Enabled {
		return false
	}

	if settings.Action == StreamTimeoutActionNone {
		return false
	}

	// Without shared accounting, this observation contributes one failure. Use the
	// same single-failure decision when a counter error cannot provide a usable
	// history count.
	var count int64 = 1
	if s.timeoutCounterCache != nil {
		count, err = s.timeoutCounterCache.IncrementTimeoutCount(ctx, provider.ID, settings.ThresholdWindowMinutes)
		if err != nil {
			s.options.Warn("stream_timeout_increment_count_failed", "provider_id", provider.ID, "error", err)
			count = 1
		}
	}

	s.options.Info("stream_timeout_count", "provider_id", provider.ID, "count", count, "threshold", settings.ThresholdCount, "window_minutes", settings.ThresholdWindowMinutes, "model", model)

	if count < int64(settings.ThresholdCount) {
		return false
	}

	switch settings.Action {
	case StreamTimeoutActionTempUnsched:
		return s.TriggerStreamTimeoutTempUnsched(ctx, provider, settings, model)
	case StreamTimeoutActionError:
		return s.TriggerStreamTimeoutError(ctx, provider, model)
	default:
		return false
	}
}

// TriggerStreamTimeoutTempUnsched writes a system-rule pause for a stream
// timeout and reports whether the primary store call succeeds. Callers supply a
// record, store, clock, and nonnil settings; this method uses
// TempUnschedMinutes directly. Its metadata uses status zero and rule index
// minus one to distinguish this action from HTTP rules.
//
// Runtime blocking precedes storage. A store error returns false and skips
// cache publication and counter reset, while the notification may remain
// installed. After a successful write, optional cache and counter operations
// are attempted independently; their logged failures leave the true result
// intact.
func (s *HealthService) TriggerStreamTimeoutTempUnsched(ctx context.Context, provider *Record, settings *StreamTimeoutSettings, model string) bool {
	// The system pause duration and its trigger metadata share this clock sample.
	now := s.options.Now()
	until := now.Add(time.Duration(settings.TempUnschedMinutes) * time.Minute)

	state := &TempUnschedState{
		UntilUnix:       until.Unix(),
		TriggeredAtUnix: now.Unix(),
		StatusCode:      0, // A stream timeout has no HTTP status.
		MatchedKeyword:  "stream_timeout",
		RuleIndex:       -1, // A system rule, outside the provider's rule list.
		ErrorMessage:    "Stream data interval timeout for model: " + model,
	}

	// Retain the timeout diagnostic when structured encoding cannot supply a
	// reason. Persistence then receives readable text even though system-rule
	// metadata is unavailable in that fallback.
	reason := ""
	if raw, err := json.Marshal(state); err == nil {
		reason = string(raw)
	}
	if reason == "" {
		reason = state.ErrorMessage
	}

	// Block selection before storing the pause. A failed write leaves counting
	// history available and prevents auxiliary cache publication and reset on this
	// action.
	s.notifyProviderSchedulingBlocked(provider, until, "stream_timeout_temp_unschedulable")
	if err := s.providerRepo.SetTempUnschedulable(ctx, provider.ID, until, reason); err != nil {
		s.options.Warn("stream_timeout_set_temp_unsched_failed", "provider_id", provider.ID, "error", err)
		return false
	}

	// Cache publication remains auxiliary to the timeout action. If it fails,
	// retain the persisted pause and continue to timeout-history cleanup below.
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(ctx, provider.ID, state); err != nil {
			s.options.Warn("stream_timeout_set_temp_unsched_cache_failed", "provider_id", provider.ID, "error", err)
		}
	}

	// Reset shared failure history only after the primary health write succeeds.
	// Cleanup errors are logged and leave the completed action's true result
	// unchanged.
	if s.timeoutCounterCache != nil {
		if err := s.timeoutCounterCache.ResetTimeoutCount(ctx, provider.ID); err != nil {
			s.options.Warn("stream_timeout_reset_count_failed", "provider_id", provider.ID, "error", err)
		}
	}

	s.options.Info("stream_timeout_temp_unschedulable", "provider_id", provider.ID, "until", until, "model", model)
	return true
}

// TriggerStreamTimeoutError marks a provider errored for repeated stream
// failures and reports whether its primary store call succeeds. Callers supply
// a record and configured store; the model name contributes to the stored
// diagnostic, while runtime notification receives a zero deadline.
//
// The blocker runs before persistence, so a store error can leave runtime state
// installed while returning false. Timeout history is reset only after the
// error write succeeds. A missing counter skips cleanup, and a failed reset is
// logged without changing the true result of the completed primary write.
func (s *HealthService) TriggerStreamTimeoutError(ctx context.Context, provider *Record, model string) bool {
	errorMsg := "Stream data interval timeout (repeated failures) for model: " + model

	// Install the runtime notification before the permanent health write. Storage
	// failure keeps timeout history for subsequent decisions and leaves the
	// callback's earlier effect available.
	s.notifyProviderSchedulingBlocked(provider, time.Time{}, "stream_timeout_error")
	if err := s.providerRepo.SetError(ctx, provider.ID, errorMsg); err != nil {
		s.options.Warn("stream_timeout_set_error_failed", "provider_id", provider.ID, "error", err)
		return false
	}

	// Error state is already recorded; resetting shared history permits a fresh
	// timeout window after recovery. Failed cleanup is logged and leaves this
	// action handled.
	if s.timeoutCounterCache != nil {
		if err := s.timeoutCounterCache.ResetTimeoutCount(ctx, provider.ID); err != nil {
			s.options.Warn("stream_timeout_reset_count_failed", "provider_id", provider.ID, "error", err)
		}
	}

	s.options.Warn("stream_timeout_provider_error", "provider_id", provider.ID, "model", model)
	return true
}

// HandleTempUnschedulable applies configured temporary rules after the record's
// error-code policy admits status. Nil records or skipped codes return false;
// repeated-401 escalation is enabled outside pool mode except for Antigravity,
// and the model scope is passed to TryTempUnschedulable.
func (s *HealthService) HandleTempUnschedulable(ctx context.Context, value *Record, status int, body []byte, model string) bool {
	if value == nil || !value.ShouldHandleErrorCode(status) {
		return false
	}
	return s.TryTempUnschedulable(ctx, value, status, body, !value.IsPoolMode() && value.Platform != PlatformAntigravity, model)
}
