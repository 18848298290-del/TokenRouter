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
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Responses handles Compact normalization, response ownership, and funds rechecks after waiting.
// @project-doc docs/interfaces/openai_upstream.md#openai_protocol_dispatch
func (h *OpenAITextHandler) Responses(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "openai")
	if !accepted {
		return
	}
	defer done()

	// Recover handler panics according to the current response state.
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)
	compactStartedAt := time.Now()
	defer h.LogRemoteCompactOutcome(c, compactStartedAt)
	h.backend.TransportHTTP(c)

	requestStart := time.Now()

	// Read the key and acting subject set by authentication.
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
		"handler.openai_gateway.responses",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	if !h.backend.Dependencies(c, reqLog) {
		return
	}

	// Read the body after authentication and dependency validation.
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

	h.backend.ObserveRequest(c, "", false)
	body, ok = h.normalizeOpenAIResponsesCompactRequest(c, reqLog, body)
	if !ok {
		return
	}
	legacyCompact, nativeCompactionV2 := IsOpenAIResponsesCompactPath(c), IsBareOpenAIResponsesPath(c) && IsOpenAIRemoteCompactionV2Request(body)
	// Body-signaled Compact sends SSE comment heartbeats while waiting to keep proxy connections alive (#3887).
	// Delay the first heartbeat by one interval so earlier failures can return JSON status; non-streaming or
	// zero-interval requests skip heartbeats.
	stopCompactKeepalive := h.backend.StartCompact(c, h.options.CompactKeepaliveInterval)
	defer stopCompactKeepalive()

	// Validate request JSON.
	if !gjson.ValidBytes(body) {
		LogRequestBodyParseFailure(reqLog, body, nil)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	// Replace user prompts after Compact normalization and before model parsing.
	body = h.prompt.ApplyUserPromptReplacementToBody(c.Request.Context(), body, "openai_responses")
	sessionHashBody := body

	// Extract fields needed for validation.
	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	apiKey, err = resolveClientGroupForRequest(c, h.backend, apiKey, protocol.ProtocolOpenAIResponses)
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
	if normalizedBody, changed := h.backend.NormalizeBootstrap(body, false); changed {
		body = normalizedBody
		reqLog.Info("openai.codex_automation_bootstrap_normalized",
			zap.String("normalization", "call_output_to_user_message"),
		)
	}
	if normalizedBody, changed := h.backend.NormalizeBootstrap(body, true); changed {
		body = normalizedBody
		reqLog.Info("openai.codex_delegation_bootstrap_normalized",
			zap.String("normalization", "call_output_to_user_message"),
		)
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
	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))
	previousResponseID := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	if previousResponseID != "" {
		previousResponseIDKind := h.backend.PreviousKind(previousResponseID)
		reqLog = reqLog.With(
			zap.Bool("has_previous_response_id", true),
			zap.String("previous_response_id_kind", previousResponseIDKind),
			zap.Int("previous_response_id_len", len(previousResponseID)),
		)
		if previousResponseIDKind == "message_id" {
			reqLog.Warn("openai.request_validation_failed",
				zap.String("reason", "previous_response_id_looks_like_message_id"),
			)
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "previous_response_id must be a response.id (resp_*), not a message id")
			return
		}
		groupID := int64(0)
		if apiKey.GroupID != nil {
			groupID = *apiKey.GroupID
		}
		owned, ownershipErr := h.backend.ValidateOwner(
			c.Request.Context(),
			groupID,
			previousResponseID,
			subject.UserID,
			apiKey.ID,
		)
		if ownershipErr != nil {
			reqLog.Warn("openai.previous_response_owner_lookup_failed", zap.Error(ownershipErr))
		}
		if !owned {
			reqLog.Warn("openai.request_validation_failed", zap.String("reason", "previous_response_owner_mismatch"))
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "previous_response_id is not available for this user")
			return
		}
	}
	h.backend.SetOwner(c, subject.UserID, apiKey.ID)

	h.backend.ObserveRequest(c, reqModel, reqStream)
	h.backend.ObserveEndpoint(c, reqStream)
	h.backend.Snapshot(c, protocol.ProtocolOpenAIResponses, body)

	if decision := h.backend.Moderate(c, reqLog, apiKey, subject, protocol.ProtocolOpenAIResponses, reqModel, body); decision != nil && decision.Blocked {
		h.errorResponse(c, ModerationHTTPStatus(decision), "content_policy_violation", decision.Message)
		return
	}

	// Group model G controls image concurrency and endpoint capability; client model R identifies logs and
	// sessions.
	// Store the group and mapped model in the execution plan.
	groupMappingRoutePlan := h.backend.Plan(c.Request.Context(), apiKey, reqModel)
	groupMapping := groupMappingRoutePlan.Mapping()
	h.backend.BindPlan(c, groupMappingRoutePlan)
	forwardBody, routingModel, forwardImageIntent := h.backend.ImageIntent(reqModel, body, groupMapping, h.backend.Platform(apiKey))
	forwardModel := strings.TrimSpace(routingModel)
	if forwardModel == "" {
		forwardModel = reqModel
	}
	// Client-declared image intent controls permissions, concurrency, and provider capability; broader intent
	// guides tools and billing.
	imageIntent := h.backend.ExplicitImageIntent("/v1/responses", routingModel, forwardBody)
	selectionCtx := c.Request.Context()
	if imageIntent {
		// Use group-mapped client image intent for the image-family concurrency marker.
		selectionCtx = h.backend.ImageContext(selectionCtx)
	}
	if imageIntent && !h.backend.AllowsImages(apiKey) {
		h.backend.FeatureDenied(c)
		h.errorResponse(c, http.StatusForbidden, "permission_error", h.backend.ImagePermissionMessage())
		return
	}
	var imageReleaseFunc func()
	if imageIntent {
		var imageAcquired bool
		imageReleaseFunc, imageAcquired = h.backend.ImageSlot(c, streamStarted)
		if !imageAcquired {
			return
		}
		if imageReleaseFunc != nil {
			defer imageReleaseFunc()
		}
	}

	h.backend.SeedImageIntent(c, groupMapping.Mapped, forwardImageIntent)

	// Validate function_call_output context before forwarding to prevent upstream 400 errors.
	if !h.backend.ValidateTools(c, body, reqLog) {
		return
	}

	// Bind passthrough rules for downstream non-failover error handling.
	h.backend.BindErrors(c)

	// A missing subscription uses balance funding.
	subscription, _ := SubscriptionFromContext(c)
	requestPlatform := h.backend.Platform(apiKey)

	h.backend.AuthLatency(c, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	userReleaseFunc, acquired := h.backend.UserSlot(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted, reqLog)
	if !acquired {
		return
	}
	// Add the acquired user lease to the selection context while retaining HTTP passthrough state.
	if lease := scheduler.RequestLease(c.Request.Context()); lease != nil {
		selectionCtx = scheduler.WithRequestLease(selectionCtx, lease)
	}
	// Release the slot on cancellation, including long-lived connection disconnection.
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// Recheck funds after the wait succeeds.
	if err := h.backend.Eligibility(c.Request.Context(), apiKey, subscription); err != nil {
		reqLog.Info("openai.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := BillingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	// Prefer session headers and fall back to prompt_cache_key.
	explicitSessionHash := h.backend.SessionHash(c, OpenAIExplicitSession, sessionHashBody)
	sessionHash := h.backend.SessionHash(c, OpenAISelectionSession, sessionHashBody)
	if h.backend.RejectCyber(c, apiKey, sessionHashBody, reqModel, protocol.ProtocolOpenAIResponses) {
		return
	}
	if explicitSessionHash != "" {
		c.Request = c.Request.WithContext(requeststate.WithSessionIsolation(c.Request.Context(), session.SessionIsolationSourceOpenAI, explicitSessionHash))
		if err := h.backend.Isolate(c.Request.Context(), apiKey, subject.UserID, session.SessionIsolationSourceOpenAI, explicitSessionHash); h.handleOpenAISessionIsolationError(c, err, streamStarted) {
			return
		}
	}
	c.Request = c.Request.WithContext(h.backend.GuardianContext(
		c.Request.Context(), c, sessionHashBody, reqModel,
	))
	requireCompact := legacyCompact

	// OpenAI image generation requires Responses support; raw Chat cannot generate images (#4417).
	// Grok has its own Responses execution. Reuse the admitted image intent from group model G and forwardBody.
	// A passive Codex image_gen namespace leaves Chat-only providers eligible (#4476).
	requiredCapability := textflow.RequiredResponsesCapability(
		imageIntent,
		nativeCompactionV2,
		legacyCompact,
		requestPlatform,
	)

	call := OpenAITextCall{
		Route:    groupMappingRoutePlan,
		Protocol: protocol.ProtocolOpenAIResponses, Key: apiKey, Subject: subject, Subscription: subscription,
		Body: body, ForwardBody: forwardBody, SessionHashBody: sessionHashBody,
		Model: reqModel, ForwardModel: forwardModel, SessionHash: sessionHash, PreviousResponseID: previousResponseID,
		Platform: requestPlatform, Stream: reqStream, NativeCompactionV2: nativeCompactionV2, LegacyCompact: legacyCompact,
		RequireCompact: requireCompact, StreamStarted: &streamStarted, SelectionContext: selectionCtx,
		Mapping: groupMapping, RoutingStart: routingStart, RequiredCapability: requiredCapability, Log: reqLog,
	}
	h.executeText(c, call, execution.TextOpenAIResponses)
}
