package provider

import (
	"context"
	"time"
)

const (
	ModelNotFoundCooldown       = 30 * time.Minute
	ModelNotFoundReason         = "upstream_404_model_not_found"
	CodexPlanGatedModelCooldown = 30 * time.Minute
	CodexPlanGatedModelReason   = "upstream_400_codex_plan_gated_model"
)

// ModelFailureObservation carries the current attempt's endpoint, model, and failure classification.
type ModelFailureObservation struct {
	NotFound       bool
	CodexPlanGated bool
	ModelKey       string
	ImageModel     bool
	ImagesEndpoint bool
}

// ApplyModelUnavailable pauses a provider/model pair subject to pool-mode and error-code policies.
func (s *HealthService) ApplyModelUnavailable(ctx context.Context, value *Record, status int, observation ModelFailureObservation) bool {
	if s == nil || s.providerRepo == nil || value == nil || value.IsPoolMode() || !value.ShouldHandleErrorCode(status) {
		return false
	}
	var cooldown time.Duration
	var reason string
	switch {
	case observation.NotFound:
		cooldown, reason = ModelNotFoundCooldown, ModelNotFoundReason
	case value.IsOpenAIOAuthLike() && observation.CodexPlanGated:
		cooldown, reason = CodexPlanGatedModelCooldown, CodexPlanGatedModelReason
	default:
		return false
	}
	if observation.ModelKey == "" {
		return false
	}
	// A text-endpoint plan rejection for an image model does not establish failure of its Images endpoint.
	if reason == CodexPlanGatedModelReason && !observation.ImagesEndpoint && observation.ImageModel {
		return true
	}
	reset := s.options.Now().Add(cooldown)
	if err := s.providerRepo.SetModelRateLimit(ctx, value.ID, observation.ModelKey, reset, reason); err != nil {
		s.options.Warn("upstream_model_not_found_set_model_rate_limit_failed", "provider_id", value.ID, "model", observation.ModelKey, "reason", reason, "error", err)
		return true
	}
	s.options.Info("upstream_model_not_found_model_rate_limited", "provider_id", value.ID, "model", observation.ModelKey, "reason", reason, "reset_at", reset)
	return true
}
