package forward

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
)

// Provider failover combines the upstream outcome with retry instructions and
// the caller's remaining attempt and output window. An explicit stop action
// ends provider switching, while the zero action keeps legacy retry permission.
// Credential failures additionally name their scope so a shared configuration
// problem can stop switching without being classified as one provider's inference failure.
//
// RetryFailure transfers budget-relevant values to the request's failover
// state, leaving display overrides and post-write output eligibility with their
// HTTP callers. Same-provider delays, deadlines, and count caps interact in that
// state; a deadline without a specific cap can use its time window beyond the
// ordinary pool count. Scheduling feedback and API-key health classification
// have separate gates, so request-transient metadata alone does not change
// ShouldReportProviderScheduleFailure's stage-and-scope decision.

// Provider actions govern whether callers may continue selection after the
// current failed attempt, independently of their output and retry-budget limits.
const (
	NextProviderLegacyRetry NextProviderAction = iota // legacy provider-switch permission
	NextProviderRetry                                 // explicit provider-switch permission
	NextProviderStop                                  // stop further provider switching

	// Client messages and stable reasons identify gateway and credential failures
	// for protocol output, request diagnostics, and provider-recovery decisions.
	OpenAIRequestBodyTooLargeClientMessage     = "Request payload is too large"
	OpenAIUpstreamAccessStateReason            = GatewayFailureReason("openai_upstream_access_state")
	OpenAIHTTPContinuationUnsupportedReason    = GatewayFailureReason("openai_http_continuation_unsupported")
	AntigravityCredentialRejectedClientMessage = "Antigravity rejected the OAuth credential after refresh; reauthorize the provider and verify project_id"
	AntigravityCredentialRejectedReason        = GatewayFailureReason("antigravity_oauth_credential_rejected")

	// Silent-refusal values share one identifying code while retaining separate
	// upstream diagnostic and exhausted-failover messages for their respective consumers.
	openAISilentRefusalErrorCode       = "openai_silent_refusal"
	openAISilentRefusalUpstreamMessage = "OpenAI upstream returned an empty completion stream with finish_reason=stop and no usage"
	openAISilentRefusalClientMessage   = "Upstream returned an empty completion without usage; no fallback provider was available"

	// Failure stages distinguish execution outcomes from credential acquisition
	// when callers choose formatting, switching, and provider-feedback behavior.
	GatewayFailureStageInference    GatewayFailureStage = "inference"     // upstream inference failure
	GatewayFailureStageProviderAuth GatewayFailureStage = "provider_auth" // credential acquisition failure

	// Failure scopes identify the affected resource so credential failures can
	// be reported against a provider or handled as shared/request conditions.
	GatewayFailureScopeProvider GatewayFailureScope = "provider" // selected provider failure
	GatewayFailureScopeShared   GatewayFailureScope = "shared"   // shared configuration/state failure
	GatewayFailureScopeRequest  GatewayFailureScope = "request"  // request-specific failure condition
)

// AgentIdentityTaskRecoveredError signals that an identity task recovered and the caller may retry.
type AgentIdentityTaskRecoveredError struct{}

// GatewayFailureStage identifies the failure stage; an empty stage follows inference handling.
type GatewayFailureStage string

// GatewayFailureScope distinguishes provider, shared-credential, and request failures for failover decisions.
type GatewayFailureScope string

// NextProviderAction controls provider switching; the zero value permits retry and NextProviderStop ends switching.
type NextProviderAction uint8

// GatewayFailureReason is the stable diagnostic label attached to a forwarding failure.
type GatewayFailureReason string

// UpstreamFailoverError carries an upstream or credential failure that may trigger provider switching.
// The caller combines its action with response status and remaining retry budget.
type UpstreamFailoverError struct {
	StatusCode int // classified upstream HTTP status
	// ResponseBody and ResponseHeaders carry captured failure evidence for
	// classification and output. Assigning this error shares body, map, and header
	// slice storage; constructors determine whether that evidence was cloned first.
	ResponseBody    []byte              // captured upstream error body
	ResponseHeaders map[string][]string // captured upstream response headers
	// ForceCacheBilling requests cache-read treatment of uncached input. The
	// failover state honors this flag even when no sticky session is bound.
	ForceCacheBilling bool
	// RetryableOnSameProvider permits retries before provider switching. Actual
	// eligibility still depends on the caller's count cap and the supplied deadline.
	RetryableOnSameProvider bool
	// SameProviderRetryDelay, SameProviderRetryDeadline, and SameProviderRetryMax
	// are interpreted together by failover state. A positive delay overrides
	// backoff; a deadline without a specific cap uses a time window beyond the
	// ordinary pool count. A positive cap requires a positive caller count limit
	// and uses the smaller of those two limits.
	SameProviderRetryDelay    time.Duration // requested same-provider wait duration
	SameProviderRetryDeadline time.Time     // same-provider retry window deadline
	SameProviderRetryMax      int           // error-specific retry count cap
	// RequestScopedTransient identifies provider-independent transient capacity.
	// Failover uses its capped backoff, and API-key health classification and
	// retry cooldown exclude it; scheduling feedback applies its own stage/scope rule.
	RequestScopedTransient bool
	// SafeToFailoverAfterWrite supplies the caller's post-write recovery evidence.
	// Output adapters still decide whether actual writes leave the request replayable.
	SafeToFailoverAfterWrite bool
	Stage                    GatewayFailureStage  // inference or credential stage
	Scope                    GatewayFailureScope  // affected failure resource scope
	Reason                   GatewayFailureReason // stable failure diagnostic label
	NextProviderAction       NextProviderAction   // provider-switch permission or stop
	// ClientStatusCode and ClientMessage supply optional final-output overrides.
	// They remain outside RetryFailure, because HTTP error writers decide when
	// an exhausted request uses these display values.
	ClientStatusCode int    // optional client HTTP status
	ClientMessage    string // optional client error message
}

// Error returns the fixed marker text identifying a recovered agent-identity task.
func (e *AgentIdentityTaskRecoveredError) Error() string { return "agent identity task recovered" }

// OpenAISilentRefusalErrorBody builds the gateway JSON error payload for an
// empty completion without usage. Encoding failure returns a literal envelope
// carrying the same silent-refusal code, so callers receive a classifiable body
// through either path.
func OpenAISilentRefusalErrorBody() []byte {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "upstream_error",
			"code":    openAISilentRefusalErrorCode,
			"message": openAISilentRefusalUpstreamMessage,
		},
	})
	if err != nil {
		// The envelope uses string values, and its literal backup retains the
		// same identifying code if encoding fails. Error classifiers can therefore
		// recognize the helper's result through either construction path.
		return []byte(`{"error":{"type":"upstream_error","code":"openai_silent_refusal","message":"OpenAI upstream returned an empty completion stream with finish_reason=stop and no usage"}}`)
	}
	return body
}

// IsOpenAISilentRefusalErrorBody matches the trimmed error.code against the
// gateway's silent-refusal marker. It uses GJSON path extraction without full
// JSON validation; callers needing a decoded payload apply their own parser.
func IsOpenAISilentRefusalErrorBody(body []byte) bool {
	return strings.TrimSpace(gjson.GetBytes(body, "error.code").String()) == openAISilentRefusalErrorCode
}

// OpenAISilentRefusalClientMessage returns the message for silent refusal after failover is exhausted.
func OpenAISilentRefusalClientMessage() string {
	return openAISilentRefusalClientMessage
}

// Error formats this failure's credential reason or upstream HTTP status.
// Call it on a nonnil error; credential-stage values use the reason label,
// while other stages use StatusCode in the diagnostic string.
func (e *UpstreamFailoverError) Error() string {
	if e != nil && e.Stage == GatewayFailureStageProviderAuth {
		return fmt.Sprintf("credential failure: %s (failover)", e.Reason)
	}
	return fmt.Sprintf("upstream error: %d (failover)", e.StatusCode)
}

// ShouldRetryNextProvider reports whether switching is permitted by this error.
// A nil error or explicit NextProviderStop returns false; zero and retry actions
// permit switching, subject to the caller's remaining output and attempt budget.
func (e *UpstreamFailoverError) ShouldRetryNextProvider() bool {
	return e != nil && e.NextProviderAction != NextProviderStop
}

// IsCredentialFailure reports whether the exact provider-auth stage is set.
// Nil errors, empty stages, and all other stage strings return false, following
// inference-style handling at callers that branch on this predicate.
func (e *UpstreamFailoverError) IsCredentialFailure() bool {
	return e != nil && e.Stage == GatewayFailureStageProviderAuth
}

// ShouldReportProviderScheduleFailure permits inference feedback and
// provider-scoped credential feedback, returning false for a nil error.
// Shared and request-scoped credential failures are excluded because this
// predicate attributes credential feedback to the selected provider.
//
// For noncredential stages it returns true regardless of Scope or
// RequestScopedTransient. API-key health and temporary cooldown apply separate
// eligibility checks, including that transient flag, so callers keep those
// decisions separate from scheduling feedback. A request-transient inference
// failure therefore remains reportable through this predicate itself.
func (e *UpstreamFailoverError) ShouldReportProviderScheduleFailure() bool {
	if e == nil {
		return false
	}
	return !e.IsCredentialFailure() || e.Scope == GatewayFailureScopeProvider
}

// RetryFailure creates the retry-budget view consumed by request failover state.
// A nil receiver returns nil; the new value copies scalar retry and cache-billing
// instructions, leaving captured response data and client-output overrides on
// this forwarding error.
func (e *UpstreamFailoverError) RetryFailure() *failover.FailureInfo {
	if e == nil {
		return nil
	}
	return &failover.FailureInfo{StatusCode: e.StatusCode, ForceCacheBilling: e.ForceCacheBilling, RetryableOnSameProvider: e.RetryableOnSameProvider, RequestScopedTransient: e.RequestScopedTransient, SameProviderRetryDelay: e.SameProviderRetryDelay, SameProviderRetryDeadline: e.SameProviderRetryDeadline, SameProviderRetryMax: e.SameProviderRetryMax, RetryNext: e.ShouldRetryNextProvider()}
}
