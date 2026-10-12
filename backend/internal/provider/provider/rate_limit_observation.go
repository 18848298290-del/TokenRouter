package provider

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	anthropicupstream "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
	openaiupstream "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// RateLimitObserver parses upstream rate-limit responses and writes provider health observations.
type RateLimitObserver struct {
	Health          *providercore.HealthService
	Plans           providercore.OpenAIPlanWriter
	RetryOpenAI     func(*providercore.Record, http.Header, []byte) bool
	NextGeminiDaily func() *int64
}

// Observe429 derives a reset time from upstream headers, response bodies, or configured cooldowns.
// Shadow providers are skipped, and OAuth retry policy can defer the health write.
func (s *RateLimitObserver) Observe429(ctx context.Context, provider *providercore.Record, headers http.Header, responseBody []byte) {
	// Spark shadow quotas come from codex_bengalfox in /wham/usage.
	// x-codex-* and usage_limit_reached in /responses describe global windows and cannot pause a funded shadow.
	// Usage queries maintain shadow snapshots; scheduling checks enforce their quota limits.
	if provider.IsShadow() {
		return
	}
	if provider.Platform == capability.PlatformOpenAI && provider.IsOpenAIOAuthLike() {
		if s.RetryOpenAI != nil && s.RetryOpenAI(provider, headers, responseBody) {
			return
		}
	}
	// Kimi, Zhipu, and DeepSeek balance failures trigger temporary suspension; exhausted
	// Coding Plan windows cool down until the snapshot reset. Other failures continue below.
	if provider.IsCNProvider() {
		if s.observeCNQuota(ctx, provider, responseBody) {
			return
		}
	}
	// Prefer OpenAI x-codex-* reset headers for rate_limit_exceeded responses.
	if provider.Platform == capability.PlatformOpenAI {
		providercore.PersistOpenAIObservedPlan(ctx, s.Plans, provider, openaiupstream.ParseUsageLimitPlanType(responseBody), slog.Info, slog.Warn)
		s.PersistCodexSnapshot(ctx, provider, headers)
		if resetAt := providercore.OpenAI429ResetTime(openaiupstream.ParseCodexRateLimitHeaders(headers), time.Now, slog.Info); resetAt != nil {
			if !s.Health.ApplyObservedRateLimit(ctx, provider, *resetAt) {
				return
			}
			slog.Info("openai_provider_rate_limited", "provider_id", provider.ID, "reset_at", *resetAt)
			return
		}
	}

	// Select the exhausted Anthropic 5h or 7d window from per-window headers.
	if result := anthropicupstream.CalculateRateLimitReset(headers, slog.Info); result != nil {
		if !s.Health.ApplyObservedRateLimit(ctx, provider, result.ResetAt) {
			return
		}

		// Use the precise 5h reset for the session window when available; otherwise use the selected reset.
		windowEnd := result.ResetAt
		if result.FiveHourReset != nil {
			windowEnd = *result.FiveHourReset
		}
		s.Health.UpdateRejectedSessionWindow(ctx, provider, windowEnd)

		slog.Info("anthropic_provider_rate_limited", "provider_id", provider.ID, "reset_at", result.ResetAt, "reset_in", time.Until(result.ResetAt).Truncate(time.Second))
		return
	}

	// Accept the aggregate Anthropic reset header for older responses.
	resetTimestamp := headers.Get("anthropic-ratelimit-unified-reset")

	// Without a reset header, inspect OpenAI or Gemini error bodies.
	if resetTimestamp == "" {
		switch provider.Platform {
		case capability.PlatformOpenAI:
			if resetAt := openaiupstream.ParseUsageLimitResetTime(responseBody, time.Now); resetAt != nil {
				resetTime := time.Unix(*resetAt, 0)
				if !s.Health.ApplyObservedRateLimit(ctx, provider, resetTime) {
					return
				}
				slog.Info("provider_rate_limited", "provider_id", provider.ID, "platform", provider.Platform, "reset_at", resetTime, "reset_in", time.Until(resetTime).Truncate(time.Second))
				return
			}
		case capability.PlatformGemini, capability.PlatformAntigravity:
			// Gemini and Antigravity share this error format.
			if resetAt := gemini.ParseGeminiRateLimitResetTime(responseBody, s.NextGeminiDaily); resetAt != nil {
				resetTime := time.Unix(*resetAt, 0)
				if !s.Health.ApplyObservedRateLimit(ctx, provider, resetTime) {
					return
				}
				slog.Info("provider_rate_limited", "provider_id", provider.ID, "platform", provider.Platform, "reset_at", resetTime, "reset_in", time.Until(resetTime).Truncate(time.Second))
				return
			}
		}

		// Anthropic 429 responses without a reset use the configured cooldown in seconds.
		// This also covers Extra usage required responses; administrators can disable the cooldown.
		if provider.Platform == capability.PlatformAnthropic {
			slog.Warn("rate_limit_429_no_reset_time",
				"provider_id", provider.ID,
				"platform", provider.Platform,
				"reason", "no rate limit reset time in headers, likely not a real rate limit")
			s.Health.Apply429Fallback(ctx, provider, "anthropic_no_reset_time")
			return
		}

		// Other platforms also use the configured cooldown when no reset is available.
		s.Health.Apply429Fallback(ctx, provider, "no_reset_time")
		return
	}

	ts, err := strconv.ParseInt(resetTimestamp, 10, 64)
	if err != nil {
		slog.Warn("rate_limit_reset_parse_failed", "reset_timestamp", resetTimestamp, "error", err)
		s.Health.Apply429Fallback(ctx, provider, "reset_parse_failed")
		return
	}

	resetAt := time.Unix(ts, 0)

	if !s.Health.ApplyObservedRateLimit(ctx, provider, resetAt) {
		return
	}

	// Derive the five-hour session window from the reset time.
	windowEnd := resetAt
	s.Health.UpdateRejectedSessionWindow(ctx, provider, windowEnd)

	slog.Info("provider_rate_limited", "provider_id", provider.ID, "reset_at", resetAt)
}

// PersistCodexSnapshot writes parsed quota headers, skipping shadow providers and nil headers.
func (s *RateLimitObserver) PersistCodexSnapshot(ctx context.Context, value *providercore.Record, headers http.Header) {
	if s == nil || s.Health == nil || value == nil || headers == nil || value.IsShadow() {
		return
	}
	s.Health.PersistCodexObservation(ctx, value, openaiupstream.ParseCodexRateLimitHeaders(headers))
}

func (s *RateLimitObserver) observeCNQuota(ctx context.Context, value *providercore.Record, body []byte) bool {
	if !value.IsCNProvider() {
		return false
	}
	if upstream.CNResponseIndicatesInsufficientBalance(body) {
		s.Health.ApplyCNInsufficientBalance(ctx, value, upstream.ExtractErrorMessage(body))
		return true
	}
	return s.Health.ApplyCNQuotaSnapshotCooldown(ctx, value)
}
