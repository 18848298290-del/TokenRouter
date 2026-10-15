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

// rate_limit_observation.go translates upstream quota signals into provider
// cooldown and session-window observations.
// Provider cooldowns use different quota signals across platforms, and Spark
// shadows have their own scope. The observer skips shadows before plan or
// header writes and can defer an OpenAI OAuth observation to the same-provider
// recovery window.
//
// CN balance and coding-window handling precede generic reset selection. OpenAI
// persists plan and quota observations before choosing a Codex reset, followed
// by Anthropic-shaped per-window or aggregate headers and platform body resets.
// Anthropic session-window updates retain a precise five-hour reset when the
// selected provider cooldown comes from a longer window. Missing or malformed
// reset signals use configured fallback handling, so durable cooldown and local
// session timing remain separate observations.

// RateLimitObserver translates upstream quota and retry signals into provider
// health observations.
type RateLimitObserver struct {
	Health *providercore.HealthService // provider quota health service
	// Plans optionally persists observed OpenAI plan metadata. A nil writer skips
	// plan changes while other quota observations can continue.
	Plans providercore.OpenAIPlanWriter
	// RetryOpenAI optionally admits same-provider recovery for OpenAI OAuth-like
	// records. A true result defers all later persistence in Observe429.
	RetryOpenAI func(*providercore.Record, http.Header, []byte) bool
	// NextGeminiDaily supplies the platform day reset for Gemini-format body
	// parsing. Its use and empty result are decided by that parser.
	NextGeminiDaily func() *int64
}

// Observe429 records reset and quota observations for a nonnil provider after
// shadow and retry exclusions. Configure Health for the selected observation
// paths; optional OpenAI retry policy can return before any plan, snapshot, or
// cooldown write.
//
// CN-specific quota handling wins first, then OpenAI plan/snapshot writes and
// Codex header reset selection. Anthropic per-window headers precede the
// aggregate header, with platform body parsing used only when that aggregate
// value is absent. The chosen cooldown is written before its session window,
// which can use a distinct five-hour reset. Missing or invalid reset text
// delegates to configured fallback cooldown rules.
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
	// Persist plan and quota observations before selecting the cooldown. Those
	// independent observations remain available even when the headers supply no
	// reset and body or fallback handling follows.
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

	// Per-window Anthropic headers retain the association between utilization and
	// reset. Evaluate them before the aggregate header so exhausted-window
	// selection can use its precise deadline.
	if result := anthropicupstream.CalculateRateLimitReset(headers, slog.Info); result != nil {
		if !s.Health.ApplyObservedRateLimit(ctx, provider, result.ResetAt) {
			return
		}

		// Provider cooldown can follow a weekly window while session accounting still
		// needs the five-hour endpoint. Prefer that precise reset after the cooldown
		// write succeeds.
		windowEnd := result.ResetAt
		if result.FiveHourReset != nil {
			windowEnd = *result.FiveHourReset
		}
		s.Health.UpdateRejectedSessionWindow(ctx, provider, windowEnd)

		slog.Info("anthropic_provider_rate_limited", "provider_id", provider.ID, "reset_at", result.ResetAt, "reset_in", time.Until(result.ResetAt).Truncate(time.Second))
		return
	}

	// Responses lacking usable per-window data can still carry the aggregate
	// Anthropic reset. Read it before platform body parsing to retain that
	// header's precedence.
	resetTimestamp := headers.Get("anthropic-ratelimit-unified-reset")

	// An absent aggregate reset permits body-based platform recovery times. A
	// present but malformed aggregate value instead reaches parse-failure fallback
	// below, keeping this precedence fixed.
	if resetTimestamp == "" {
		switch provider.Platform {
		case capability.PlatformOpenAI:
			if resetAt := openaiupstream.ParseUsageLimitResetTime(responseBody, time.Now); resetAt != nil {
				// Body reset seconds become an absolute provider recovery deadline.
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
				// Body reset seconds become an absolute provider recovery deadline.
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

		// No header or platform body supplied a reset. Delegate the remaining
		// cooldown choice to Health so its enabled flag and configured seconds govern
		// this observation.
		s.Health.Apply429Fallback(ctx, provider, "no_reset_time")
		return
	}

	ts, err := strconv.ParseInt(resetTimestamp, 10, 64)
	if err != nil {
		// Malformed aggregate reset text cannot establish a durable deadline. Log its
		// parse failure and use the configured fallback instead of an arbitrary
		// timestamp.
		slog.Warn("rate_limit_reset_parse_failed", "reset_timestamp", resetTimestamp, "error", err)
		s.Health.Apply429Fallback(ctx, provider, "reset_parse_failed")
		return
	}

	// Aggregate reset text encodes an absolute Unix-second deadline.
	resetAt := time.Unix(ts, 0)

	if !s.Health.ApplyObservedRateLimit(ctx, provider, resetAt) {
		return
	}

	// The provider cooldown has been accepted. Give session-window handling the
	// same aggregate endpoint so its five-hour start can be derived consistently
	// from that observation.
	windowEnd := resetAt
	s.Health.UpdateRejectedSessionWindow(ctx, provider, windowEnd)

	slog.Info("provider_rate_limited", "provider_id", provider.ID, "reset_at", resetAt)
}

// PersistCodexSnapshot passes parsed Codex quota headers to Health for snapshot
// persistence. Nil observers, health services, records, or headers are skipped,
// and shadow records remain excluded from these global header observations.
func (s *RateLimitObserver) PersistCodexSnapshot(ctx context.Context, value *providercore.Record, headers http.Header) {
	if s == nil || s.Health == nil || value == nil || headers == nil || value.IsShadow() {
		return
	}
	s.Health.PersistCodexObservation(ctx, value, openaiupstream.ParseCodexRateLimitHeaders(headers))
}

// observeCNQuota consumes a CN insufficient-balance body or an exhausted stored
// quota window and reports whether handling won. Other records return false;
// insufficient-balance classification returns true after requesting its health
// update.
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
