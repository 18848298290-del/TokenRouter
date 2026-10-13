package provider

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Image quota failures and lost image capability use one dedicated model-limit
// scope, so ordinary provider and text-model windows remain separate. Pool mode
// skips local quota observation, while an accepted capability-loss
// classification can still install the fixed image-scope cooldown.

// Image reason values and the capability-loss duration are stored under the
// dedicated image-generation scope for later media eligibility checks.
const (
	OpenAIImageRateLimitReason        = "openai_image_rate_limited"
	OpenAIImageCapabilityLossReason   = "openai_image_capability_lost"
	OpenAIImageCapabilityLossCooldown = 30 * time.Minute
	OpenAIImageGenerationRateLimitKey = "openai:image_generation"
)

// ApplyImageRateLimit observes an eligible OpenAI image quota rejection and
// attempts its model-scope pause. Missing service, record or store, pool mode
// and a skipped error code return false before observe is called. Supply a
// callable observer when those checks pass. A match returns true even after a
// logged write failure, and the supplied reset is used directly.
func (s *HealthService) ApplyImageRateLimit(ctx context.Context, provider *Record, statusCode int, observe func() (bool, time.Time)) bool {
	if s == nil || provider == nil || s.providerRepo == nil {
		return false
	}
	if provider.Platform != capability.PlatformOpenAI {
		return false
	}
	// Pool records bypass ordinary local health under CheckErrorPolicy when
	// custom codes are disabled. Keep image quota observation separate from the
	// temporary rules that the pool policy can still apply.
	if provider.IsPoolMode() {
		return false
	}
	if !provider.ShouldHandleErrorCode(statusCode) {
		s.options.Info("openai_image_rate_limit_skipped_by_error_code_policy", "provider_id", provider.ID, "status_code", statusCode)
		return false
	}
	matched, resetAt := observe()
	if !matched {
		return false
	}

	// A model-limit error leaves persistence uncertain. The observer's matched
	// classification still returns to callers, which decide their own subsequent
	// response-health handling.
	if err := s.providerRepo.SetModelRateLimit(ctx, provider.ID, OpenAIImageGenerationRateLimitKey, resetAt, OpenAIImageRateLimitReason); err != nil {
		s.options.Warn("openai_image_rate_limit_set_model_rate_limit_failed", "provider_id", provider.ID, "scope", OpenAIImageGenerationRateLimitKey, "error", err)
		return true
	}
	s.options.Info("openai_image_rate_limited", "provider_id", provider.ID, "scope", OpenAIImageGenerationRateLimitKey, "reset_at", resetAt, "reset_in", time.Until(resetAt).Truncate(time.Second))
	return true
}

// ApplyImageCapabilityLoss attempts a thirty-minute image-scope pause for an
// accepted OpenAI capability-loss classification. Missing dependencies, another
// platform, skipped codes or a false classification return false. Pool mode is
// eligible here. A selected classification returns true even when its
// model-limit write fails and is logged.
func (s *HealthService) ApplyImageCapabilityLoss(ctx context.Context, provider *Record, statusCode int, capabilityLost bool) bool {
	if s == nil || provider == nil || s.providerRepo == nil {
		return false
	}
	if provider.Platform != capability.PlatformOpenAI {
		return false
	}
	if !provider.ShouldHandleErrorCode(statusCode) {
		s.options.Info("openai_image_capability_loss_skipped_by_error_code_policy", "provider_id", provider.ID, "status_code", statusCode)
		return false
	}
	if !capabilityLost {
		return false
	}

	resetAt := s.options.Now().Add(OpenAIImageCapabilityLossCooldown)
	// A capability denial remains matched after a failed model-limit write.
	// Callers choose their response-health flow independently; OpenAIResponseHealth
	// already exits on its own image classification.
	if err := s.providerRepo.SetModelRateLimit(ctx, provider.ID, OpenAIImageGenerationRateLimitKey, resetAt, OpenAIImageCapabilityLossReason); err != nil {
		s.options.Warn("openai_image_capability_loss_set_model_rate_limit_failed", "provider_id", provider.ID, "scope", OpenAIImageGenerationRateLimitKey, "error", err)
		return true
	}
	s.options.Info("openai_image_capability_lost", "provider_id", provider.ID, "scope", OpenAIImageGenerationRateLimitKey, "reset_at", resetAt, "reset_in", time.Until(resetAt).Truncate(time.Second))
	return true
}
