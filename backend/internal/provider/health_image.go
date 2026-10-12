package provider

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Image generation uses a separate model-limit key for its reason and cooldown.
const (
	OpenAIImageRateLimitReason        = "openai_image_rate_limited"
	OpenAIImageCapabilityLossReason   = "openai_image_capability_lost"
	OpenAIImageCapabilityLossCooldown = 30 * time.Minute
	OpenAIImageGenerationRateLimitKey = "openai:image_generation"
)

// ApplyImageRateLimit records an OpenAI image-generation cooldown independently of other capabilities.
func (s *HealthService) ApplyImageRateLimit(ctx context.Context, provider *Record, statusCode int, observe func() (bool, time.Time)) bool {
	if s == nil || provider == nil || s.providerRepo == nil {
		return false
	}
	if provider.Platform != capability.PlatformOpenAI {
		return false
	}
	// Pool mode delegates capability selection upstream, so it skips this local image rate limit.
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

	if err := s.providerRepo.SetModelRateLimit(ctx, provider.ID, OpenAIImageGenerationRateLimitKey, resetAt, OpenAIImageRateLimitReason); err != nil {
		s.options.Warn("openai_image_rate_limit_set_model_rate_limit_failed", "provider_id", provider.ID, "scope", OpenAIImageGenerationRateLimitKey, "error", err)
		return true
	}
	s.options.Info("openai_image_rate_limited", "provider_id", provider.ID, "scope", OpenAIImageGenerationRateLimitKey, "reset_at", resetAt, "reset_in", time.Until(resetAt).Truncate(time.Second))
	return true
}

// ApplyImageCapabilityLoss cools down image scheduling when the upstream rejects the image capability.
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
	if err := s.providerRepo.SetModelRateLimit(ctx, provider.ID, OpenAIImageGenerationRateLimitKey, resetAt, OpenAIImageCapabilityLossReason); err != nil {
		s.options.Warn("openai_image_capability_loss_set_model_rate_limit_failed", "provider_id", provider.ID, "scope", OpenAIImageGenerationRateLimitKey, "error", err)
		return true
	}
	s.options.Info("openai_image_capability_lost", "provider_id", provider.ID, "scope", OpenAIImageGenerationRateLimitKey, "reset_at", resetAt, "reset_in", time.Until(resetAt).Truncate(time.Second))
	return true
}
