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

// Grok failures can be request-specific, model-limited, quota-limited or
// provider-wide. RequestScoped errors return before quota or health writes.
// Other failures first retain quota headers, then apply pool/custom policy,
// model rules and ordinary temporary policy before selecting default cooldowns.
//
// That ordering lets explicit policy consume an error and keeps known API-key
// transient failures on the provider/model tracker. Classified free quota can
// choose a model window or provider reset, spending limits use recoverable
// billing state, and compatibility failures leave health for caller failover.
// The returned decision reports selection behavior even when best-effort writes
// fail or a handled classification intentionally installs no state.

// GrokHealthInput contains the model and health observations for one attempt.
type GrokHealthInput struct {
	Observation HealthObservation // failed exchange health data
	// Models supplies the first attempted model for normalization. QuotaModel fills
	// a missing classification name, while TeamModel separately names the shared
	// OAuth team window used by provider-level limit writes.
	Models     []string // attempted model-name input
	QuotaModel string   // alternate quota-classification model
	TeamModel  string   // shared team-window model
	// RequestScoped suppresses quota and health observation before any state
	// context
	// is created, leaving a content-specific failure with the request caller.
	RequestScoped bool
	// ServerTransient selects the API-key provider/model tracker for a known model
	// on 5xx. This branch returns even when ModelTransient is not configured.
	ServerTransient bool
}

// firstGrokHealthModel returns the trimmed first supplied model or an empty string.
// Later entries do not supply a replacement for an empty first entry. Callers
// use the result to decide whether another classification name is needed.
func firstGrokHealthModel(models []string) string {
	if len(models) == 0 {
		return ""
	}
	return strings.TrimSpace(models[0])
}

// ObserveError applies Grok quota, explicit policy, model and default health
// decisions.
// Nil service, nil record and RequestScoped input return no policy decision.
// A nonblank first Models entry requires NormalizeModel and becomes the canonical
// name before quota stamping. State operations use the detached five-second
// context, and StoreSnapshot can mutate the borrowed record and snapshot.
//
// Pool and custom policy decisions precede model handling; ordinary temporary
// rules follow precise model observation. Known API-key server transients use
// ModelTransient, while classified free quota and billing choose their specific
// windows before generic HTTP cooldowns. StopScheduling reports the handling
// decision, not guaranteed persistence of every best-effort state write.
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
	// Classification can happen after request cancellation. Use the state budget
	// so accepted health effects remain eligible while the upstream failure is
	// returned.
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
	// Retain capacity quota headers for display while disabling default provider
	// limit installation from that snapshot; later classification can apply
	// model-specific handling.
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
		// An explicit disable decision also blocks this process immediately before
		// returning; RuntimeBlockState resolves its zero deadline to the
		// default local window.
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
	// Ordinary providers check precise model handling first so a model-specific
	// failure does not get widened by a matching provider temporary rule afterward.
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

	// A known API-key transient model uses its shared failure tracker before
	// default provider handling. This selected branch returns even if that tracker
	// is absent, so missing configuration does not imply a provider-wide fallback.
	model := firstGrokHealthModel(canonicalModel)
	if model == "" {
		model = quotaModel
	}
	if value.Type == capability.ProviderTypeAPIKey && model != "" && statusCode >= 500 &&
		input.ServerTransient {
		s.recordModelTransient(value, model)
		return decision
	}

	// Classified failure text can select model quota or recoverable billing state
	// more precisely than its HTTP status, so apply it before generic status
	// cooldowns.
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

	// Default status windows apply after more precise policy and classification:
	// authentication uses ten minutes, billing/access/unsupported endpoints thirty,
	// and remaining server failures two before the provider can be reconsidered.
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
		// Quota observation has already attempted its active window handling.
		// Return
		// the scheduling decision here without inventing another deadline from
		// status alone.
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

// ApplyFailure consumes a classified cooldown decision and reports whether
// handling is complete.
// Nil service/record or nonpositive cooldown returns false. Model-specific free
// quota and capacity can install model windows; spending or credit reasons use
// recoverable provider limiting. Compatibility can return true with no health
// write, while generic rate-limit classification returns false for outer handling.
// A blank capacity model is also consumed without installing a window.
func (s *GrokHealth) ApplyFailure(
	ctx context.Context,
	value *providercore.Record,
	decision grok.GrokUpstreamFailureDecision, teamModel string,
) bool {
	if s == nil || value == nil || !decision.ShouldCooldown || decision.Cooldown <= 0 {
		return false
	}
	// The reason chosen by the handled classification reaches temporary-state
	// storage.
	var reason string
	switch decision.Class {
	case grok.GrokFailureFreeUsage:
		reason = "grok free usage exhausted"
		// Free-quota text can identify one model. Install that model key so other
		// model requests are not excluded by a provider-wide temporary state write.
		low := strings.ToLower(decision.Reason)
		if decision.Model != "" && providercore.IsGrokModelSpecificFreeUsage(low, decision.Model) {
			until := time.Now().Add(decision.Cooldown)
			providercore.MarkGrokModelQuotaBlock(value.ID, decision.Model, until)
			return true
		}
	case grok.GrokFailureBilling:
		low := strings.ToLower(decision.Reason)
		if strings.Contains(low, "spending") || strings.Contains(low, "credits") {
			// Spending and credit reasons select the recoverable billing
			// deadline helper.
			// Later successful OAuth observations can compare stored
			// timestamps before clearing that window.
			s.RateLimit(ctx, value, providercore.GrokSpendingLimitResetAt(providercore.CloneRecord(value), time.Now()), teamModel)
			return true
		}
		reason = "grok payment required"
	case grok.GrokFailureEmptyUpstream:
		reason = "grok empty model output"
	case grok.GrokFailureModelCapacity:
		// Capacity is consumed at model scope. A usable model gets a transient
		// window;
		// a blank model still reports handled so generic provider cooldown
		// does not widen it.
		if model := strings.TrimSpace(decision.Model); model != "" {
			cooldown := decision.Cooldown
			// The outer guard already rejects nonpositive decision cooldowns.
			// This defensive default is unreachable for the capacity decisions
			// admitted by the current call path.
			if cooldown <= 0 {
				cooldown = 3 * time.Minute
			}
			providercore.MarkGrokModelTransientBlock(value.ID, model, time.Now().Add(cooldown))
		}
		return true
	case grok.GrokFailureRateLimit:
		// Generic rate limits remain unconsumed here because the outer observer has
		// already saved header timing and retains its dedicated 429 handling
		// decision.
		return false
	case grok.GrokFailureServer:
		reason = "grok upstream temporary error"
	case grok.GrokFailureCompatibility:
		// Compatibility failures belong to the caller failover path. Report
		// handling
		// complete here so generic HTTP processing cannot turn them into
		// provider cooldown.
		return true
	default:
		return false
	}
	s.TempUnschedule(ctx, value, decision.Cooldown, reason)
	return true
}

// applyForbiddenPolicy consumes the first matched configured 403 temporary rule.
// Missing rule support or no match returns false. It tries the shared policy
// service when available, then uses a positive configured duration directly.
// A matched nonpositive duration still returns true with no direct fallback write.
func (s *GrokHealth) applyForbiddenPolicy(ctx context.Context, value *providercore.Record, responseBody []byte, effectiveModel string) bool {
	if value == nil || !value.IsTempUnschedulableEnabled() {
		return false
	}

	matches := providercore.MatchTempUnschedulableRules(providercore.CloneRecord(value), http.StatusForbidden, responseBody)
	if len(matches) == 0 {
		return false
	}

	match := matches[0]
	// Prefer shared temporary-policy handling for its stored reason and cache
	// effects.
	// If it cannot handle the match, the configured duration still drives the
	// local fallback.
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

	// A matching rule remains consumed when the shared service did not handle it.
	// A positive duration writes temporary state; nonpositive duration leaves
	// no direct cooldown.
	cooldown := time.Duration(match.Rule.DurationMinutes) * time.Minute
	if cooldown > 0 {
		s.TempUnschedule(ctx, value, cooldown, "grok configured forbidden rule")
	}
	return true
}

// recordModelTransient updates the shared provider/model failure tracker when
// configured.
// Nil tracker or provider is ignored. It normalizes the model before recording
// the failure, and logs a nonzero resulting streak with its selected cooldown.
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
