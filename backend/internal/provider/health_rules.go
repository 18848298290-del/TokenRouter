package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const (
	TempUnschedBodyMaxBytes    = 64 << 10
	TempUnschedMessageMaxBytes = 2048
)

// HealthStore supplies independent provider health reads and writes.
type HealthStore interface {
	GetByID(context.Context, int64) (*Record, error)
	SetError(context.Context, int64, string) error
	SetOverloaded(context.Context, int64, time.Time) error
	SetRateLimited(context.Context, int64, time.Time) error
	SetTempUnschedulable(context.Context, int64, time.Time, string) error
	SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error
}
type HealthOptions struct {
	APIKeyHealthCounter         OpenAIAPIKeyHealthCache
	APIKeyHealthSettings        func(context.Context) (*OpenAIAPIKeyHealthBreakerSettings, error)
	APIKeyHealthWarn            func(string, ...any)
	UnauthorizedCooldownMinutes int
	InvalidateUnauthorizedToken func(context.Context, *Record) error
	SessionWindows              SessionWindowStore
	ClearWindowRateLimit        func(context.Context, int64) error
	RateLimit429Settings        func(context.Context) (*RateLimit429CooldownSettings, error)
	CNIntervalMinutes           int
	ForbiddenCounter            OpenAI403CounterCache
	ForbiddenSettings           func(context.Context) (*OpenAI403CooldownSettings, error)
	OverloadMinutes             int
	OverloadSettings            func(context.Context) (*OverloadCooldownSettings, error)
	HasThresholdSettings        func() bool
	Thresholds                  func(context.Context) map[string]int
	Now                         func() time.Time
	Warn, Info                  func(string, ...any)
	StreamSettings              func(context.Context) (*StreamTimeoutSettings, error, bool)
	Block                       func(*Record, time.Time, string)
	TimeoutCounter              TimeoutCounterCache
}

// HealthService applies common health rules and stream-timeout thresholds to errors classified by callers.
type HealthService struct {
	providerRepo        HealthStore
	tempUnschedCache    TempUnschedCache
	timeoutCounterCache TimeoutCounterCache
	options             HealthOptions
}

type TempUnschedulableRuleMatch struct {
	Rule           TempUnschedulableRule
	RuleIndex      int
	MatchedKeyword string
}

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

func (s *HealthService) notifyProviderSchedulingBlocked(v *Record, until time.Time, reason string) {
	if s.options.Block != nil {
		s.options.Block(v, until, reason)
	}
}

func firstRequestedModel(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func MatchTempUnschedulableRules(provider *Record, statusCode int, responseBody []byte) []TempUnschedulableRuleMatch {
	if provider == nil || !provider.IsTempUnschedulableEnabled() || statusCode <= 0 || len(responseBody) == 0 {
		return nil
	}
	rules := provider.GetTempUnschedulableRules()
	if len(rules) == 0 {
		return nil
	}
	body := responseBody
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

func WasTempUnschedByStatusCode(reason string, statusCode int) bool {
	if statusCode <= 0 {
		return false
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return false
	}

	var state TempUnschedState
	if err := json.Unmarshal([]byte(reason), &state); err != nil {
		return false
	}
	return state.StatusCode == statusCode
}

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

func TruncateTempUnschedMessage(body []byte, maxBytes int) string {
	if maxBytes <= 0 || len(body) == 0 {
		return ""
	}
	if len(body) > maxBytes {
		body = body[:maxBytes]
	}
	return strings.TrimSpace(string(body))
}

func (s *HealthService) TriggerTempUnschedulable(ctx context.Context, provider *Record, rule TempUnschedulableRule, ruleIndex int, statusCode int, matchedKeyword string, responseBody []byte, requestedModel ...string) bool {
	if provider == nil {
		return false
	}
	if rule.DurationMinutes <= 0 {
		return false
	}

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

	reason := ""
	if raw, err := json.Marshal(state); err == nil {
		reason = string(raw)
	}
	if reason == "" {
		reason = strings.TrimSpace(state.ErrorMessage)
	}

	// A known model scopes the cooldown to this provider/model pair.
	// Authentication failures and unknown models use the provider-wide pause below.
	modelKey := firstRequestedModel(requestedModel)
	if modelKey != "" && statusCode != 401 {
		if err := s.providerRepo.SetModelRateLimit(ctx, provider.ID, modelKey, until, reason); err != nil {
			s.options.Warn("temp_unsched_model_rate_limit_set_failed", "provider_id", provider.ID, "model", modelKey, "error", err)
			// The rule still requires failover if persistence fails; keep the failure model-scoped.
			return true
		}
		s.options.Info("provider_model_temp_unschedulable", "provider_id", provider.ID, "model", modelKey, "until", until, "rule_index", ruleIndex, "status_code", statusCode)
		return true
	}

	s.notifyProviderSchedulingBlocked(provider, until, "temp_unschedulable")
	if err := s.providerRepo.SetTempUnschedulable(ctx, provider.ID, until, reason); err != nil {
		s.options.Warn("temp_unsched_set_failed", "provider_id", provider.ID, "error", err)
		return false
	}

	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(ctx, provider.ID, state); err != nil {
			s.options.Warn("temp_unsched_cache_set_failed", "provider_id", provider.ID, "error", err)
		}
	}

	s.options.Info("provider_temp_unschedulable", "provider_id", provider.ID, "until", until, "rule_index", ruleIndex, "status_code", statusCode)
	return true
}

// TryTempUnschedulable applies matching pause rules and optionally escalates repeated 401 responses.
// Pool-mode callers can apply their configured 401 pause without escalation.
func (s *HealthService) TryTempUnschedulable(ctx context.Context, provider *Record, statusCode int, responseBody []byte, escalateRepeated401 bool, requestedModel ...string) bool {
	if provider == nil {
		return false
	}
	if !provider.IsTempUnschedulableEnabled() {
		return false
	}
	// A first 401 pause gives token refresh time to run. If escalation is enabled and the
	// previous pause was also a 401, return false so the caller applies its default error action.
	if escalateRepeated401 && statusCode == 401 {
		reason := provider.TempUnschedulableReason
		// Load the persisted reason when the selected provider snapshot omits it.
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
	for _, match := range MatchTempUnschedulableRules(provider, statusCode, responseBody) {
		if s.TriggerTempUnschedulable(ctx, provider, match.Rule, match.RuleIndex, statusCode, match.MatchedKeyword, responseBody, firstRequestedModel(requestedModel)) {
			return true
		}
	}

	return false
}

// HandleStreamTimeout counts timeouts and applies the configured action when the threshold is reached.
// It reports whether the resulting health action requests a scheduling stop.
func (s *HealthService) HandleStreamTimeout(ctx context.Context, provider *Record, model string) bool {
	if provider == nil {
		return false
	}

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

	var count int64 = 1
	if s.timeoutCounterCache != nil {
		count, err = s.timeoutCounterCache.IncrementTimeoutCount(ctx, provider.ID, settings.ThresholdWindowMinutes)
		if err != nil {
			s.options.Warn("stream_timeout_increment_count_failed", "provider_id", provider.ID, "error", err)
			// Treat an unavailable counter as one failure for this decision.
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

// TriggerStreamTimeoutTempUnsched persists a temporary scheduling pause and resets the timeout count.
func (s *HealthService) TriggerStreamTimeoutTempUnsched(ctx context.Context, provider *Record, settings *StreamTimeoutSettings, model string) bool {
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

	reason := ""
	if raw, err := json.Marshal(state); err == nil {
		reason = string(raw)
	}
	if reason == "" {
		reason = state.ErrorMessage
	}

	s.notifyProviderSchedulingBlocked(provider, until, "stream_timeout_temp_unschedulable")
	if err := s.providerRepo.SetTempUnschedulable(ctx, provider.ID, until, reason); err != nil {
		s.options.Warn("stream_timeout_set_temp_unsched_failed", "provider_id", provider.ID, "error", err)
		return false
	}

	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(ctx, provider.ID, state); err != nil {
			s.options.Warn("stream_timeout_set_temp_unsched_cache_failed", "provider_id", provider.ID, "error", err)
		}
	}

	if s.timeoutCounterCache != nil {
		if err := s.timeoutCounterCache.ResetTimeoutCount(ctx, provider.ID); err != nil {
			s.options.Warn("stream_timeout_reset_count_failed", "provider_id", provider.ID, "error", err)
		}
	}

	s.options.Info("stream_timeout_temp_unschedulable", "provider_id", provider.ID, "until", until, "model", model)
	return true
}

// TriggerStreamTimeoutError marks the provider as errored and resets the timeout count.
func (s *HealthService) TriggerStreamTimeoutError(ctx context.Context, provider *Record, model string) bool {
	errorMsg := "Stream data interval timeout (repeated failures) for model: " + model

	s.notifyProviderSchedulingBlocked(provider, time.Time{}, "stream_timeout_error")
	if err := s.providerRepo.SetError(ctx, provider.ID, errorMsg); err != nil {
		s.options.Warn("stream_timeout_set_error_failed", "provider_id", provider.ID, "error", err)
		return false
	}

	if s.timeoutCounterCache != nil {
		if err := s.timeoutCounterCache.ResetTimeoutCount(ctx, provider.ID); err != nil {
			s.options.Warn("stream_timeout_reset_count_failed", "provider_id", provider.ID, "error", err)
		}
	}

	s.options.Warn("stream_timeout_provider_error", "provider_id", provider.ID, "model", model)
	return true
}

// HandleTempUnschedulable applies error-code policy and enables repeated-401 escalation outside pool mode and
// Antigravity.
func (s *HealthService) HandleTempUnschedulable(ctx context.Context, value *Record, status int, body []byte, model string) bool {
	if value == nil || !value.ShouldHandleErrorCode(status) {
		return false
	}
	return s.TryTempUnschedulable(ctx, value, status, body, !value.IsPoolMode() && value.Platform != PlatformAntigravity, model)
}
