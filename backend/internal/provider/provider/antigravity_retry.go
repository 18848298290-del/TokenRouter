package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// AntigravityRetryRequest contains one attempt and its synchronous observation and health callbacks.
type AntigravityRetryRequest struct {
	ModelStore                                            provider.AntigravityHealthStore
	Do                                                    func(*http.Request) (*http.Response, error)
	LogConfig                                             func() (bool, int)
	PolicyModelFallback                                   string
	Context                                               context.Context
	Provider                                              *provider.Record
	Prefix, ProxyURL, AccessToken, Action, RequestedModel string
	Body                                                  []byte
	Thinking                                              *bool
	SingleProvider, Sticky                                bool
	UserAgent                                             string
	Observe                                               func(antigravity.RetryObservation)
	SetError                                              func(int, string, string)
	HandleError                                           func(int, http.Header, []byte)
	ClearSticky                                           func()
	Changed                                               func(*provider.Record)
}

// AntigravityRetry binds the platform retry loop to provider health services.
type AntigravityRetry struct {
	Health         *provider.AntigravityHealth
	Policy         *provider.HealthService
	Do             func(*http.Request, string, int64, int) (*http.Response, error)
	BaseURL        func(*provider.Record) string
	BodyLimit      func() int64
	LogConfig      func() (bool, int)
	TruncateString func(string, int) string
	SafeURL        func(string) string
}

// Bind creates attempt-local callbacks for the upstream retry loop.
func (s *AntigravityRetry) Bind(p AntigravityRetryRequest) (*antigravity.RetryAdapter, antigravity.RetryInput) {
	value := p.Provider
	changed := func() {
		if p.Changed != nil {
			p.Changed(value)
		}
	}
	clearSticky := func() {
		if p.ClearSticky != nil {
			p.ClearSticky()
		}
	}
	input := antigravity.RetryInput{
		Ctx: p.Context, Prefix: p.Prefix, ProviderID: value.ID, ProviderName: value.Name,
		Native: value.Platform == provider.PlatformAntigravity, OveragesEnabled: value.IsOveragesEnabled(), SingleProvider: p.SingleProvider,
		AccessToken: p.AccessToken, Action: p.Action, Body: p.Body, RequestedModel: p.RequestedModel, IsStickySession: p.Sticky,
		CreditsExhausted: func() bool {
			reset := value.ModelRateLimitResetAt(provider.CreditsExhaustedKey)
			return reset != nil && time.Now().Before(*reset)
		},
		ModelLimited: func(_ context.Context, model string) bool {
			for _, key := range antigravityRequestLimitKeys(value, model, p.Thinking) {
				if reset := value.ModelRateLimitResetAt(key); reset != nil && time.Now().Before(*reset) {
					return true
				}
			}
			return false
		},
		ModelRemaining: func(_ context.Context, model string) time.Duration {
			var remaining time.Duration
			for _, key := range antigravityRequestLimitKeys(value, model, p.Thinking) {
				if reset := value.ModelRateLimitResetAt(key); reset != nil {
					if left := time.Until(*reset); left > remaining {
						remaining = left
					}
				}
			}
			return remaining
		},
	}
	options := antigravity.RetryOptions{
		BaseURL: func() string { return s.BaseURL(value) },
		Do: func(req *http.Request) (*http.Response, error) {
			if p.Do != nil {
				return p.Do(req)
			}
			return s.Do(req, p.ProxyURL, value.ID, value.Concurrency)
		},
		ApplyHeaders: func(req *http.Request) {
			if agent := strings.TrimSpace(p.UserAgent); req != nil && agent != "" {
				req.Header.Set("User-Agent", agent)
			}
		},
		TruncateString: s.TruncateString, TruncateForLog: logredact.TruncateLine, SafeURL: s.SafeURL,
		ReadErrorBody: func(resp *http.Response) []byte {
			if resp == nil || resp.Body == nil {
				return nil
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, s.BodyLimit()))
			return body
		},
		Observe: func(observation antigravity.RetryObservation) {
			if p.Observe != nil {
				p.Observe(observation)
			}
		},
		SetError: func(status int, message, detail string) {
			if p.SetError != nil {
				p.SetError(status, message, detail)
			}
		},
		HandleError: p.HandleError,
		ApplyErrorPolicy: func(status int, headers http.Header, body []byte) (bool, int, error) {
			return s.applyPolicy(p, status, headers, body)
		},
		SetModelLimits: func(model string, status int, reset time.Time, force bool) bool {
			result := s.Health.SetAntigravityModelRateLimits(p.Context, p.ModelStore, value, model, p.Prefix, status, reset, force)
			changed()
			return result
		},
		ClearSticky: clearSticky,
		CreditsModel: func(model string) string {
			return AntigravityCreditsModelKey(value, model, p.RequestedModel, p.Thinking)
		},
		ClearCredits:         func() { s.Health.ClearCreditsExhausted(p.Context, value); changed() },
		CreditsFailure:       func(model string, resp *http.Response, err error) { s.creditsFailure(p, model, resp, err); changed() },
		Internal500Exhausted: func() { s.Health.HandleInternal500RetryExhausted(p.Context, p.Prefix, value); changed() },
		ResetInternal500:     func() { s.Health.ResetInternal500Counter(p.Context, p.Prefix, value.ID) },
	}
	if p.LogConfig != nil {
		options.LogBody, options.LogMaxBytes = p.LogConfig()
	} else if s.LogConfig != nil {
		options.LogBody, options.LogMaxBytes = s.LogConfig()
	}
	return &antigravity.RetryAdapter{Options: options}, input
}

func (s *AntigravityRetry) applyPolicy(p AntigravityRetryRequest, status int, headers http.Header, body []byte) (bool, int, error) {
	if s.Policy == nil {
		return false, status, nil
	}
	model := FinalAntigravityModel(p.Provider, p.RequestedModel, p.Thinking)
	if strings.TrimSpace(model) == "" {
		model = p.PolicyModelFallback
	}
	switch s.Policy.CheckErrorPolicy(p.Context, p.Provider, status, body, model, false) {
	case provider.ErrorPolicyCustomSkipped:
		if s.modelLimitBeforePolicy(p, status, body) {
			return true, status, nil
		}
		return true, http.StatusInternalServerError, nil
	case provider.ErrorPolicyCustomMatched:
		if s.modelLimitBeforePolicy(p, status, body) {
			return true, status, nil
		}
		p.HandleError(status, headers, body)
		return true, status, nil
	case provider.ErrorPolicyTempUnscheduled:
		s.Health.Info("temp_unschedulable_matched", "prefix", p.Prefix, "status_code", status, "provider_id", p.Provider.ID)
		return true, status, &antigravity.AntigravityProviderSwitchError{OriginalProviderID: p.Provider.ID, RateLimitedModel: p.RequestedModel, IsStickySession: p.Sticky}
	case provider.ErrorPolicyPoolBypassed:
		return false, status, nil
	}
	return false, status, nil
}

func (s *AntigravityRetry) modelLimitBeforePolicy(p AntigravityRetryRequest, status int, body []byte) bool {
	if (status != http.StatusTooManyRequests && status != http.StatusServiceUnavailable) || p.Provider == nil || p.Provider.Platform != provider.PlatformAntigravity {
		return false
	}
	_, limited, duration, model, capacity := antigravity.ShouldTriggerAntigravitySmartRetry(true, body)
	if capacity || !limited || strings.TrimSpace(model) == "" {
		return false
	}
	if duration <= 0 {
		duration = antigravity.AntigravityDefaultRateLimitDuration
	}
	reset := time.Now().Add(duration)
	if !s.Health.SetAntigravityModelRateLimits(p.Context, p.ModelStore, p.Provider, model, p.Prefix, status, reset, false) {
		return false
	}
	if p.Changed != nil {
		p.Changed(p.Provider)
	}
	if p.ClearSticky != nil {
		p.ClearSticky()
	}
	logging.LegacyPrintf("service.antigravity_gateway", "%s status=%d model_rate_limited_before_error_policy model=%s provider=%d reset_in=%v", p.Prefix, status, model, p.Provider.ID, duration)
	return true
}

func (s *AntigravityRetry) creditsFailure(p AntigravityRetryRequest, model string, resp *http.Response, err error) {
	var body []byte
	status := 0
	if resp != nil {
		status = resp.StatusCode
		if resp.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
		}
	}
	if antigravity.ShouldMarkCreditsExhausted(resp, body, err) && p.Provider != nil {
		s.Health.SetCreditsExhausted(p.Context, p.Provider)
		logging.LegacyPrintf("service.antigravity_gateway", "%s credit_overages_failed model=%s provider=%d marked_exhausted=true status=%d body=%s", p.Prefix, model, p.Provider.ID, status, logredact.TruncateLine(body, 200))
		return
	}
	if p.Provider != nil {
		logging.LegacyPrintf("service.antigravity_gateway", "%s credit_overages_failed model=%s provider=%d marked_exhausted=false status=%d err=%v body=%s", p.Prefix, model, p.Provider.ID, status, err, logredact.TruncateLine(body, 200))
	}
}

func antigravityRequestLimitKeys(value *provider.Record, model string, thinking *bool) []string {
	return AntigravityModelLimitKeys(FinalAntigravityModel(value, model, thinking))
}

// AntigravityModelLimitKeys returns the exact model key plus the shared Gemini window key for Gemini models.
func AntigravityModelLimitKeys(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	keys := []string{model}
	if strings.HasPrefix(antigravity.NormalizeAntigravityModelName(model), "gemini-") && model != "antigravity:gemini" {
		keys = append(keys, "antigravity:gemini")
	}
	return keys
}

// AntigravityPaidTier reports whether the provider has a pro or ultra plan.
func AntigravityPaidTier(value *provider.Record) bool {
	if value == nil || value.Credentials == nil {
		return false
	}
	tier, ok := value.Credentials["plan_type"].(string)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "pro", "ultra":
		return true
	default:
		return false
	}
}

// AntigravityCreditsModelKey prefers the upstream name, then the mapped request name, then a
// normalized request name.
func AntigravityCreditsModelKey(value *provider.Record, upstreamModel, requestedModel string, thinking *bool) string {
	if key := strings.TrimSpace(upstreamModel); key != "" {
		return key
	}
	if value == nil {
		return ""
	}
	if key := FinalAntigravityModel(value, requestedModel, thinking); strings.TrimSpace(key) != "" {
		return key
	}
	return antigravity.NormalizeAntigravityModelName(requestedModel)
}
