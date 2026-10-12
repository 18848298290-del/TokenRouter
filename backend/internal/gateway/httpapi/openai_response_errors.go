package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func LogOpenAIInstructionsRequiredDebug(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	upstreamStatusCode int,
	upstreamMsg string,
	requestBody []byte,
	upstreamBody []byte,
) {
	msg := strings.TrimSpace(upstreamMsg)
	if !isOpenAIInstructionsRequiredError(upstreamStatusCode, msg, upstreamBody) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	providerID := int64(0)
	providerName := ""
	if provider != nil {
		providerID = provider.Record.ID
		providerName = strings.TrimSpace(provider.Record.Name)
	}

	userAgent := ""
	originator := ""
	if c != nil {
		userAgent = strings.TrimSpace(c.GetHeader("User-Agent"))
		originator = strings.TrimSpace(c.GetHeader("originator"))
	}

	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.Int64("provider_id", providerID),
		zap.String("provider_name", providerName),
		zap.Int("upstream_status_code", upstreamStatusCode),
		zap.String("upstream_error_message", msg),
		zap.String("request_user_agent", userAgent),
		zap.Bool("codex_official_client_match", openai.IsCodexOfficialClientByHeaders(userAgent, originator)),
	}
	fields = AppendCodexRejectedRequestFields(fields, c, requestBody)

	logging.FromContext(ctx).With(fields...).Warn("OpenAI 上游返回 Instructions are required，已记录请求详情用于排查")
}

func isOpenAIInstructionsRequiredError(upstreamStatusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if upstreamStatusCode != http.StatusBadRequest {
		return false
	}

	hasInstructionRequired := func(text string) bool {
		lower := strings.ToLower(strings.TrimSpace(text))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "instructions are required") {
			return true
		}
		if strings.Contains(lower, "required parameter: 'instructions'") {
			return true
		}
		if strings.Contains(lower, "required parameter: instructions") {
			return true
		}
		if strings.Contains(lower, "missing required parameter") && strings.Contains(lower, "instructions") {
			return true
		}
		return strings.Contains(lower, "instruction") && strings.Contains(lower, "required")
	}

	if hasInstructionRequired(upstreamMsg) {
		return true
	}
	if len(upstreamBody) == 0 {
		return false
	}

	errMsg := gjson.GetBytes(upstreamBody, "error.message").String()
	errMsgLower := strings.ToLower(strings.TrimSpace(errMsg))
	errCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.code").String()))
	errParam := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.param").String()))
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.type").String()))

	if errParam == "instructions" {
		return true
	}
	if hasInstructionRequired(errMsg) {
		return true
	}
	if strings.Contains(errCode, "missing_required_parameter") && strings.Contains(errMsgLower, "instructions") {
		return true
	}
	if strings.Contains(errType, "invalid_request") && strings.Contains(errMsgLower, "instructions") && strings.Contains(errMsgLower, "required") {
		return true
	}

	return false
}

func (p *OpenAIResponseOutput) ReadErrorBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	limit := p.errorBodyReadLimit()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return body
}

func (p *OpenAIResponseOutput) ResponseError(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	requestBody []byte,
	requestedModel ...string,
) (*forwardcore.OpenAIResult, error) {
	body := p.ReadErrorBody(resp)
	body = p.redact(ctx, provider, body)

	if hit, code, cyberMsg := openai.DetectOpenAICyberPolicy(body); hit {
		MarkOpsCyberPolicy(c, moderationflow.Mark{
			Code:           code,
			Message:        cyberMsg,
			Body:           logredact.TruncateUTF8(string(body), 4096),
			UpstreamStatus: resp.StatusCode,
		})
		SetOpsUpstreamError(c, resp.StatusCode, cyberMsg, logredact.TruncateUTF8(string(body), 2048))
		WriteOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, p.Headers)
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		MarkResponseCommitted(c)
		c.Data(resp.StatusCode, contentType, body)
		if cyberMsg == "" {
			return nil, fmt.Errorf("openai cyber_policy: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("openai cyber_policy: %s", cyberMsg)
	}
	if provider != nil && provider.Record.Platform == capability.PlatformGrok && grok.IsGrokContentPolicyRejection(resp.StatusCode, body) {
		clientMsg := gatewayprovider.GrokContentPolicyClientMessage(body)
		SetOpsUpstreamError(c, resp.StatusCode, clientMsg, logredact.TruncateUTF8(string(body), 2048))
		WriteOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, p.Headers)
		MarkResponseCommitted(c)
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": clientMsg,
			},
		})
		return nil, fmt.Errorf("grok content policy rejection: %s", clientMsg)
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

	if p.Options.Configured && p.Options.LogUpstreamErrorBody {
		logging.LegacyPrintf("service.openai_gateway",
			"OpenAI upstream error %d (provider=%d platform=%s type=%s): %s",
			resp.StatusCode,
			provider.Record.ID,
			provider.Record.Platform,
			provider.Record.Type,
			logredact.TruncateLine(body, p.Options.LogUpstreamErrorBodyMaxBytes),
		)
	}

	if gatewayprovider.IsOpenAICyberWarningPayload(body, upstreamMsg) {
		errMsg := gatewayprovider.ExtractOpenAICyberWarningMessage(body, upstreamMsg)
		AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
			Platform:           provider.Record.Platform,
			ProviderID:         provider.Record.ID,
			ProviderName:       provider.Record.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            errMsg,
			Detail:             upstreamDetail,
		})
		c.JSON(resp.StatusCode, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": errMsg,
			},
		})
		return nil, gatewayprovider.WrapOpenAIUpstreamWarningIfCyber(resp.StatusCode, body, errMsg, fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, errMsg))
	}

	if gatewayprovider.IsOpenAIRequestBodyTooLargeError(resp.StatusCode, upstreamMsg, body) {
		AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
			Platform:           provider.Record.Platform,
			ProviderID:         provider.Record.ID,
			ProviderName:       provider.Record.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		WriteOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, p.Headers)
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		MarkResponseCommitted(c)
		c.Data(resp.StatusCode, contentType, body)
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream request body too large: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream request body too large: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	if openai.IsOpenAIClientInvalidRequestError(resp.StatusCode, upstreamMsg, body) {
		// Parameter errors leave provider health unchanged; retain Ops context and return the full error structure.
		AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
			Platform:           provider.Record.Platform,
			ProviderID:         provider.Record.ID,
			ProviderName:       provider.Record.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		WriteOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, p.Headers)
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		MarkResponseCommitted(c)
		c.Data(http.StatusBadRequest, contentType, body)
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream invalid request: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream invalid request: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	// Apply provider policy after excluding request errors, including statuses that do not trigger failover.
	var reqModel string
	if len(requestedModel) > 0 {
		reqModel = strings.TrimSpace(requestedModel[0])
	}
	if reqModel == "" {
		reqModel, _, _ = requeststate.OpenAIRequestMetaFromBody(requestBody)
		reqModel = gatewayprovider.ExecutionModelPolicy(provider).CanonicalSchedulingModel(reqModel)
	}
	var decision providercore.UpstreamErrorDecision
	if provider != nil && provider.Record.Platform == capability.PlatformGrok {
		decision = gatewayprovider.ApplyGrokExecutionHealth(ctx, p.GrokHealth, provider, resp.StatusCode, resp.Header, body, "", reqModel)
	} else {
		decision = gatewayprovider.ApplyOpenAIResponseHealth(ctx, p.Health, provider, resp.StatusCode, resp.Header, body, false, reqModel)
	}
	if decision.ShouldReturnGenericError() {
		AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
			Platform:           provider.Record.Platform,
			ProviderID:         provider.Record.ID,
			ProviderName:       provider.Record.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		MarkResponseCommitted(c)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"type":    "upstream_error",
				"message": "Upstream gateway error",
			},
		})
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (not in custom error codes) message=%s", resp.StatusCode, upstreamMsg)
	}

	kind := "http_error"
	defaultFailover := gatewayprovider.ShouldFailoverOpenAIResponse(resp.StatusCode, upstreamMsg, body)
	if provider != nil && provider.Record.Platform == capability.PlatformGrok {
		defaultFailover = gatewayprovider.ShouldFailoverGrokResponse(resp.StatusCode, body)
	}
	if decision.ShouldFailoverWithDefaults(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, decision.StopScheduling, defaultFailover) {
		kind = "failover"
	}
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform:           provider.Record.Platform,
		ProviderID:         provider.Record.ID,
		ProviderName:       provider.Record.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               kind,
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	if kind == "failover" {
		return nil, &forwardcore.UpstreamFailoverError{
			StatusCode:              resp.StatusCode,
			ResponseBody:            body,
			RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
		}
	}

	// After provider policy, passthrough rules select the final client response.
	if status, errType, errMsg, matched := ApplyErrorPassthroughRule(
		c,
		provider.Record.Platform,
		resp.StatusCode,
		body,
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	); matched {
		MarkResponseCommitted(c)
		c.JSON(status, gin.H{
			"error": gin.H{
				"type":    errType,
				"message": errMsg,
			},
		})
		if upstreamMsg == "" {
			upstreamMsg = errMsg
		}
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (passthrough rule matched)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (passthrough rule matched) message=%s", resp.StatusCode, upstreamMsg)
	}

	// A 400 is deterministic only when the classifier rules out failover.
	// Pool retry statuses, server_is_overloaded, and transient processing failures follow retry or 502 handling.
	if provider != nil && provider.Record.Platform == capability.PlatformOpenAI &&
		IsOpenAIDeterministicClientError(resp.StatusCode, defaultFailover) {
		MarkResponseCommitted(c)
		WriteOpenAIUpstreamClientError(c, resp.StatusCode, body, upstreamMsg)
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
	}
	MarkResponseCommitted(c)

	// Map the upstream status to a client error.
	var errType, errMsg string
	var statusCode int

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream authentication failed, please contact administrator"
	case http.StatusPaymentRequired:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream payment required: insufficient balance or billing issue"
	case http.StatusForbidden:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream access forbidden, please contact administrator"
	case http.StatusTooManyRequests:
		statusCode = http.StatusTooManyRequests
		errType = "rate_limit_error"
		errMsg = "Upstream rate limit exceeded, please retry later"
	default:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream request failed"
	}
	if openai.IsOpenAIContextWindowError(upstreamMsg, body) && upstreamMsg != "" {
		errMsg = upstreamMsg
	}

	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": errMsg,
		},
	})

	if upstreamMsg == "" {
		return nil, fmt.Errorf("upstream error: %d", resp.StatusCode)
	}
	return nil, fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
}

// CompatError handles non-failover errors for Chat Completions and Anthropic Messages.
func (p *OpenAIResponseOutput) CompatError(
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	writeError func(*gin.Context, int, string, string),
	writeErrorBody func(*gin.Context, int, []byte),
	requestedModel ...string,
) (*forwardcore.OpenAIResult, error) {
	body := p.ReadErrorBody(resp)
	body = p.redact(context.Background(), provider, body)

	if hit, code, cyberMsg := openai.DetectOpenAICyberPolicy(body); hit {
		MarkOpsCyberPolicy(c, moderationflow.Mark{
			Code:           code,
			Message:        cyberMsg,
			Body:           logredact.TruncateUTF8(string(body), 4096),
			UpstreamStatus: resp.StatusCode,
		})
		SetOpsUpstreamError(c, resp.StatusCode, cyberMsg, logredact.TruncateUTF8(string(body), 2048))
		clientMsg := cyberMsg
		if clientMsg == "" {
			clientMsg = "Request blocked by upstream cyber-security policy"
		}
		MarkResponseCommitted(c)
		writeError(c, resp.StatusCode, "invalid_request_error", clientMsg)
		if cyberMsg == "" {
			return nil, fmt.Errorf("openai cyber_policy: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("openai cyber_policy: %s", cyberMsg)
	}
	if provider != nil && provider.Record.Platform == capability.PlatformGrok && grok.IsGrokContentPolicyRejection(resp.StatusCode, body) {
		clientMsg := gatewayprovider.GrokContentPolicyClientMessage(body)
		SetOpsUpstreamError(c, resp.StatusCode, clientMsg, logredact.TruncateUTF8(string(body), 2048))
		MarkResponseCommitted(c)
		writeError(c, http.StatusForbidden, "invalid_request_error", clientMsg)
		return nil, fmt.Errorf("grok content policy rejection: %s", clientMsg)
	}

	upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(body))
	if upstreamMsg == "" {
		upstreamMsg = fmt.Sprintf("Upstream error: %d", resp.StatusCode)
	}
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

	if openai.IsOpenAIClientInvalidRequestError(resp.StatusCode, upstreamMsg, body) {
		// Keep structured upstream error details such as code and param in compatible protocols.
		AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
			Platform:           provider.Record.Platform,
			ProviderID:         provider.Record.ID,
			ProviderName:       provider.Record.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		MarkResponseCommitted(c)
		writeErrorBody(c, http.StatusBadRequest, body)
		return nil, fmt.Errorf("upstream invalid request: %d message=%s", resp.StatusCode, upstreamMsg)
	}
	if gatewayprovider.IsOpenAIRequestBodyTooLargeError(resp.StatusCode, upstreamMsg, body) {
		MarkResponseCommitted(c)
		writeErrorBody(c, resp.StatusCode, body)
		return nil, fmt.Errorf("upstream request body too large: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	var modelForCooldown string
	if len(requestedModel) > 0 {
		modelForCooldown = requestedModel[0]
	}
	var decision providercore.UpstreamErrorDecision
	if provider.Record.Platform == capability.PlatformGrok {
		decision = gatewayprovider.ApplyGrokExecutionHealth(c.Request.Context(), p.GrokHealth, provider, resp.StatusCode, resp.Header, body, "", modelForCooldown)
	} else {
		decision = gatewayprovider.ApplyOpenAIResponseHealth(c.Request.Context(), p.Health, provider, resp.StatusCode, resp.Header, body, false, modelForCooldown)
	}
	if decision.ShouldReturnGenericError() {
		AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
			Platform:           provider.Record.Platform,
			ProviderID:         provider.Record.ID,
			ProviderName:       provider.Record.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		MarkResponseCommitted(c)
		writeError(c, http.StatusInternalServerError, "api_error", "Upstream gateway error")
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (not in custom error codes) message=%s", resp.StatusCode, upstreamMsg)
	}

	kind := "http_error"
	defaultFailover := gatewayprovider.ShouldFailoverOpenAIResponse(resp.StatusCode, upstreamMsg, body)
	if provider.Record.Platform == capability.PlatformGrok {
		defaultFailover = gatewayprovider.ShouldFailoverGrokResponse(resp.StatusCode, body)
	}
	if decision.ShouldFailoverWithDefaults(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode, decision.StopScheduling, defaultFailover) {
		kind = "failover"
	}
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform:           provider.Record.Platform,
		ProviderID:         provider.Record.ID,
		ProviderName:       provider.Record.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               kind,
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	if kind == "failover" {
		return nil, &forwardcore.UpstreamFailoverError{
			StatusCode:              resp.StatusCode,
			ResponseBody:            body,
			RetryableOnSameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), resp.StatusCode),
		}
	}

	// After provider policy, passthrough rules select the response format.
	if status, errType, errMsg, matched := ApplyErrorPassthroughRule(
		c, provider.Record.Platform, resp.StatusCode, body,
		http.StatusBadGateway, "api_error", "Upstream request failed",
	); matched {
		MarkResponseCommitted(c)
		writeError(c, status, errType, errMsg)
		if upstreamMsg == "" {
			upstreamMsg = errMsg
		}
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (passthrough rule matched)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (passthrough rule matched) message=%s", resp.StatusCode, upstreamMsg)
	}
	MarkResponseCommitted(c)

	// Map the upstream status to the compatible protocol error type.
	errType := "api_error"
	switch {
	case resp.StatusCode == http.StatusBadRequest:
		errType = "invalid_request_error"
	case resp.StatusCode == http.StatusNotFound:
		errType = "not_found_error"
	case resp.StatusCode == http.StatusTooManyRequests:
		errType = "rate_limit_error"
	case resp.StatusCode >= 500:
		errType = "api_error"
	}

	writeError(c, resp.StatusCode, errType, upstreamMsg)
	return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
}

func (p *OpenAIResponseOutput) errorBodyReadLimit() int64 {
	limit := int64(512 << 10)
	if p != nil && p.Options.LogUpstreamErrorBody && p.Options.LogUpstreamErrorBodyMaxBytes > int(limit) {
		limit = int64(p.Options.LogUpstreamErrorBodyMaxBytes)
	}
	return limit
}

// ApplyHTTPFailure applies provider health policy using the first supplied model.
func (p *OpenAIResponseOutput) ApplyHTTPFailure(ctx context.Context, resp *http.Response, target *gatewayprovider.ExecutionProvider, body []byte, models ...string) providercore.UpstreamErrorDecision {
	if len(models) > 0 {
		return gatewayprovider.ApplyOpenAIResponseHealth(ctx, p.Health, target, resp.StatusCode, resp.Header, body, false, models[0])
	}
	return gatewayprovider.ApplyOpenAIResponseHealth(ctx, p.Health, target, resp.StatusCode, resp.Header, body, false)
}

// ReadReplayableError reads and closes the upstream error body, then replaces resp.Body with a readable copy.
// It returns the raw body and a message with sensitive query parameters sanitized.
func (p *OpenAIResponseOutput) ReadReplayableError(resp *http.Response) ([]byte, string) {
	respBody := p.ReadErrorBody(resp)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	upstreamMsg := strings.TrimSpace(upstream.ExtractErrorMessage(respBody))
	upstreamMsg = logredact.SanitizeUpstreamQueries(upstreamMsg)
	return respBody, upstreamMsg
}
