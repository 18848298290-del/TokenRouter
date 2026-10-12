package provider

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func openAIImageResetAt(headers http.Header, body []byte) time.Time {
	now := time.Now()
	if resetAt := openai.ParseRetryAfterResetTime(headers, now); resetAt != nil && resetAt.After(now) {
		return *resetAt
	}
	if resetAt := provider.OpenAI429ResetTime(openai.ParseCodexRateLimitHeaders(headers), time.Now, slog.Info); resetAt != nil && resetAt.After(now) {
		return *resetAt
	}
	if resetUnix := openai.ParseUsageLimitResetTime(body, time.Now); resetUnix != nil {
		if resetAt := time.Unix(*resetUnix, 0); resetAt.After(now) {
			return resetAt
		}
	}
	if cooldown := openai.ParseImageTryAgainCooldown(body); cooldown > 0 {
		return now.Add(cooldown)
	}
	return now.Add(time.Minute)
}

// ObserveOpenAIImageRateLimit parses reset signals when provider health rules permit an image-limit
// observation.
func ObserveOpenAIImageRateLimit(ctx context.Context, health *provider.HealthService, value *provider.Record, status int, headers http.Header, body []byte) bool {
	return health.ApplyImageRateLimit(ctx, value, status, func() (bool, time.Time) {
		if !openai.IsImageRateLimitError(status, body) {
			return false, time.Time{}
		}
		return true, openAIImageResetAt(headers, body)
	})
}

// ObserveOpenAIImageCapabilityLoss passes a classified capability denial to provider health rules.
func ObserveOpenAIImageCapabilityLoss(ctx context.Context, health *provider.HealthService, value *provider.Record, status int, body []byte) bool {
	return health.ApplyImageCapabilityLoss(ctx, value, status, openai.IsImageCapabilityLossError(status, body))
}
