package provider

import (
	"context"
	"net/http"
	"time"
)

// Spark quota exhaustion pauses its normalized model scope, leaving the
// provider's other model windows separate. A confirmed five-hour or seven-day
// exhaustion can use its future reset; transient classifications discard long
// reset data and choose a short fallback.
//
// The fallback decision still supplies a positive built-in interval when
// provider-wide fallback policy is disabled or unusable. This keeps the handled
// Spark response on a bounded model cooldown while global quota windows
// continue to describe their own observations. The observer is invoked after
// provider, status, code and model checks. Model-limit storage is attempted
// once and failures are logged; the true result denotes handled classification
// independently of persistence.

// CodexSparkRateLimitReason labels the model-scope pause so Spark eligibility
// and diagnostics can distinguish it from provider-wide quota windows.
const CodexSparkRateLimitReason = "openai_codex_spark_rate_limit"

// ApplySparkRateLimit attempts the Spark model cooldown for a handled
// OAuth-like OpenAI 429 response. Missing dependencies, unsupported
// provider/status, a false Spark classification, skipped code or empty model
// key return false. Supply a callable observer when those checks pass.
//
// Only confirmed five-hour or seven-day exhaustion retains an observed reset.
// Missing or nonfuture reset data uses the current fallback duration; a
// disabled or nonpositive fallback still selects the built-in five seconds. The
// model-limit call uses that selected deadline, and a logged write failure
// leaves the method's handled result true.
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
	// A missing or expired vendor reset cannot provide a future model block.
	// Resolve its fallback duration while retaining the already-selected Spark
	// classification for this response.
	if resetAt == nil || !resetAt.After(now) {
		cooldown, ok := s.Fallback429Cooldown(ctx, provider)
		// This model-specific response remains classified as a cooldown even when
		// provider-wide fallback policy is disabled, so install the built-in short
		// interval in that case.
		if !ok || cooldown <= 0 {
			cooldown = time.Duration(DefaultRateLimit429CooldownSeconds) * time.Second
		}
		reset := now.Add(cooldown)
		resetAt = &reset
	}
	// Retain Spark's model-scoped classification after a storage error; logging
	// exposes incomplete persistence while the caller can continue its selected
	// model failure handling.
	if err := s.providerRepo.SetModelRateLimit(ctx, provider.ID, modelKey, *resetAt, CodexSparkRateLimitReason); err != nil {
		s.options.Warn("openai_codex_spark_model_rate_limit_set_failed", "provider_id", provider.ID, "model", modelKey, "error", err)
	}
	s.options.Info("openai_codex_spark_model_rate_limited", "provider_id", provider.ID, "model", modelKey, "reset_at", *resetAt)
	return true
}
