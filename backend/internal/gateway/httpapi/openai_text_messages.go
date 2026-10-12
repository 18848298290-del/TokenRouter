package httpapi

import (
	"net/http"
	"strconv"
	"strings"
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

func (h *OpenAITextHandler) Messages(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "anthropic")
	if !accepted {
		return
	}
	defer done()

	streamStarted := false
	defer h.recoverAnthropicMessagesPanic(c, &streamStarted)

	requestStart := time.Now()

	apiKey, ok := h.backend.Access(c)
	if !ok {
		h.anthropicErrorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		h.anthropicErrorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := RequestLogger(
		c,
		"handler.openai_gateway.messages",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	// Check group permission to schedule /v1/messages.
	if !h.backend.AllowsMessages(apiKey) {
		h.backend.PolicyDenied(c)
		h.anthropicErrorResponse(c, http.StatusForbidden, "permission_error",
			"This group does not allow Anthropic Messages requests")
		return
	}

	if !h.backend.Dependencies(c, reqLog) {
		return
	}

	body, err := ReadLenientJSONRequestBodyWithPrealloc(c.Request, h.options.MaxBodyBytes)
	if err != nil {
		if maxErr, ok := openAITextMaxBytesError(err); ok {
			h.anthropicErrorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", BodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	if !gjson.ValidBytes(body) {
		LogRequestBodyParseFailure(reqLog, body, nil)
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	// Replace user prompts before model resolution, moderation, and session hashing so later steps share the
	// rewritten body.
	body = h.prompt.ApplyUserPromptReplacementToBody(c.Request.Context(), body, "anthropic_messages")

	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	if preparer, ok := h.backend.(interface {
		PrepareMessages(*gin.Context, []byte) error
	}); ok {
		if err := preparer.PrepareMessages(c, body); err != nil {
			h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	} else if err := PrepareMessageClientContext(c, body, nil); err != nil {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	apiKey, err = resolveClientGroupForRequest(c, h.backend, apiKey, protocol.ProtocolAnthropicMessages)
	if err != nil {
		writeClientGroupFallbackError(c, err, h.anthropicErrorResponse)
		return
	}
	h.backend.MessageReasoning(c, apiKey, body)
	reqStream := gjson.GetBytes(body, "stream").Bool()

	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))

	h.backend.ObserveRequest(c, reqModel, reqStream)
	h.backend.ObserveEndpoint(c, reqStream)
	h.backend.Snapshot(c, protocol.ProtocolAnthropicMessages, body)

	if decision := h.backend.Moderate(c, reqLog, apiKey, subject, protocol.ProtocolAnthropicMessages, reqModel, body); decision != nil && decision.Blocked {
		h.anthropicErrorResponse(c, ModerationHTTPStatus(decision), "content_policy_violation", decision.Message)
		return
	}

	// Resolve the group model mapping.
	// Pass the current group and mapped model to the execution plan.
	groupMappingMsgRoutePlan := h.backend.Plan(c.Request.Context(), apiKey, reqModel)
	groupMappingMsg := groupMappingMsgRoutePlan.Mapping()
	h.backend.BindPlan(c, groupMappingMsgRoutePlan)
	groupMappedModel := strings.TrimSpace(groupMappingMsg.MappedModel)
	if groupMappedModel == "" {
		groupMappedModel = reqModel
	}
	providerLayerModel := h.backend.MessageProviderModel(c.Request.Context(), apiKey, groupMappedModel)

	// Bind passthrough rules for downstream non-failover error handling.
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
		reqLog.Info("openai_messages.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := BillingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.anthropicStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	sessionHash := h.backend.SessionHash(c, OpenAISelectionSession, body)
	promptCacheKey := h.backend.SessionHash(c, OpenAIPromptCacheSession, body)
	sessionHash, promptCacheKey = h.backend.MetadataSession(c, sessionHash, promptCacheKey, reqModel, body)
	if h.backend.RejectCyber(c, apiKey, body, reqModel, protocol.ProtocolAnthropicMessages) {
		return
	}
	isolationSource := session.SessionIsolationSourceOpenAI
	explicitSessionHash := h.backend.SessionHash(c, OpenAIExplicitSession, body)
	if explicitSessionHash == "" {
		if isolationSessionID := MetadataSessionID(gjson.GetBytes(body, "metadata.user_id").String()); isolationSessionID != "" {
			explicitSessionHash = isolationSessionID
			isolationSource = session.SessionIsolationSourceGateway
		}
	}
	if explicitSessionHash != "" {
		c.Request = c.Request.WithContext(requeststate.WithSessionIsolation(c.Request.Context(), isolationSource, explicitSessionHash))
		if err := h.backend.Isolate(c.Request.Context(), apiKey, subject.UserID, isolationSource, explicitSessionHash); h.handleAnthropicSessionIsolationError(c, err, streamStarted) {
			return
		}
	}

	call := OpenAITextCall{
		Route:    groupMappingMsgRoutePlan,
		Protocol: protocol.ProtocolAnthropicMessages, Key: apiKey, Subject: subject, Subscription: subscription,
		Body: body, Model: reqModel, SessionHash: sessionHash, Platform: requestPlatform, Stream: reqStream,
		StreamStarted: &streamStarted, RoutingStart: routingStart, Log: reqLog, Mapping: groupMappingMsg,
		ProviderLayerModel: providerLayerModel, PromptCacheKey: promptCacheKey,
	}
	h.executeText(c, call, execution.TextOpenAIMessages)
}
