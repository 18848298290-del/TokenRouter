package provider

import (
	"context"
	"net/http"
	"time"
)

// CodexSparkRateLimitReason identifies a Spark model cooldown.
const CodexSparkRateLimitReason = "openai_codex_spark_rate_limit"

// ApplySparkRateLimit stores Spark quota exhaustion as a model-specific cooldown.
// Other models continue to use their own quota state.
func (s *HealthService) ApplySparkRateLimit(ctx context.Context, provider *Record, modelKey string, statusCode int, spark bool, observe func() (OpenAI429Disposition, *time.Time)) bool {
	if s == nil || provider == nil || s.providerRepo == nil || statusCode != http.StatusTooManyRequests || !provider.IsOpenAIOAuthLike() {
		return false
	}
	if !spark || !provider.ShouldHandleErrorCode(statusCode) {
		return false
	}

	if modelKey == "" {
		return false
	}
	now := s.options.Now()
	disposition, resetAt := observe()
	// Use a long upstream reset only for confirmed five-hour or seven-day exhaustion.
	// Transient 429 responses use the short cooldown even when global reset headers are present.
	if disposition != OpenAI429Quota5h && disposition != OpenAI429Quota7d {
		resetAt = nil
	}
	if resetAt == nil || !resetAt.After(now) {
		cooldown, ok := s.Fallback429Cooldown(ctx, provider)
		if !ok || cooldown <= 0 {
			cooldown = time.Duration(DefaultRateLimit429CooldownSeconds) * time.Second
		}
		reset := now.Add(cooldown)
		resetAt = &reset
	}
	if err := s.providerRepo.SetModelRateLimit(ctx, provider.ID, modelKey, *resetAt, CodexSparkRateLimitReason); err != nil {
		s.options.Warn("openai_codex_spark_model_rate_limit_set_failed", "provider_id", provider.ID, "model", modelKey, "error", err)
	}
	s.options.Info("openai_codex_spark_model_rate_limited", "provider_id", provider.ID, "model", modelKey, "reset_at", *resetAt)
	return true
}
