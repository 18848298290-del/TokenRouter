package provider

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	anthropicupstream "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	"github.com/TokenFlux/TokenRouter/internal/upstream/jev"
	"github.com/TokenFlux/TokenRouter/internal/upstream/kimi"
)

// HealthObservation carries the failed request's model and endpoint information for health decisions.
type HealthObservation struct {
	Status         int
	Headers        http.Header
	Body           []byte
	Model          string
	ModelProvided  bool
	EffectiveModel string
	Thinking       *bool
	ImagesEndpoint bool
}

// UpstreamHealth parses upstream observations and applies provider health rules.
type UpstreamHealth struct {
	Core   *providercore.HealthService
	Team   *providercore.TeamLinkedHealth
	Limits *RateLimitObserver
	Models *ModelHealth
}

// QuotaWindowObservation converts a parsed upstream window for provider health rules.
func QuotaWindowObservation(value *anthropicupstream.WindowLimit) *providercore.QuotaWindowObservation {
	if value == nil {
		return nil
	}
	return &providercore.QuotaWindowObservation{Window: value.Window, ResetAt: value.ResetAt, FiveHourReset: value.FiveHourReset, Reason: value.Reason}
}

// SessionWindowObservation extracts session status, reset time, and passive usage from headers.
func SessionWindowObservation(headers http.Header) providercore.SessionWindowObservation {
	return providercore.SessionWindowObservation{Status: headers.Get("anthropic-ratelimit-unified-5h-status"), Reset: headers.Get("anthropic-ratelimit-unified-5h-reset"), Passive: anthropicupstream.PassiveUsageFields(headers)}
}

// ClientRejectionObservation classifies rejection messages and detail.code values.
func ClientRejectionObservation(message string, body []byte) providercore.ClientRejectionObservation {
	lower := strings.ToLower(message)
	return providercore.ClientRejectionObservation{Message: message, OrganizationDisabled: strings.Contains(lower, "organization has been disabled"), CreditBalanceExhausted: strings.Contains(lower, "credit balance"), IdentityVerificationRequired: strings.Contains(lower, "identity verification is required"), WorkspaceDeactivated: gjson.GetBytes(body, "detail.code").String() == "deactivated_workspace"}
}

// ForbiddenObservation extracts rejection type, verification URL, and concurrency-limit evidence.
func ForbiddenObservation(value *providercore.Record, message string, body []byte) providercore.ForbiddenObservation {
	kind := antigravity.ClassifyForbiddenType(string(body))
	url := ""
	if kind == providercore.ForbiddenTypeValidation {
		url = antigravity.ExtractValidationURL(string(body))
	}
	return providercore.ForbiddenObservation{Message: message, Body: body, HTML: upstream.IsHTMLResponse(body), Kind: kind, ValidationURL: url, ConcurrentRequestLimited: CNConcurrencyLimit403(value, message), ConcurrencyReason: "cn_concurrency_limit: " + kimi.ConcurrentRequestLimitMessage}
}

// CNConcurrencyLimit403 reports whether a Kimi response describes a concurrency limit.
func CNConcurrencyLimit403(value *providercore.Record, message string) bool {
	return value != nil && value.Platform == providercore.PlatformKimi && kimi.IsConcurrencyLimitMessage(message)
}

// LogAPIKeyHealthWarning writes an API key health event to the application log.
func LogAPIKeyHealthWarning(message string, fields ...any) {
	attrs := make([]zap.Field, 0, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if ok {
			attrs = append(attrs, zap.Any(key, fields[i+1]))
		}
	}
	logging.L().Warn(message, attrs...)
}

// UnauthorizedObservation extracts authentication failure and token revocation evidence.
func UnauthorizedObservation(body []byte, message string) providercore.UnauthorizedObservation {
	code := upstream.ExtractErrorCode(body)
	return providercore.UnauthorizedObservation{Message: message, TokenRevoked: code == "token_invalidated" || code == "token_revoked", PermanentlyUnauthorized: gjson.GetBytes(body, "detail").String() == "Unauthorized"}
}

func (s *UpstreamHealth) CheckErrorPolicy(ctx context.Context, provider *providercore.Record, observation HealthObservation) providercore.ErrorPolicyResult {
	statusCode, responseBody := observation.Status, observation.Body
	if provider == nil {
		return providercore.ErrorPolicyNone
	}
	return s.Core.CheckErrorPolicy(ctx, provider, statusCode, responseBody, observation.EffectiveModel, provider == nil || provider.Platform != capability.PlatformAntigravity)
}

// ApplyExplicitErrorPolicy checks and applies administrator-defined error policy.
// A custom-code match writes the provider error and tells callers to skip built-in status handling.
func (s *UpstreamHealth) ApplyExplicitErrorPolicy(ctx context.Context, provider *providercore.Record, observation HealthObservation) providercore.ErrorPolicyResult {
	statusCode, responseBody := observation.Status, observation.Body
	result := s.CheckErrorPolicy(ctx, provider, observation)
	if result != providercore.ErrorPolicyCustomMatched || provider == nil {
		return result
	}
	message := logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(responseBody)))
	if message == "" {
		message = "Custom error code triggered"
	}
	s.Core.ApplyCustomErrorCode(ctx, provider, statusCode, message)
	return result
}

// ApplyUpstreamError applies configured policy before default handling for non-pool providers.
func (s *UpstreamHealth) ApplyUpstreamError(ctx context.Context, provider *providercore.Record, observation HealthObservation) providercore.UpstreamErrorDecision {
	statusCode, responseBody := observation.Status, observation.Body
	// Team workspace suspension precedes pool, custom-code, and temporary suspension checks.
	// HandleWorkspaceDeactivated deduplicates calls from the fast path.
	s.Team.HandleWorkspaceDeactivated(ctx, provider, statusCode == http.StatusPaymentRequired && ClientRejectionObservation("", responseBody).WorkspaceDeactivated)
	policy := providercore.ErrorPolicyNone
	// Pool and custom-code providers evaluate policy before platform defaults.
	if provider != nil && (provider.IsPoolMode() || provider.IsCustomErrorCodesEnabled()) {
		policy = s.ApplyExplicitErrorPolicy(ctx, provider, observation)
	}
	decision := providercore.UpstreamErrorDecision{Policy: policy}
	switch policy {
	case providercore.ErrorPolicyCustomMatched, providercore.ErrorPolicyTempUnscheduled:
		decision.StopScheduling = true
		return decision
	case providercore.ErrorPolicyCustomSkipped, providercore.ErrorPolicyPoolBypassed:
		return decision
	}
	decision.StopScheduling = s.HandleDefault(ctx, provider, observation)
	return decision
}

// HandleDefault applies platform health rules after callers resolve pool and custom-code policy.
func (s *UpstreamHealth) HandleDefault(ctx context.Context, provider *providercore.Record, observation HealthObservation) (shouldDisable bool) {
	statusCode, headers, responseBody := observation.Status, observation.Headers, observation.Body
	if provider == nil {
		return false
	}
	// Jev rate-limit and overload responses use Retry-After for cooldown.
	if provider.Platform == capability.PlatformJev && (statusCode == http.StatusTooManyRequests || statusCode == 529) {
		if reset := jev.RetryAfterResetTime(headers, time.Now()); reset != nil {
			s.Core.ApplyObservedRateLimit(ctx, provider, *reset)
			return false
		}
		// A 529 without Retry-After can match an administrator's temporary suspension rule.
		if statusCode == 529 && s.Core.TryTempUnschedulable(ctx, provider, statusCode, responseBody, true, observation.EffectiveModel) {
			return true
		}
	}

	if statusCode == 529 {
		s.Core.ApplyOverload(ctx, provider)
		return false
	}

	if observation.ModelProvided && s.Models.Observe(ctx, provider, observation.Model, statusCode, responseBody, observation.Thinking, observation.ImagesEndpoint) {
		return true
	}

	// Apply exhausted Anthropic 5h/7d windows before local temporary suspension rules.
	// A broad 429 keyword rule could otherwise shorten an hours- or days-long cooldown.
	if statusCode == http.StatusTooManyRequests && provider.Platform == capability.PlatformAnthropic {
		// The Fable-only 7d_oi window pauses its models while other models remain schedulable.
		fableLimited := s.observeFableWindow(ctx, provider, headers)
		if s.observeExhaustedWindow(ctx, provider, headers) {
			return false
		}
		if fableLimited {
			return false
		}
	}

	// A 529 denotes upstream-wide overload and records the global cooldown.
	if statusCode == 529 {
		s.Core.ApplyOverload(ctx, provider)
		return false
	}
	// Let 401 reach authentication recovery and its default cooldown before temporary rules.
	if statusCode != http.StatusUnauthorized && s.Core.TryTempUnschedulable(ctx, provider, statusCode, responseBody, provider.Platform != capability.PlatformAntigravity, observation.EffectiveModel) {
		return true
	}

	upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(responseBody))
	upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
	if upstreamMsg != "" {
		upstreamMsg = logredact.TruncateLine([]byte(upstreamMsg), 512)
	}

	switch statusCode {
	case 400:
		shouldDisable = s.Core.ApplyBadRequest(ctx, provider, ClientRejectionObservation(upstreamMsg, responseBody))

	case 401:
		shouldDisable = s.Core.ApplyUnauthorized(ctx, provider, UnauthorizedObservation(responseBody, upstreamMsg))

	case 402:
		shouldDisable = s.Core.ApplyPaymentRequired(ctx, provider, ClientRejectionObservation(upstreamMsg, responseBody))

	case 403:
		logging.LegacyPrintf(
			"service.ratelimit",
			"[HandleUpstreamErrorRaw] provider_id=%d platform=%s type=%s status=403 request_id=%s cf_ray=%s upstream_msg=%s raw_body=%s",
			provider.ID,
			provider.Platform,
			provider.Type,
			strings.TrimSpace(headers.Get("x-request-id")),
			strings.TrimSpace(headers.Get("cf-ray")),
			upstreamMsg,
			logredact.TruncateLine(responseBody, 1024),
		)
		shouldDisable = s.Core.ApplyForbiddenObservation(ctx, provider, ForbiddenObservation(provider, upstreamMsg, responseBody))
	case 429:
		s.Limits.Observe429(ctx, provider, headers, responseBody)
		shouldDisable = false
	case 529:
		s.Core.ApplyOverload(ctx, provider)
		shouldDisable = false
	default:
		if statusCode >= 500 {
			// Unhandled 5xx responses are logged without disabling the provider.
			slog.Warn("provider_upstream_error", "provider_id", provider.ID, "status_code", statusCode)
			shouldDisable = false
		}
	}

	return shouldDisable
}

func (s *UpstreamHealth) observeFableWindow(ctx context.Context, value *providercore.Record, headers http.Header) bool {
	limit := anthropicupstream.SelectFableWindowLimit(headers, time.Now())
	if limit == nil {
		return false
	}
	return s.Core.ApplyFableQuotaWindow(ctx, value, QuotaWindowObservation(limit), anthropicupstream.PassiveUsageFields(headers))
}

func (s *UpstreamHealth) observeExhaustedWindow(ctx context.Context, value *providercore.Record, headers http.Header) bool {
	now := time.Now()
	return s.Core.ApplyExhaustedQuotaWindow(ctx, value, QuotaWindowObservation(anthropicupstream.SelectExhaustedWindow(headers, now)), now)
}
