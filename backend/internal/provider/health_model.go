package provider

import (
	"context"
	"time"
)

// An unavailable model pauses a provider/model pair while other model scopes
// remain selectable. A not-found classification takes precedence over an
// OAuth-like Codex plan restriction; an absent canonical model key leaves no
// model-specific pause to write, and pool or skipped-code policy prevents local
// handling.
//
// Endpoint intent matters for plan-gated image models. A rejection through a
// text endpoint is handled without installing a model cooldown because it does
// not establish that the same model's Images endpoint is unavailable. Ordinary
// matched model failures use their fixed duration and reason in one model-limit
// call. A logged write failure retains the handled result, letting the caller
// keep the failure scoped to its current model.

// Model-unavailability durations and reason values pair not-found or Codex
// plan-gated observations with their dedicated stored model cooldowns.
const (
	ModelNotFoundCooldown       = 30 * time.Minute
	ModelNotFoundReason         = "upstream_404_model_not_found"
	CodexPlanGatedModelCooldown = 30 * time.Minute
	CodexPlanGatedModelReason   = "upstream_400_codex_plan_gated_model"
)

// ModelFailureObservation holds a canonical model key and classified
// failure/endpoint intent for one attempt.
type ModelFailureObservation struct {
	// NotFound and CodexPlanGated supply ordered classification. NotFound selects
	// its cooldown first; a plan-gated observation requires an OAuth-like OpenAI
	// record before it selects its own reason.
	NotFound       bool // missing model classification
	CodexPlanGated bool // subscription plan restriction
	// ModelKey identifies the normalized scheduling scope. ApplyModelUnavailable
	// requires a nonempty key before its write or handled image-endpoint exception
	// can be selected.
	ModelKey string
	// ImageModel and ImagesEndpoint keep plan-gated text observations separate
	// from native image intent. Their text/image combination can return handled
	// before writing the model scope.
	ImageModel     bool // image model classification
	ImagesEndpoint bool // native image endpoint
}

// ApplyModelUnavailable selects a model cooldown from the current attempt's
// classified failure. Missing dependencies, pool mode, skipped codes, an
// unmatched classification or an empty canonical ModelKey return false.
// NotFound takes precedence; CodexPlanGated qualifies only for OAuth-like
// OpenAI records.
//
// A plan-gated image model seen through a text endpoint returns true before
// persistence, retaining the separate Images route. Other selected cases write
// the model scope with its fixed duration and reason. Logged storage failure
// still returns true, allowing the caller to retain this known model rejection
// without promoting it to provider-wide classification.
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
	// A text-endpoint plan rejection for an image model does not establish failure
	// of its Images endpoint. Keep the handled result while leaving that model's
	// native image eligibility available.
	if reason == CodexPlanGatedModelReason && !observation.ImagesEndpoint && observation.ImageModel {
		return true
	}
	reset := s.options.Now().Add(cooldown)
	// A failed model-limit write still leaves the upstream rejection classified.
	// Log persistence failure and keep the true result so the caller retains the
	// selected model scope.
	if err := s.providerRepo.SetModelRateLimit(ctx, value.ID, observation.ModelKey, reset, reason); err != nil {
		s.options.Warn("upstream_model_not_found_set_model_rate_limit_failed", "provider_id", value.ID, "model", observation.ModelKey, "reason", reason, "error", err)
		return true
	}
	s.options.Info("upstream_model_not_found_model_rate_limited", "provider_id", value.ID, "model", observation.ModelKey, "reason", reason, "reset_at", reset)
	return true
}
