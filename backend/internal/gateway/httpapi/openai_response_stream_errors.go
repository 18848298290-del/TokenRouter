package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// writeSanitizedOpenAIPassthroughError writes a sanitized error with Compact SSE support.
func writeSanitizedOpenAIPassthroughError(c *gin.Context, upstreamStatus int, upstreamHeaders http.Header) {
	WriteSanitizedForwardPassthroughError(c, upstreamStatus, upstreamHeaders, func(c *gin.Context, status int, body []byte) bool {
		return WriteOpenAICompactSSEBridge(c, status, body, MarkOpsStreamError)
	})
}

// writeOpenAIPassthroughErrorEnvelope writes an error envelope with Compact SSE support.
func writeOpenAIPassthroughErrorEnvelope(c *gin.Context, downstreamStatus int, upstreamHeaders http.Header, message string) {
	WriteForwardPassthroughErrorEnvelope(c, downstreamStatus, upstreamHeaders, message, func(c *gin.Context, status int, body []byte) bool {
		return WriteOpenAICompactSSEBridge(c, status, body, MarkOpsStreamError)
	})
}

func (p *OpenAIResponseOutput) PassthroughFailoverError(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	requestBody []byte,
	responseBody []byte,
) error {
	body := p.redact(ctx, provider, responseBody)

	upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(body))
	upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
	upstreamDetail := ""
	if p.Options.Configured && p.Options.LogUpstreamErrorBody {
		maxBytes := p.Options.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = logredact.TruncateUTF8(string(body), maxBytes)
	}
	SetOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	LogOpenAIInstructionsRequiredDebug(ctx, c, provider, resp.StatusCode, upstreamMsg, requestBody, body)
	reqModel, _, _ := requeststate.OpenAIRequestMetaFromBody(requestBody)
	canonicalModel := gatewayprovider.ExecutionModelPolicy(provider).CanonicalSchedulingModel(reqModel)
	decision := gatewayprovider.ApplyOpenAIResponseHealth(ctx, p.Health, provider, resp.StatusCode, resp.Header, body, false, canonicalModel)
	if decision.ShouldReturnGenericError() {
		MarkResponseCommitted(c)
		writeOpenAIPassthroughErrorEnvelope(c, http.StatusInternalServerError, resp.Header, "Upstream gateway error")
		return fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
	}
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform:             provider.Record.Platform,
		ProviderID:           provider.Record.ID,
		ProviderName:         provider.Record.Name,
		UpstreamStatusCode:   resp.StatusCode,
		UpstreamRequestID:    resp.Header.Get("x-request-id"),
		Passthrough:          true,
		Kind:                 "failover",
		Message:              upstreamMsg,
		Detail:               upstreamDetail,
		UpstreamResponseBody: upstreamDetail,
	})
	shouldDisable := decision.StopScheduling
	return (gatewayprovider.OpenAIFailoverPolicy{Health: p.Health}).NewProviderFailure(
		provider,
		resp.StatusCode,
		resp.Header,
		body,
		upstreamMsg,
		shouldDisable,
		!shouldDisable && provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(resp.StatusCode),
	)
}

func (p *OpenAIResponseOutput) PassthroughError(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	requestBody []byte,
	responseBody []byte,
) error {
	body := p.redact(ctx, provider, responseBody)

	// Record cyber_policy from the redacted upstream body for subsequent risk handling and email.
	// The client error is rebuilt below. Cyber policy rejection is request-scoped,
	// so skip provider health updates that could apply a temporary scheduling ban.
	cyberHit, cyberCode, cyberMsg := openai.DetectOpenAICyberPolicy(body)
	if cyberHit {
		MarkOpsCyberPolicy(c, moderationflow.Mark{
			Code:           cyberCode,
			Message:        cyberMsg,
			Body:           logredact.TruncateUTF8(string(body), 4096),
			UpstreamStatus: resp.StatusCode,
		})
	}

	upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(body))
	upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
	upstreamDetail := ""
	if p.Options.Configured && p.Options.LogUpstreamErrorBody {
		maxBytes := p.Options.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = logredact.TruncateUTF8(string(body), maxBytes)
	}
	SetOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	LogOpenAIInstructionsRequiredDebug(ctx, c, provider, resp.StatusCode, upstreamMsg, requestBody, body)
	clientInvalidRequest := openai.IsOpenAIClientInvalidRequestError(resp.StatusCode, upstreamMsg, body)
	requestScopedError := cyberHit || clientInvalidRequest || openai.IsOpenAIContextWindowError(upstreamMsg, body) ||
		gatewayprovider.IsOpenAIRequestBodyTooLargeError(resp.StatusCode, upstreamMsg, body)
	// Update provider state so sticky routing stops reusing a rate-limited provider.
	// Request-scoped errors bypass provider cooldown and pool retries.
	if !requestScopedError {
		reqModel, _, _ := requeststate.OpenAIRequestMetaFromBody(requestBody)
		canonicalModel := gatewayprovider.ExecutionModelPolicy(provider).CanonicalSchedulingModel(reqModel)
		decision := gatewayprovider.ApplyOpenAIResponseHealth(ctx, p.Health, provider, resp.StatusCode, resp.Header, body, false, canonicalModel)
		if decision.ShouldReturnGenericError() {
			MarkResponseCommitted(c)
			writeOpenAIPassthroughErrorEnvelope(c, http.StatusInternalServerError, resp.Header, "Upstream gateway error")
			return fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		if decision.ShouldFailoverWithDefaults(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, false, false) {
			return gatewayprovider.NewOpenAIUpstreamFailure(
				resp.StatusCode,
				resp.Header,
				body,
				upstreamMsg,
				decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
			)
		}
	}
	MarkResponseCommitted(c)
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform:             provider.Record.Platform,
		ProviderID:           provider.Record.ID,
		ProviderName:         provider.Record.Name,
		UpstreamStatusCode:   resp.StatusCode,
		UpstreamRequestID:    resp.Header.Get("x-request-id"),
		Passthrough:          true,
		Kind:                 "http_error",
		Message:              upstreamMsg,
		Detail:               upstreamDetail,
		UpstreamResponseBody: upstreamDetail,
	})
	if clientInvalidRequest {
		// Parameter errors return safe headers and the full redacted error object.
		WriteForwardPassthroughErrorHeaders(c.Writer.Header(), resp.Header)
		c.Data(http.StatusBadRequest, "application/json; charset=utf-8", body)
		return fmt.Errorf("upstream invalid request: %d message=%s", resp.StatusCode, upstreamMsg)
	}
	// Return the sanitized context-window message so clients can trigger recovery such as automatic compaction.
	if openai.IsOpenAIContextWindowError(upstreamMsg, body) && upstreamMsg != "" {
		writeOpenAIPassthroughErrorEnvelope(c, resp.StatusCode, resp.Header, upstreamMsg)
	} else {
		writeSanitizedOpenAIPassthroughError(c, resp.StatusCode, resp.Header)
	}

	return fmt.Errorf("upstream error: %d (client response sanitized)", resp.StatusCode)
}

func LogOpenAICapacityFailoverSuppressed(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	path string,
	upstreamRequestID string,
	eventType string,
) {
	fields := []zap.Field{
		zap.String("path", path),
		zap.String("event_type", strings.TrimSpace(eventType)),
		zap.String("upstream_request_id", strings.TrimSpace(upstreamRequestID)),
	}
	if provider != nil {
		fields = append(fields,
			zap.Int64("provider_id", provider.Record.ID),
			zap.String("platform", provider.Record.Platform),
		)
	}
	logging.FromContext(ctx).Warn("gateway.failover_suppressed_after_semantic_output", fields...)
}

func openAIStreamFailedEventPassthroughBody(payload []byte, failedMessage string) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	if gjson.GetBytes(payload, "error").Exists() {
		return payload
	}
	responseError := gjson.GetBytes(payload, "response.error")
	if !responseError.Exists() {
		if strings.TrimSpace(failedMessage) == "" {
			return payload
		}
		body, err := wirejson.Marshal(gin.H{
			"error": gin.H{
				"message": failedMessage,
			},
		})
		if err != nil {
			return payload
		}
		return body
	}

	errorPayload := gin.H{}
	if errType := strings.TrimSpace(gjson.Get(responseError.Raw, "type").String()); errType != "" {
		errorPayload["type"] = errType
	}
	if code := strings.TrimSpace(gjson.Get(responseError.Raw, "code").String()); code != "" {
		errorPayload["code"] = code
	}
	if param := strings.TrimSpace(gjson.Get(responseError.Raw, "param").String()); param != "" {
		errorPayload["param"] = param
	}
	message := strings.TrimSpace(gjson.Get(responseError.Raw, "message").String())
	if message == "" {
		message = strings.TrimSpace(failedMessage)
	}
	if message != "" {
		errorPayload["message"] = message
	}
	if len(errorPayload) == 0 {
		return payload
	}
	body, err := wirejson.Marshal(gin.H{"error": errorPayload})
	if err != nil {
		return payload
	}
	return body
}

// ApplyOpenAIStreamFailedErrorRule applies passthrough rules to response.failed.
// The normalized body supplies matching text and messages; event contents determine the status.
// Callers supply provider.Platform so OpenAI and Grok rules match their own platforms.
func ApplyOpenAIStreamFailedErrorRule(
	c *gin.Context,
	platform string,
	payload []byte,
	failedMessage string,
) (status int, errType string, errMsg string, matched bool) {
	ruleBody := openAIStreamFailedEventPassthroughBody(payload, failedMessage)
	upstreamStatus := openai.OpenAIStreamFailedEventSemanticStatus(payload, failedMessage)
	return ApplyErrorPassthroughRule(
		c,
		platform,
		upstreamStatus,
		ruleBody,
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	)
}

func (p *OpenAIResponseOutput) TerminalProviderEffects(
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	payload []byte,
	message string,
	headers http.Header,
	canonicalModel ...string,
) (int, bool) {
	statusCode := openai.OpenAIStreamFailureStatus(payload, message)
	switch statusCode {
	case http.StatusForbidden:
		if !openai.OpenAIStream403ProviderFailure(payload, message) {
			return statusCode, false
		}
		fallthrough
	case http.StatusUnauthorized, http.StatusTooManyRequests, 529:
		ctx := context.Background()
		if c != nil && c.Request != nil {
			ctx = c.Request.Context()
		}
		model := requeststate.FirstNonEmpty(canonicalModel...)
		if model == "" {
			model = requeststate.FirstNonEmpty(gjson.GetBytes(payload, "model").String(), gjson.GetBytes(payload, "response.model").String())
		}
		providerHeaders := headers
		if statusCode == http.StatusTooManyRequests {
			// A streaming 429 for ordinary models must not inherit the HTTP 200 global quota snapshot.
			// OAuth and Setup Token Spark quota failures need the explicit reset window.
			providerHeaders = gatewayprovider.OpenAISemantic429Headers(provider, model, headers)
		}
		return statusCode, gatewayprovider.ApplyOpenAIResponseHealth(ctx, p.Health, provider, statusCode, providerHeaders, payload, false, model).StopScheduling
	default:
		// response.failed can carry custom statuses such as 422. Apply configured or pool retry policy, while
		// request validation leaves health unchanged.
		customMatched := provider != nil && provider.View().IsCustomErrorCodesEnabled() && provider.View().ShouldHandleErrorCode(statusCode)
		poolRetryable := provider != nil && provider.View().IsPoolMode() && provider.View().IsPoolModeRetryableStatus(statusCode)
		if customMatched || poolRetryable {
			ctx := context.Background()
			if c != nil && c.Request != nil {
				ctx = c.Request.Context()
			}
			return statusCode, gatewayprovider.ApplyOpenAIResponseHealth(ctx, p.Health, provider, statusCode, headers, payload, false, requeststate.FirstNonEmpty(canonicalModel...)).StopScheduling
		}
		return statusCode, false
	}
}

// ApplyStreamFailurePolicy maps response.failed inside HTTP 200 streams
// to provider policy using a shared event-status interpretation.
func (p *OpenAIResponseOutput) ApplyStreamFailurePolicy(
	ctx context.Context,
	provider *gatewayprovider.ExecutionProvider,
	model string,
	headers http.Header,
	payload []byte,
	message string,
) (int, providercore.UpstreamErrorDecision) {
	status := openai.OpenAIStreamFailedEventSemanticStatus(payload, message)
	if status < http.StatusBadRequest {
		status = http.StatusBadGateway
	}
	return status, gatewayprovider.ApplyOpenAIResponseHealth(ctx, p.Health, provider, status, headers, payload, true, model)
}

func (p *OpenAIResponseOutput) RecordStreamError(
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	passthrough bool,
	upstreamRequestID string,
	kind string,
	payload []byte,
	message string,
) string {
	message = logredact.SanitizeUpstreamQueries(strings.TrimSpace(message))
	if message == "" {
		message = "OpenAI upstream response failed"
	}
	statusCode := openai.OpenAIStreamFailureStatus(payload, message)
	detail := ""
	if len(payload) > 0 && p != nil && p.Options.Configured && p.Options.LogUpstreamErrorBody {
		maxBytes := p.Options.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		detail = logredact.TruncateUTF8(string(payload), maxBytes)
	}
	if c != nil {
		SetOpsUpstreamError(c, statusCode, message, detail)
		event := ops.OpsUpstreamErrorEvent{
			Platform:           capability.PlatformOpenAI,
			UpstreamStatusCode: statusCode,
			UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
			Passthrough:        passthrough,
			Kind:               kind,
			Message:            message,
			Detail:             detail,
		}
		if provider != nil {
			event.Platform = provider.Record.Platform
			event.ProviderID = provider.Record.ID
			event.ProviderName = provider.Record.Name
		}
		AppendOpsUpstreamError(c, event)
	}
	return message
}

func (p *OpenAIResponseOutput) NewStreamFailure(
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	passthrough bool,
	upstreamRequestID string,
	payload []byte,
	message string,
	responseHeaders ...http.Header,
) *forwardcore.UpstreamFailoverError {
	return p.NewStreamFailureWithModel(c, provider, passthrough, upstreamRequestID, payload, message, "", responseHeaders...)
}

func (p *OpenAIResponseOutput) NewStreamFailureWithModel(
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	passthrough bool,
	upstreamRequestID string,
	payload []byte,
	message string,
	canonicalModel string,
	responseHeaders ...http.Header,
) *forwardcore.UpstreamFailoverError {
	var headers http.Header
	if len(responseHeaders) > 0 && responseHeaders[0] != nil {
		headers = responseHeaders[0]
	}
	return p.NewStreamPolicyFailureWithModel(
		c, provider, passthrough, upstreamRequestID, headers, http.StatusBadGateway, payload, message, false, canonicalModel,
	)
}

// NewStreamPolicyFailure builds a stream failover error after applying provider policy.
// The handler receives the client envelope, event status, and upstream headers.
func (p *OpenAIResponseOutput) NewStreamPolicyFailure(
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	passthrough bool,
	upstreamRequestID string,
	responseHeaders http.Header,
	statusCode int,
	payload []byte,
	message string,
	_ bool,
) *forwardcore.UpstreamFailoverError {
	return p.NewStreamPolicyFailureWithModel(c, provider, passthrough, upstreamRequestID, responseHeaders, statusCode, payload, message, false)
}

func (p *OpenAIResponseOutput) NewStreamPolicyFailureWithModel(
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	passthrough bool,
	upstreamRequestID string,
	responseHeaders http.Header,
	statusCode int,
	payload []byte,
	message string,
	_ bool,
	canonicalModel ...string,
) *forwardcore.UpstreamFailoverError {
	message = logredact.SanitizeUpstreamQueries(strings.TrimSpace(message))
	if message == "" {
		message = "OpenAI stream disconnected before completion"
	}
	var headers http.Header
	if len(responseHeaders) > 0 {
		headers = responseHeaders.Clone()
	}
	observedStatus, shouldDisable, sideEffectsApplied := consumeOpenAIResponseFailureEffects(c)
	if sideEffectsApplied {
		statusCode = observedStatus
	}
	if !sideEffectsApplied {
		statusCode, shouldDisable = p.TerminalProviderEffects(c, provider, payload, message, headers, canonicalModel...)
	}
	if statusCode < http.StatusBadRequest {
		statusCode = openai.OpenAIStreamFailureStatus(payload, message)
	}
	// Use the event status to update health for failed events inside HTTP 200 streams.
	// The failover engine selects recovery from StatusCode and RetryableOnSameProvider.
	message = p.RecordStreamError(c, provider, passthrough, upstreamRequestID, "failover", payload, message)
	errType := "upstream_error"
	if statusCode == http.StatusTooManyRequests {
		errType = "rate_limit_error"
	}
	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	retryable := gatewayprovider.OpenAIStreamFailureRetryable(provider, payload, message)
	// HTTP 200 headers describe stream establishment; quota classification uses the terminal event status.
	// Retain those headers for Retry-After and request ID lookup.
	classificationHeaders := headers
	if statusCode == http.StatusTooManyRequests {
		classificationHeaders = nil
	}
	failoverErr := (gatewayprovider.OpenAIFailoverPolicy{Health: p.Health}).NewProviderFailureWithClassificationHeaders(provider, statusCode, headers, classificationHeaders, payload, message, shouldDisable, retryable)
	if failoverErr.IsCredentialFailure() || failoverErr.RequestScopedTransient {
		return failoverErr
	}
	// Unclassified stream failures use a generic envelope; credential and capacity failures keep the upstream
	// body.
	failoverErr.ResponseBody = body
	return failoverErr
}

// nonStreamingTerminalFailure applies streaming classification to buffered SSE terminal failures.
// Bare error events require a transient signal for failover; response.failed uses full classification.
// The entry still decides retry eligibility from commitment state and non-heartbeat output.
// A missing provider or committed response stays on the protocol-error path.
func (p *OpenAIResponseOutput) nonStreamingTerminalFailure(
	c *gin.Context,
	resp *http.Response,
	provider *gatewayprovider.ExecutionProvider,
	passthrough bool,
	terminalType string,
	payload []byte,
	message string,
	canonicalModel ...string,
) *forwardcore.UpstreamFailoverError {
	if provider == nil || IsResponseCommitted(c) {
		return nil
	}
	shouldFailover := openai.OpenAIStreamFailedEventShouldFailover(payload, message)
	if terminalType == "error" {
		shouldFailover = openai.OpenAIStreamErrorEventShouldFailover(payload, message)
	}
	if !shouldFailover {
		return nil
	}
	var headers http.Header
	upstreamRequestID := ""
	if resp != nil {
		headers = resp.Header
		upstreamRequestID = strings.TrimSpace(resp.Header.Get("x-request-id"))
	}
	return p.NewStreamFailureWithModel(c, provider, passthrough, upstreamRequestID, payload, message, requeststate.FirstNonEmpty(canonicalModel...), headers)
}
