package provider

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Image rate limits carry several possible recovery signals that can disagree
// with account-wide quota windows. Image reset selection first accepts a future
// Retry-After value, then a future Codex quota reset, then a future reset in
// the response body.
//
// An image-specific try-again duration supplies the next choice, and one minute
// supplies the final recovery window when no signal can establish a future
// deadline. Provider health rules decide whether the response qualifies before
// storing that deadline under the image capability key. Capability-loss errors
// use a separate classification so an image denial can be handled independently
// of rate-limit parsing.

// openAIImageResetAt selects a future deadline from image response headers and
// body. Retry-After wins over Codex quota resets, which win over a usage-limit
// body timestamp; past absolute deadlines are ignored.
//
// If those signals cannot provide a future reset, a positive image try-again
// duration extends the captured current time. The final choice is one minute
// from that time, allowing image health to store a bounded cooldown even when
// the response lacks a usable reset signal.
func openAIImageResetAt(headers http.Header, body []byte) time.Time {
	now := time.Now()
	if resetAt := openai.ParseRetryAfterResetTime(headers, now); resetAt != nil && resetAt.After(now) {
		return *resetAt
	}
	if resetAt := provider.OpenAI429ResetTime(openai.ParseCodexRateLimitHeaders(headers), time.Now, slog.Info); resetAt != nil && resetAt.After(now) {
		return *resetAt
	}
	if resetUnix := openai.ParseUsageLimitResetTime(body, time.Now); resetUnix != nil {
		// Body reset seconds name an absolute image recovery timestamp.
		if resetAt := time.Unix(*resetUnix, 0); resetAt.After(now) {
			return resetAt
		}
	}
	// An image-specific message can supply a usable relative retry interval after
	// absolute reset signals have failed their future-time checks.
	if cooldown := openai.ParseImageTryAgainCooldown(body); cooldown > 0 {
		return now.Add(cooldown)
	}
	// No response signal establishes a future recovery point. Use a bounded
	// minute-long image cooldown so malformed limit responses still permit later
	// attempts.
	return now.Add(time.Minute)
}

// ObserveOpenAIImageRateLimit classifies an image rate limit when provider
// health rules permit it and reports whether it was handled. The health service
// owns scope and persistence, while the callback supplies the selected image
// reset time.
func ObserveOpenAIImageRateLimit(ctx context.Context, health *provider.HealthService, value *provider.Record, status int, headers http.Header, body []byte) bool {
	return health.ApplyImageRateLimit(ctx, value, status, func() (bool, time.Time) {
		if !openai.IsImageRateLimitError(status, body) {
			return false, time.Time{}
		}
		return true, openAIImageResetAt(headers, body)
	})
}

// ObserveOpenAIImageCapabilityLoss classifies a denied image capability and
// reports whether provider health handled it. The health service applies
// platform and configured error-code eligibility before installing any
// image-specific cooldown.
func ObserveOpenAIImageCapabilityLoss(ctx context.Context, health *provider.HealthService, value *provider.Record, status int, body []byte) bool {
	return health.ApplyImageCapabilityLoss(ctx, value, status, openai.IsImageCapabilityLossError(status, body))
}
