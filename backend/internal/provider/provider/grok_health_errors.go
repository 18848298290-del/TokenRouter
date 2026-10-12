package provider

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// GrokHealthInput contains the model and health observations for one attempt.
type GrokHealthInput struct {
	Observation     HealthObservation
	Models          []string
	QuotaModel      string
	TeamModel       string
	RequestScoped   bool
	ServerTransient bool
}

func firstGrokHealthModel(models []string) string {
	if len(models) == 0 {
		return ""
	}
	return strings.TrimSpace(models[0])
}

// ObserveError records quota observations and applies configured, model-specific, and default health rules.
func (s *GrokHealth) ObserveError(ctx context.Context, value *providercore.Record, input GrokHealthInput) providercore.UpstreamErrorDecision {
	statusCode, headers, responseBody := input.Observation.Status, input.Observation.Headers, input.Observation.Body
	canonicalModel := input.Models

	if s == nil || value == nil {
		return providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone}
	}
	if input.RequestScoped {
		return providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone}
	}
	if model := firstGrokHealthModel(canonicalModel); model != "" {
		canonicalModel = []string{s.NormalizeModel(value, model)}
	}
	stateCtx, cancel := ProviderStateContext(ctx)
	defer cancel()
	observation := input.Observation
	observation.Headers = nil
	observation.ModelProvided = len(canonicalModel) > 0
	if len(canonicalModel) > 0 {
		observation.Model = canonicalModel[0]
	}
	if model := firstGrokHealthModel(canonicalModel); model != "" {
		observation.EffectiveModel = model
	}

	now := time.Now()
	quotaSnapshot := grok.ParseQuotaObservation(headers, statusCode, now)
	quotaModel := firstGrokHealthModel(canonicalModel)
	if quotaModel == "" {
		quotaModel = input.QuotaModel
	}
	providercore.StampGrokQuotaPlan(providercore.CloneRecord(value), quotaSnapshot, quotaModel, grok.ResolveGrokTextResponsesModelID, grok.ApplyGrok45ResponsesPlanSignal)
	// Capacity errors save quota observations without installing a provider-wide rate limit.
	snapshotFailure := grok.ClassifyGrokUpstreamFailure(statusCode, responseBody, quotaModel)
	s.StoreSnapshot(stateCtx, value, quotaSnapshot, snapshotFailure.Class != grok.GrokFailureModelCapacity, input.TeamModel)

	decision := providercore.ErrorDecisionWithoutPersistence(value, statusCode)
	if s.Health != nil {
		if value.IsPoolMode() || value.IsCustomErrorCodesEnabled() {
			decision.Policy = s.Health.ApplyExplicitErrorPolicy(stateCtx, providercore.CloneRecord(value), observation)
			decision.StopScheduling = decision.Policy == providercore.ErrorPolicyCustomMatched || decision.Policy == providercore.ErrorPolicyTempUnscheduled
		} else {
			decision = providercore.UpstreamErrorDecision{Policy: providercore.ErrorPolicyNone}
		}
	}
	switch decision.Policy {
	case providercore.ErrorPolicyCustomMatched:
		decision.StopScheduling = true
		s.Runtime.BlockProviderScheduling(value, time.Time{}, "upstream_disable")
		return decision
	case providercore.ErrorPolicyTempUnscheduled:
		decision.StopScheduling = true
		return decision
	case providercore.ErrorPolicyCustomSkipped, providercore.ErrorPolicyPoolBypassed:
		return decision
	}

	if s.Health != nil && len(canonicalModel) > 0 &&
		s.Health.Models.Observe(stateCtx, providercore.CloneRecord(value), canonicalModel[0], statusCode, responseBody, observation.Thinking, observation.ImagesEndpoint) {
		decision.StopScheduling = true
		return decision
	}
	// Handle precise model errors before administrator-defined temporary rules for ordinary providers.
	if s.Health != nil && statusCode != http.StatusUnauthorized &&
		!value.IsPoolMode() && !value.IsCustomErrorCodesEnabled() &&
		s.Health.Core.HandleTempUnschedulable(stateCtx, providercore.CloneRecord(value), statusCode, responseBody, observation.EffectiveModel) {
		decision.Policy = providercore.ErrorPolicyTempUnscheduled
		decision.StopScheduling = true
		if firstGrokHealthModel(canonicalModel) == "" {
			s.Runtime.BlockProviderScheduling(value, time.Time{}, "upstream_disable")
		}
		return decision
	}

	// API key transient 5xx failures use shared provider/model cooldowns when the final model is known.
	// OAuth and unknown-model requests continue through provider-wide health rules.
	model := firstGrokHealthModel(canonicalModel)
	if model == "" {
		model = quotaModel
	}
	if value.Type == capability.ProviderTypeAPIKey && model != "" && statusCode >= 500 &&
		input.ServerTransient {
		s.recordModelTransient(value, model)
		return decision
	}

	// Classify free quota, billing, empty output, and capacity failures before generic status handling.
	failure := grok.ClassifyGrokUpstreamFailure(statusCode, responseBody, model)
	if failure.ShouldCooldown && failure.Class != grok.GrokFailureNone && failure.Class != grok.GrokFailureRateLimit {
		if failure.Class == grok.GrokFailureFreeUsage {
			if resetAt, limited := providercore.GrokRateLimitResetAtForProvider(value, quotaSnapshot, now); limited && resetAt.After(now) {
				if failure.Model != "" && providercore.IsGrokModelSpecificFreeUsage(strings.ToLower(failure.Reason), failure.Model) {
					providercore.MarkGrokModelQuotaBlock(value.ID, failure.Model, resetAt)
					decision.StopScheduling = true
					return decision
				}
				s.RateLimit(stateCtx, value, resetAt, input.TeamModel)
				decision.StopScheduling = true
				return decision
			}
		}
		if s.ApplyFailure(stateCtx, value, failure, input.TeamModel) {
			decision.StopScheduling = true
			return decision
		}
	}

	switch statusCode {
	case http.StatusUnauthorized:
		s.TempUnschedule(stateCtx, value, 10*time.Minute, "grok credentials unauthorized")
		decision.StopScheduling = true
		return decision
	case http.StatusPaymentRequired:
		s.TempUnschedule(stateCtx, value, 30*time.Minute, "grok payment required")
		decision.StopScheduling = true
		return decision
	case http.StatusForbidden:
		if s.applyForbiddenPolicy(stateCtx, value, responseBody, observation.EffectiveModel) {
			decision.StopScheduling = true
			return decision
		}
		s.TempUnschedule(stateCtx, value, 30*time.Minute, "grok access or entitlement denied")
		decision.StopScheduling = true
		return decision
	case http.StatusMethodNotAllowed:
		s.TempUnschedule(stateCtx, value, 30*time.Minute, "grok endpoint not supported (405)")
		decision.StopScheduling = true
		return decision
	case http.StatusTooManyRequests:
		// Snapshot handling above installs any observed active rate limit.
		decision.StopScheduling = true
		return decision
	default:
		if statusCode >= 500 {
			s.TempUnschedule(stateCtx, value, 2*time.Minute, "grok upstream temporary error")
			decision.StopScheduling = true
			return decision
		}
	}
	return decision
}

// ApplyFailure applies a classified failure to provider health. A true result tells
// the caller to skip generic status handling, including when the failure leaves health unchanged.
func (s *GrokHealth) ApplyFailure(
	ctx context.Context,
	value *providercore.Record,
	decision grok.GrokUpstreamFailureDecision, teamModel string,
) bool {
	if s == nil || value == nil || !decision.ShouldCooldown || decision.Cooldown <= 0 {
		return false
	}
	// These reasons are persisted for the operations UI.
	var reason string
	switch decision.Class {
	case grok.GrokFailureFreeUsage:
		reason = "grok free usage exhausted"
		// A model-specific free-quota limit leaves other models on this provider available.
		low := strings.ToLower(decision.Reason)
		if decision.Model != "" && providercore.IsGrokModelSpecificFreeUsage(low, decision.Model) {
			until := time.Now().Add(decision.Cooldown)
			providercore.MarkGrokModelQuotaBlock(value.ID, decision.Model, until)
			return true
		}
	case grok.GrokFailureBilling:
		low := strings.ToLower(decision.Reason)
		if strings.Contains(low, "spending") || strings.Contains(low, "credits") {
			// Spending and credit limits use a recoverable billing window cleared by normal rate-limit recovery.
			s.RateLimit(ctx, value, providercore.GrokSpendingLimitResetAt(providercore.CloneRecord(value), time.Now()), teamModel)
			return true
		}
		reason = "grok payment required"
	case grok.GrokFailureEmptyUpstream:
		reason = "grok empty model output"
	case grok.GrokFailureModelCapacity:
		// Capacity pressure cools the requested model; the caller decides whether to switch providers.
		if model := strings.TrimSpace(decision.Model); model != "" {
			cooldown := decision.Cooldown
			if cooldown <= 0 {
				cooldown = 3 * time.Minute
			}
			providercore.MarkGrokModelTransientBlock(value.ID, model, time.Now().Add(cooldown))
		}
		return true
	case grok.GrokFailureRateLimit:
		// Generic 429 handling uses quota headers and Retry-After outside this classifier.
		return false
	case grok.GrokFailureServer:
		reason = "grok upstream temporary error"
	case grok.GrokFailureCompatibility:
		// Compatibility failures leave health unchanged for the caller to handle through failover.
		return true
	default:
		return false
	}
	s.TempUnschedule(ctx, value, decision.Cooldown, reason)
	return true
}

// applyForbiddenPolicy applies configured 403 cooldowns and reports whether a rule matched.
func (s *GrokHealth) applyForbiddenPolicy(ctx context.Context, value *providercore.Record, responseBody []byte, effectiveModel string) bool {
	if value == nil || !value.IsTempUnschedulableEnabled() {
		return false
	}

	matches := providercore.MatchTempUnschedulableRules(providercore.CloneRecord(value), http.StatusForbidden, responseBody)
	if len(matches) == 0 {
		return false
	}

	match := matches[0]
	// Use the shared policy service for reason fields and cache writes when available.
	if s != nil && s.Health != nil &&
		s.Health.Limits.Plans !=
			nil {
		stateCtx, cancel := ProviderStateContext(ctx)
		handled := s.Health.Core.TryTempUnschedulable(stateCtx, providercore.CloneRecord(value), http.StatusForbidden, responseBody, value.Platform != capability.PlatformAntigravity, effectiveModel)

		cancel()
		if handled {
			return true
		}
	}

	// When the shared service is unavailable, still honor the configured duration.
	cooldown := time.Duration(match.Rule.DurationMinutes) * time.Minute
	if cooldown > 0 {
		s.TempUnschedule(ctx, value, cooldown, "grok configured forbidden rule")
	}
	return true
}

// recordModelTransient records a failure in the provider/model state shared with scheduling.
func (s *GrokHealth) recordModelTransient(value *providercore.Record, model string) {
	if s.ModelTransient == nil || value == nil {
		return
	}
	model = providercore.NormalizeTransientModel(model)
	decision := s.ModelTransient.RecordFailure(value.ID, model, time.Now())
	if decision.FailureStreak == 0 {
		return
	}
	slog.Warn("openai_model_transient_state", "provider_id", value.ID, "platform", value.Platform, "model", model, "failure_streak", decision.FailureStreak, "cooldown_ms", decision.Cooldown.Milliseconds(), "block_scope", "provider_model")
}
