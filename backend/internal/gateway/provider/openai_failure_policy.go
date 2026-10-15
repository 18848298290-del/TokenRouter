package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openai_failure_policy.go derives OpenAI failover, retry timing, and client
// error metadata.
// OpenAI failures carry provider-switch decisions, same-provider retry
// windows, and client-facing status independently. Cyber and context failures
// remain request conditions, capacity can use bounded replay, and structured
// access-state or proxy body-size failures choose another provider with their
// own client response metadata.
//
// Response headers used for classification can differ from retained diagnostic
// headers. Eligible OAuth 429 handling derives a deadline from shared health
// and a delay bounded by Retry-After, its maximum, and remaining time. API Key
// health accounting excludes dedicated recovery, replay, and shared/request
// scope so those observations do not accumulate as provider outages. Callers
// also enforce request budgets and the prohibition on replay after actual
// output begins.

// These values identify proxy body-size failures and generic access-state
// client text, while default and maximum delays bound eligible OAuth 429 retries.
const (
	openAIRequestBodyTooLargeReason              = forwardcore.GatewayFailureReason("openai_request_body_too_large")
	openAIUpstreamAccessUnavailableClientMessage = "Upstream access is temporarily unavailable, please retry later"
	openAIOAuth429RetryDelay                     = 500 * time.Millisecond
	openAIOAuth429MaxRetryDelay                  = 8 * time.Second
)

// OpenAIFailoverPolicy combines shared response health with request retry
// metadata selection.
type OpenAIFailoverPolicy struct {
	// shared OAuth response health policy
	Health *provideradapter.OpenAIResponseHealth
}

// ClassifyOpenAIAPIKeyHealthFailure returns observed failure status/body and
// provider-health eligibility. Cancellation, credential recovery,
// same-provider retries, and request/shared failures are excluded; qualifying
// failover or image errors require 429 or server status.
func ClassifyOpenAIAPIKeyHealthFailure(err error) (int, []byte, bool) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, nil, false
	}

	if failoverErr, ok := errors.AsType[*forwardcore.UpstreamFailoverError](err); ok {
		// Dedicated recovery and replay already have their own state handling.
		// Exclude their errors and shared/request scopes before accumulating ordinary provider-health failures.
		if failoverErr.IsCredentialFailure() ||
			failoverErr.RequestScopedTransient ||
			failoverErr.RetryableOnSameProvider ||
			failoverErr.Scope == forwardcore.GatewayFailureScopeRequest ||
			failoverErr.Scope == forwardcore.GatewayFailureScopeShared {
			return failoverErr.StatusCode, failoverErr.ResponseBody, false
		}
		if failoverErr.StatusCode == http.StatusTooManyRequests || failoverErr.StatusCode >= http.StatusInternalServerError {
			return failoverErr.StatusCode, failoverErr.ResponseBody, true
		}
		return failoverErr.StatusCode, failoverErr.ResponseBody, false
	}

	if imageErr, ok := errors.AsType[*openai.OpenAIImagesUpstreamError](err); ok {
		if imageErr.StatusCode == http.StatusTooManyRequests || imageErr.StatusCode >= http.StatusInternalServerError {
			return imageErr.StatusCode, []byte(strings.TrimSpace(imageErr.Message)), true
		}
	}
	return 0, nil, false
}

// IsOpenAIRequestBodyTooLarge recognizes the stable oversized-request reason
// on a failover error.
func IsOpenAIRequestBodyTooLarge(e *forwardcore.UpstreamFailoverError) bool {
	return e != nil && e.Reason == "openai_request_body_too_large"
}

// IsOpenAICapacityShed requires request-scoped transient metadata and a
// recognized capacity payload. Nil errors or capacity text without that scope
// flag return false, retaining classification evidence as well as content.
func IsOpenAICapacityShed(e *forwardcore.UpstreamFailoverError) bool {
	return e != nil && e.RequestScopedTransient && openai.IsOpenAIRequestScopedCapacityShed("", e.ResponseBody)
}

// OpenAI429RetryDelay returns a wait bounded by Retry-After policy and the
// caller's retry deadline. Positive future Retry-After evidence supplies the
// initial delay; absent or elapsed header evidence uses the default delay.
//
// The server delay is capped before the remaining deadline clips it, because
// both limits can apply to one replay. A zero deadline skips that second cap,
// while an elapsed deadline returns zero. Callers enforce attempt eligibility
// and decide whether a zero wait still permits another attempt.
func OpenAI429RetryDelay(headers http.Header, deadline time.Time) time.Duration {
	delay := openAIOAuth429RetryDelay
	now := time.Now()
	if resetAt := openai.ParseRetryAfterResetTime(headers, now); resetAt != nil && resetAt.After(now) {
		delay = resetAt.Sub(now)
	}
	// Bound the server-supplied delay before clipping it to the remaining request
	// retry window, so long Retry-After values cannot extend caller recovery time.
	if delay > openAIOAuth429MaxRetryDelay {
		delay = openAIOAuth429MaxRetryDelay
	}
	if remaining := time.Until(deadline); !deadline.IsZero() && delay > remaining {
		delay = remaining
	}
	if delay < 0 {
		return 0
	}
	return delay
}

// ShouldFailoverUpstreamStatus recognizes authentication, payment, rate-limit,
// overload, and server status codes.
func ShouldFailoverUpstreamStatus(statusCode int) bool {
	switch statusCode {
	case 401, 402, 403, 429, 529:
		return true
	default:
		return statusCode >= 500
	}
}

// ShouldFailoverOpenAIResponse excludes cyber warnings and context-window
// rejection before provider-switch classification. Access-state errors and
// oversized request bodies permit failover, followed by ordinary status rules
// and OpenAI transient-processing evidence.
func ShouldFailoverOpenAIResponse(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	// A cyber_policy rejection stays a request rejection even when an intermediary wraps it in a 5xx status.
	if IsOpenAICyberWarningPayload(upstreamBody, upstreamMsg) {
		return false
	}
	if openai.IsOpenAIContextWindowError(upstreamMsg, upstreamBody) {
		return false
	}
	if IsOpenAIHTTPUpstreamAccessStateError(statusCode, upstreamMsg, upstreamBody) {
		return true
	}
	if IsOpenAIRequestBodyTooLargeError(statusCode, upstreamMsg, upstreamBody) {
		return true
	}
	if ShouldFailoverUpstreamStatus(statusCode) {
		return true
	}
	return openai.IsOpenAITransientProcessingError(statusCode, upstreamMsg, upstreamBody)
}

// IsOpenAIRequestBodyTooLargeError recognizes HTTP 413 when the payload does
// not identify a context-window error. That distinction lets callers switch
// targets for proxy/body limits while retaining request-specific model-context
// rejection.
func IsOpenAIRequestBodyTooLargeError(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	return statusCode == http.StatusRequestEntityTooLarge && !openai.IsOpenAIContextWindowError(upstreamMsg, upstreamBody)
}

// NewOpenAIUpstreamFailure builds a classified error retaining body bytes and
// cloned response headers. Capacity evidence initially enables same-provider
// retry, while oversized request and access-state handling can replace that
// classification.
//
// Body-size failures select provider switching with a 413 client result.
// Structured access-state failures override authentication scope and use a
// generic 502 client message; otherwise capacity sets a retryable 503
// explanation. If capacity evidence also accompanies a body-size failure,
// this final capacity branch can replace the client status/message while
// retaining provider-scope switching metadata. Callers keep the body stable
// while the loop reads it and enforce attempt and output-start restrictions.
func NewOpenAIUpstreamFailure(
	statusCode int,
	responseHeaders http.Header,
	responseBody []byte,
	upstreamMsg string,
	retryableOnSameProvider bool,
) *forwardcore.UpstreamFailoverError {
	requestScopedCapacity := openai.IsOpenAIRequestScopedCapacityShed(upstreamMsg, responseBody)
	failoverErr := &forwardcore.UpstreamFailoverError{
		StatusCode:              statusCode,
		ResponseBody:            responseBody,
		ResponseHeaders:         responseHeaders.Clone(),
		RetryableOnSameProvider: retryableOnSameProvider || requestScopedCapacity,
		RequestScopedTransient:  requestScopedCapacity,
	}
	// A proxy body-size refusal can differ between providers; select switching
	// and retain 413 for the client after the request owner exhausts that recovery.
	if IsOpenAIRequestBodyTooLargeError(statusCode, upstreamMsg, responseBody) {
		failoverErr.RetryableOnSameProvider = false
		failoverErr.RequestScopedTransient = false
		failoverErr.Scope = forwardcore.GatewayFailureScopeProvider
		failoverErr.Reason = openAIRequestBodyTooLargeReason
		failoverErr.NextProviderAction = forwardcore.NextProviderRetry
		failoverErr.ClientStatusCode = http.StatusRequestEntityTooLarge
		failoverErr.ClientMessage = forwardcore.OpenAIRequestBodyTooLargeClientMessage
	}
	// Structured access-state evidence attributes this failure to authentication
	// availability, replacing capacity replay metadata with a provider-switch decision and generic client text.
	if IsOpenAIHTTPUpstreamAccessStateError(statusCode, upstreamMsg, responseBody) {
		failoverErr.RetryableOnSameProvider = false
		failoverErr.RequestScopedTransient = false
		failoverErr.Stage = forwardcore.GatewayFailureStageProviderAuth
		failoverErr.Scope = forwardcore.GatewayFailureScopeProvider
		failoverErr.Reason = forwardcore.OpenAIUpstreamAccessStateReason
		failoverErr.NextProviderAction = forwardcore.NextProviderRetry
		failoverErr.ClientStatusCode = http.StatusBadGateway
		failoverErr.ClientMessage = openAIUpstreamAccessUnavailableClientMessage
	} else if requestScopedCapacity {
		// Capacity supplies the final client explanation when access-state handling
		// did not win, including a body-size match whose switching metadata stays set.
		failoverErr.ClientStatusCode = http.StatusServiceUnavailable
		failoverErr.ClientMessage = OpenAICapacityShedClientMessage(upstreamMsg, responseBody)
	}
	return failoverErr
}

// NewProviderFailure builds an upstream failure using the same response
// headers for classification and retained diagnostics. It delegates OAuth 429
// eligibility and deadline calculation to
// NewProviderFailureWithClassificationHeaders.
func (p OpenAIFailoverPolicy) NewProviderFailure(
	provider *ExecutionProvider,
	statusCode int,
	responseHeaders http.Header,
	responseBody []byte,
	upstreamMsg string,
	shouldDisable bool,
	retryableOnSameProvider bool,
) *forwardcore.UpstreamFailoverError {
	return p.NewProviderFailureWithClassificationHeaders(provider, statusCode, responseHeaders, responseHeaders, responseBody, upstreamMsg, shouldDisable, retryableOnSameProvider)
}

// NewProviderFailureWithClassificationHeaders separates health-classification
// headers from those retained by the failover error. Eligible OAuth 429
// responses add the shared health deadline and a bounded delay computed from
// responseHeaders; other failures keep ordinary metadata.
func (p OpenAIFailoverPolicy) NewProviderFailureWithClassificationHeaders(
	provider *ExecutionProvider,
	statusCode int,
	responseHeaders http.Header,
	classificationHeaders http.Header,
	responseBody []byte,
	upstreamMsg string,
	shouldDisable bool,
	retryableOnSameProvider bool,
) *forwardcore.UpstreamFailoverError {
	oauth429Retry := p.Health.RetryOAuth429(ExecutionRecord(provider), statusCode, shouldDisable, classificationHeaders, responseBody)
	failoverErr := NewOpenAIUpstreamFailure(
		statusCode,
		responseHeaders,
		responseBody,
		upstreamMsg,
		retryableOnSameProvider || oauth429Retry,
	)
	if oauth429Retry {
		failoverErr.SameProviderRetryDeadline = p.Health.RetryDeadline(ExecutionRecord(provider))
		failoverErr.SameProviderRetryDelay = OpenAI429RetryDelay(responseHeaders, failoverErr.SameProviderRetryDeadline)
	}
	return failoverErr
}

// IsOpenAIUpstreamAccessStateError delegates structured access-state detection
// using body alone. The compatibility string argument is ignored, so caller
// message text cannot independently establish this provider-auth
// classification.
func IsOpenAIUpstreamAccessStateError(_ string, body []byte) bool {
	return openai.IsOpenAIUpstreamAccessStateError("", body)
}

// IsOpenAIHTTPUpstreamAccessStateError delegates structured HTTP access-state
// detection using body alone. Status and message arguments are currently
// ignored, retaining the body evidence as the classifier input.
func IsOpenAIHTTPUpstreamAccessStateError(_ int, _ string, body []byte) bool {
	return openai.IsOpenAIHTTPUpstreamAccessStateError(0, "", body)
}

// OpenAICapacityShedClientMessage selects the first recognized sanitized
// capacity explanation from supplied text and nested error messages. Unmatched
// candidates use a generic overload message, keeping unrelated upstream text
// out of this client response.
func OpenAICapacityShedClientMessage(upstreamMsg string, body []byte) string {
	for _, candidate := range []string{
		upstreamMsg,
		gjson.GetBytes(body, "error.message").String(),
		gjson.GetBytes(body, "response.error.message").String(),
		gjson.GetBytes(body, "message").String(),
	} {
		candidate = logredact.SanitizeUpstreamQueries(strings.TrimSpace(candidate))
		if candidate != "" && openai.IsOpenAICapacityShedMessage(candidate) {
			return candidate
		}
	}
	return "Upstream service is temporarily overloaded, please retry later"
}

// OpenAISemantic429Headers returns borrowed headers for exact Spark models on
// OAuth-like targets. Other targets or models return nil, so in-stream health
// classification receives these headers only for that qualified path.
func OpenAISemantic429Headers(target *ExecutionProvider, model string, headers http.Header) http.Header {
	if IsCodexSparkModel(model) && target != nil && target.View().IsOpenAIOAuthLike() {
		return headers
	}
	return nil
}

// OpenAIStreamFailureRetryable permits recognized capacity events or eligible
// pool-mode semantic errors. Nil targets fail; capacity can retry outside pool
// mode, while other failures require configured pool statuses or
// transient-processing evidence.
func OpenAIStreamFailureRetryable(provider *ExecutionProvider, payload []byte, message string) bool {
	if provider == nil {
		return false
	}
	// Recognized capacity shedding has request-scoped recovery metadata and can
	// use the caller's same-provider budget even when pool mode is not enabled.
	if openai.IsOpenAIUpstreamCapacityShedEvent(payload) {
		return true
	}
	if !provider.View().IsPoolMode() {
		return false
	}
	semanticStatus := openai.OpenAIStreamFailedEventSemanticStatus(payload, message)
	return provider.View().IsPoolModeRetryableStatus(semanticStatus) ||
		openai.IsOpenAITransientProcessingError(http.StatusBadRequest, message, payload)
}
