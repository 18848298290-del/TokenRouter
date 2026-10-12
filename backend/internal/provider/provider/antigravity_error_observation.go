package provider

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// AntigravityModelLimitResult describes whether to retry or switch after a model limit.
type AntigravityModelLimitResult struct {
	Handled      bool                                        // Whether model-limit handling consumed the error.
	ShouldRetry  bool                                        // Whether the caller should wait and retry.
	WaitDuration time.Duration                               // Delay before retrying.
	SwitchError  *antigravity.AntigravityProviderSwitchError // Non-nil when the caller should switch providers.
}

// AntigravityErrorInput contains the failed attempt, model, and sticky-session observation.
type AntigravityErrorInput struct {
	Context          context.Context
	Provider         *provider.Record
	Prefix           string
	Status           int
	Headers          http.Header
	Body             []byte
	RequestedModel   string
	OtherObservation HealthObservation
	Thinking         *bool
	Sticky           bool
	ClearSticky      func()
}

// AntigravityErrorObserver records model limits and provider health from upstream failures.
type AntigravityErrorObserver struct {
	Health          *provider.AntigravityHealth
	LogConfig       func() (bool, int)
	TruncateString  func(string, int) string
	ResetTime       func([]byte) *int64
	DefaultDuration func() time.Duration
	SetRateLimited  func(context.Context, int64, time.Time) error
	Other           *UpstreamHealth
}

// handleModelRateLimit handles model-specific 429 and 503 errors before general health updates.
// Capacity exhaustion is left to the platform retry loop. For rate limits,
// the caller waits when retryDelay is below the threshold; longer delays record
// a model limit, clear the sticky session, and request a provider switch.
func (s *AntigravityErrorObserver) handleModelRateLimit(p *AntigravityErrorInput) *AntigravityModelLimitResult {
	if p.Status != 429 && p.Status != 503 {
		return &AntigravityModelLimitResult{Handled: false}
	}

	info := antigravity.ParseAntigravitySmartRetryInfo(p.Body)
	if info == nil || info.ModelName == "" {
		return &AntigravityModelLimitResult{Handled: false}
	}

	// Capacity exhaustion uses the platform retry loop because the capacity pool is shared.
	if info.IsModelCapacityExhausted {
		log.Printf("%s status=%d model_capacity_exhausted model=%s (not switching provider, retry handled by smart retry)",
			p.Prefix, p.Status, info.ModelName)
		return &AntigravityModelLimitResult{
			Handled: true,
		}
	}

	if info.RetryDelay < antigravity.AntigravityRateLimitThreshold {
		logging.LegacyPrintf("service.antigravity_gateway", "%s status=%d model_rate_limit_wait model=%s wait=%v",
			p.Prefix, p.Status, info.ModelName, info.RetryDelay)
		return &AntigravityModelLimitResult{
			Handled:      true,
			ShouldRetry:  true,
			WaitDuration: info.RetryDelay,
		}
	}

	s.setModelRateLimitAndClearSession(p, info)

	return &AntigravityModelLimitResult{
		Handled: true,
		SwitchError: &antigravity.AntigravityProviderSwitchError{
			OriginalProviderID: p.Provider.ID,
			RateLimitedModel:   info.ModelName,
			IsStickySession:    p.Sticky,
		},
	}
}

// setModelRateLimitAndClearSession records the reset time and releases the sticky binding.
func (s *AntigravityErrorObserver) setModelRateLimitAndClearSession(p *AntigravityErrorInput, info *antigravity.AntigravitySmartRetryInfo) {
	resetAt := time.Now().Add(info.RetryDelay)
	logging.LegacyPrintf("service.antigravity_gateway", "%s status=%d model_rate_limited model=%s provider=%d reset_in=%v",
		p.Prefix, p.Status, info.ModelName, p.Provider.ID, info.RetryDelay)

	s.Health.SetAntigravityModelRateLimits(p.Context, s.Health.Store, p.Provider, info.ModelName, p.Prefix, p.Status, resetAt, false)

	if p.ClearSticky != nil {
		p.ClearSticky()
	}
}

// Observe classifies one failure and records provider health.
func (s *AntigravityErrorObserver) Observe(p AntigravityErrorInput) *AntigravityModelLimitResult {
	ctx, prefix, value := p.Context, p.Prefix, p.Provider
	statusCode, body, requestedModel := p.Status, p.Body, p.RequestedModel
	// A custom status-code policy can exclude this response from health updates.
	if !value.ShouldHandleErrorCode(statusCode) {
		return nil
	}
	result := s.handleModelRateLimit(&p)
	if result.Handled {
		return result
	}

	// A 503 with no recognized model limit leaves provider health unchanged.
	if statusCode == 503 {
		return nil
	}

	// When a 429 lacks model-limit metadata, derive a limit key from the requested model.
	if statusCode == 429 {
		if logBody, maxBytes := s.LogConfig(); logBody {
			logging.LegacyPrintf("service.antigravity_gateway", "[Antigravity-Debug] 429 response body: %s", s.TruncateString(string(body), maxBytes))
		}

		resetAt := s.ResetTime(body)
		defaultDur := s.DefaultDuration()

		// The requested name may precede mapping and thinking normalization.
		// Record the final model name so scheduling checks the same limit key.
		modelKey := FinalAntigravityModel(value, requestedModel, p.Thinking)
		if strings.TrimSpace(modelKey) == "" {
			// If final-model resolution fails, normalize the requested name for the limit key.
			modelKey = antigravity.NormalizeAntigravityModelName(requestedModel)
		}
		if modelKey != "" {
			ra := s.resolveResetTime(resetAt, defaultDur)
			if !s.Health.SetAntigravityModelRateLimits(ctx, s.Health.Store, value, modelKey, prefix, statusCode, ra, false) {
				logging.LegacyPrintf("service.antigravity_gateway", "%s status=429 model_rate_limit_set_failed model=%s", prefix, modelKey)
			} else {
				logging.LegacyPrintf("service.antigravity_gateway", "%s status=429 model_rate_limited model=%s provider=%d reset_at=%v reset_in=%v",
					prefix, modelKey, value.ID, ra.Format("15:04:05"), time.Until(ra).Truncate(time.Second))
			}
			return nil
		}

		// Without a model key, record a provider-wide limit.
		ra := s.resolveResetTime(resetAt, defaultDur)
		logging.LegacyPrintf("service.antigravity_gateway", "%s status=429 rate_limited provider=%d reset_at=%v reset_in=%v (fallback)",
			prefix, value.ID, ra.Format("15:04:05"), time.Until(ra).Truncate(time.Second))
		if err := s.SetRateLimited(ctx, value.ID, ra); err != nil {
			logging.LegacyPrintf("service.antigravity_gateway", "%s status=429 rate_limit_set_failed provider=%d error=%v", prefix, value.ID, err)
		}
		return nil
	}
	if s.Other == nil {
		return nil
	}
	shouldDisable := s.Other.ApplyUpstreamError(ctx, value, p.OtherObservation).StopScheduling
	if shouldDisable {
		logging.LegacyPrintf("service.antigravity_gateway", "%s status=%d marked_error", prefix, statusCode)
	}
	return nil
}

// resolveResetTime uses the parsed reset timestamp or the configured default duration.
func (s *AntigravityErrorObserver) resolveResetTime(resetAt *int64, defaultDur time.Duration) time.Time {
	if resetAt != nil {
		return time.Unix(*resetAt, 0)
	}
	return time.Now().Add(defaultDur)
}

// AntigravityFallbackDuration gives a positive environment value in seconds precedence over configured
// minutes.
func AntigravityFallbackDuration(minutes int, raw string) time.Duration {
	duration := antigravity.AntigravityDefaultRateLimitDuration
	if minutes > 0 {
		duration = time.Duration(minutes) * time.Minute
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && seconds > 0 {
		duration = time.Duration(seconds) * time.Second
	}
	return duration
}
