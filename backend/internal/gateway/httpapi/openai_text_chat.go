package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

func (h *OpenAITextHandler) ChatCompletions(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "openai")
	if !accepted {
		return
	}
	defer done()

	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)

	requestStart := time.Now()

	apiKey, ok := h.backend.Access(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := RequestLogger(
		c,
		"handler.openai_gateway.chat_completions",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	if !h.backend.Dependencies(c, reqLog) {
		return
	}

	body, err := ReadLenientJSONRequestBodyWithPrealloc(c.Request, h.options.MaxBodyBytes)
	if err != nil {
		if maxErr, ok := openAITextMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", BodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.backend.ReadFailure(reqLog, c.Request, err)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	if !gjson.ValidBytes(body) {
		LogRequestBodyParseFailure(reqLog, body, nil)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	// Replace user prompts before model resolution, moderation, and session hashing so later steps share the
	// rewritten body.
	body = h.prompt.ApplyUserPromptReplacementToBody(c.Request.Context(), body, "chat_completions")

	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	apiKey, err = resolveClientGroupForRequest(c, h.backend, apiKey, protocol.ProtocolOpenAIChatCompletions)
	if err != nil {
		writeClientGroupFallbackError(c, err, h.errorResponse)
		return
	}
	if cappedBody, changed, policyErr := h.backend.Reasoning(c, apiKey, body); policyErr != nil {
		h.backend.PolicyDenied(c)
		h.errorResponse(c, http.StatusForbidden, "permission_error", policyErr.Error())
		return
	} else if changed {
		body = cappedBody
	}
	reqStream, ok := ParseOpenAICompatibleStream(body)
	if !ok {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", InvalidStreamFieldTypeMessage)
		return
	}
	if err := h.backend.ValidateTier(body); err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	// Use group-mapped model G for Chat endpoint capability checks and client model R for logs and errors.
	// Pass the current group and mapped model to the execution plan.
	groupMappingRoutePlan := h.backend.Plan(c.Request.Context(), apiKey, reqModel)
	groupMapping := groupMappingRoutePlan.Mapping()
	h.backend.BindPlan(c, groupMappingRoutePlan)
	if h.backend.ChatImageModel(reqModel, groupMapping) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "This model is not supported on the Chat Completions endpoint")
		return
	}

	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))

	h.backend.ObserveRequest(c, reqModel, reqStream)
	h.backend.ObserveEndpoint(c, reqStream)
	h.backend.Snapshot(c, protocol.ProtocolOpenAIChatCompletions, body)

	if decision := h.backend.Moderate(c, reqLog, apiKey, subject, protocol.ProtocolOpenAIChatCompletions, reqModel, body); decision != nil && decision.Blocked {
		h.errorResponse(c, ModerationHTTPStatus(decision), "content_policy_violation", decision.Message)
		return
	}

	h.backend.BindErrors(c)

	subscription, _ := SubscriptionFromContext(c)
	requestPlatform := h.backend.Platform(apiKey)

	h.backend.AuthLatency(c, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	userReleaseFunc, acquired := h.backend.UserSlot(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	if err := h.backend.Eligibility(c.Request.Context(), apiKey, subscription); err != nil {
		reqLog.Info("openai_chat_completions.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := BillingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	sessionHash := h.backend.SessionHash(c, OpenAISelectionSession, body)
	promptCacheKey := h.backend.SessionHash(c, OpenAIPromptCacheSession, body)
	explicitSessionHash := h.backend.SessionHash(c, OpenAIExplicitSession, body)
	if h.backend.RejectCyber(c, apiKey, body, reqModel, protocol.ProtocolOpenAIChatCompletions) {
		return
	}
	if explicitSessionHash != "" {
		c.Request = c.Request.WithContext(requeststate.WithSessionIsolation(c.Request.Context(), session.SessionIsolationSourceOpenAI, explicitSessionHash))
		if err := h.backend.Isolate(c.Request.Context(), apiKey, subject.UserID, session.SessionIsolationSourceOpenAI, explicitSessionHash); h.handleOpenAISessionIsolationError(c, err, streamStarted) {
			return
		}
	}

	call := OpenAITextCall{
		Route:    groupMappingRoutePlan,
		Protocol: protocol.ProtocolOpenAIChatCompletions, Key: apiKey, Subject: subject, Subscription: subscription,
		Body: body, Model: reqModel, SessionHash: sessionHash, Platform: requestPlatform, Stream: reqStream,
		StreamStarted: &streamStarted, RoutingStart: routingStart, Mapping: groupMapping, Log: reqLog, PromptCacheKey: promptCacheKey,
	}
	h.executeText(c, call, execution.TextOpenAIChat)
}
