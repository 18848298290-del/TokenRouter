package forward

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
)

const (
	NextProviderLegacyRetry NextProviderAction = iota
	NextProviderRetry
	NextProviderStop

	OpenAIRequestBodyTooLargeClientMessage     = "Request payload is too large"
	OpenAIUpstreamAccessStateReason            = GatewayFailureReason("openai_upstream_access_state")
	OpenAIHTTPContinuationUnsupportedReason    = GatewayFailureReason("openai_http_continuation_unsupported")
	AntigravityCredentialRejectedClientMessage = "Antigravity rejected the OAuth credential after refresh; reauthorize the provider and verify project_id"
	AntigravityCredentialRejectedReason        = GatewayFailureReason("antigravity_oauth_credential_rejected")

	openAISilentRefusalErrorCode       = "openai_silent_refusal"
	openAISilentRefusalUpstreamMessage = "OpenAI upstream returned an empty completion stream with finish_reason=stop and no usage"
	openAISilentRefusalClientMessage   = "Upstream returned an empty completion without usage; no fallback provider was available"

	GatewayFailureStageInference    GatewayFailureStage = "inference"
	GatewayFailureStageProviderAuth GatewayFailureStage = "provider_auth"

	GatewayFailureScopeProvider GatewayFailureScope = "provider"
	GatewayFailureScopeShared   GatewayFailureScope = "shared"
	GatewayFailureScopeRequest  GatewayFailureScope = "request"
)

// AgentIdentityTaskRecoveredError signals that an identity task recovered and the caller may retry.
type AgentIdentityTaskRecoveredError struct{}

// GatewayFailureStage identifies the failure stage; an empty stage follows inference handling.
type GatewayFailureStage string

// GatewayFailureScope distinguishes provider, shared-credential, and request failures for failover decisions.
type GatewayFailureScope string

// NextProviderAction controls provider switching; the zero value permits retry and NextProviderStop ends switching.
type NextProviderAction uint8

type GatewayFailureReason string

// UpstreamFailoverError carries an upstream or credential failure that may trigger provider switching.
// The caller combines its action with response status and remaining retry budget.
type UpstreamFailoverError struct {
	StatusCode                int
	ResponseBody              []byte              // Upstream body used for error-rule matching.
	ResponseHeaders           map[string][]string // Upstream headers read by HTTP adapters.
	ForceCacheBilling         bool                // Bill input as cache reads after a sticky-session switch.
	RetryableOnSameProvider   bool                // Allow retries on this provider before switching.
	SameProviderRetryDelay    time.Duration
	SameProviderRetryDeadline time.Time
	SameProviderRetryMax      int  // Optional cap when lower than the caller retry budget.
	RequestScopedTransient    bool // Provider-independent transient failure; provider health must not be penalized for it.
	SafeToFailoverAfterWrite  bool // Allow failover when written bytes contain only control output, such as SSE comments.
	Stage                     GatewayFailureStage
	Scope                     GatewayFailureScope
	Reason                    GatewayFailureReason
	NextProviderAction        NextProviderAction
	ClientStatusCode          int
	ClientMessage             string
}

func (e *AgentIdentityTaskRecoveredError) Error() string { return "agent identity task recovered" }

// OpenAISilentRefusalErrorBody returns the gateway error payload for an empty completion without usage.
func OpenAISilentRefusalErrorBody() []byte {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "upstream_error",
			"code":    openAISilentRefusalErrorCode,
			"message": openAISilentRefusalUpstreamMessage,
		},
	})
	if err != nil {
		return []byte(`{"error":{"type":"upstream_error","code":"openai_silent_refusal","message":"OpenAI upstream returned an empty completion stream with finish_reason=stop and no usage"}}`)
	}
	return body
}

// IsOpenAISilentRefusalErrorBody checks for the gateway silent-refusal error code.
func IsOpenAISilentRefusalErrorBody(body []byte) bool {
	return strings.TrimSpace(gjson.GetBytes(body, "error.code").String()) == openAISilentRefusalErrorCode
}

// OpenAISilentRefusalClientMessage returns the message for silent refusal after failover is exhausted.
func OpenAISilentRefusalClientMessage() string {
	return openAISilentRefusalClientMessage
}

func (e *UpstreamFailoverError) Error() string {
	if e != nil && e.Stage == GatewayFailureStageProviderAuth {
		return fmt.Sprintf("credential failure: %s (failover)", e.Reason)
	}
	return fmt.Sprintf("upstream error: %d (failover)", e.StatusCode)
}

func (e *UpstreamFailoverError) ShouldRetryNextProvider() bool {
	return e != nil && e.NextProviderAction != NextProviderStop
}

func (e *UpstreamFailoverError) IsCredentialFailure() bool {
	return e != nil && e.Stage == GatewayFailureStageProviderAuth
}

// ShouldReportProviderScheduleFailure reports inference failures and provider-scoped credential failures.
// Shared and request-scoped credential failures do not count against provider scheduling health.
func (e *UpstreamFailoverError) ShouldReportProviderScheduleFailure() bool {
	if e == nil {
		return false
	}
	return !e.IsCredentialFailure() || e.Scope == GatewayFailureScopeProvider
}

func (e *UpstreamFailoverError) RetryFailure() *failover.FailureInfo {
	if e == nil {
		return nil
	}
	return &failover.FailureInfo{StatusCode: e.StatusCode, ForceCacheBilling: e.ForceCacheBilling, RetryableOnSameProvider: e.RetryableOnSameProvider, RequestScopedTransient: e.RequestScopedTransient, SameProviderRetryDelay: e.SameProviderRetryDelay, SameProviderRetryDeadline: e.SameProviderRetryDeadline, SameProviderRetryMax: e.SameProviderRetryMax, RetryNext: e.ShouldRetryNextProvider()}
}
