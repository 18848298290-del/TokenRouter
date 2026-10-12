package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

type CompatibleTextCall struct {
	MessagesCall
	RequestContext                        context.Context
	ForwardBody                           []byte
	RequestPlatform, SelectionSessionHash string
}
type CompatibleTextBackend interface {
	Access(*gin.Context) (*apikey.APIKey, bool)
	ObserveRequest(*gin.Context, string, bool)
	ObserveEndpoint(*gin.Context, bool)
	Reasoning(*gin.Context, *apikey.APIKey, []byte) ([]byte, bool, error)
	PolicyDenied(*gin.Context)
	Plan(context.Context, *apikey.APIKey, string) routing.RoutePlan
	BindPlan(*gin.Context, routing.RoutePlan)
	ImageIntent(*apikey.APIKey, string, []byte, routing.GroupMappingResult) ([]byte, bool)
	ImageContext(context.Context) context.Context
	ChatImageModel(string, routing.GroupMappingResult) bool
	Moderate(*gin.Context, *zap.Logger, *apikey.APIKey, authctx.AuthSubject, string, string, []byte) *moderation.Decision
	BindErrors(*gin.Context)
	AuthLatency(*gin.Context, int64)
	Eligibility(context.Context, *apikey.APIKey, *billing.UserSubscription) error
	Isolate(context.Context, *apikey.APIKey, int64, string) error
	MarkStream(*gin.Context, string, string, int)
	FailoverObservation(context.Context, string, map[string]any)
}
type CompatibleTextHandler struct {
	executor execution.Executor
	RequestLifetime

	options     MessagesHTTPOptions
	backend     CompatibleTextBackend
	prompt      MessagesPrompt
	concurrency *ConcurrencyHelper
}

func NewCompatibleTextHandler(options MessagesHTTPOptions, backend CompatibleTextBackend, prompt MessagesPrompt, concurrency *ConcurrencyHelper, executor execution.Executor) *CompatibleTextHandler {
	return &CompatibleTextHandler{executor: executor, options: options, backend: backend, prompt: prompt, concurrency: concurrency}
}

func compatibleMaxBytesError(err error) (*http.MaxBytesError, bool) {
	var limit *http.MaxBytesError
	ok := errors.As(err, &limit)
	return limit, ok
}

func WriteCompatibleResponsesError(c *gin.Context, status int, kind, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": kind, "message": message}})
}

func WriteCompatibleChatError(c *gin.Context, status int, kind, message string) {
	c.JSON(status, gin.H{"error": gin.H{"type": kind, "message": message}})
}

func (h *CompatibleTextHandler) responsesErrorResponse(c *gin.Context, status int, kind, message string) {
	WriteCompatibleResponsesError(c, status, kind, message)
}

func (h *CompatibleTextHandler) chatCompletionsErrorResponse(c *gin.Context, status int, kind, message string) {
	WriteCompatibleChatError(c, status, kind, message)
}

func (h *CompatibleTextHandler) concurrencyError(c *gin.Context, err error, slot string, started bool) {
	status, kind, code, message := ConcurrencyErrorResponse(err, slot)
	WriteAnthropicStreamError(c, status, kind, code, message, started, h.backend.MarkStream)
}

func (h *CompatibleTextHandler) Responses(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "openai")
	if !accepted {
		return
	}
	defer done()

	streamStarted := false

	requestStart := time.Now()

	apiKey, ok := h.backend.Access(c)
	if !ok {
		h.responsesErrorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		h.responsesErrorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := RequestLogger(
		c,
		"handler.gateway.responses",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	body, err := ReadLenientJSONRequestBodyWithPrealloc(c.Request, h.options.MaxBodyBytes)
	if err != nil {
		if maxErr, ok := compatibleMaxBytesError(err); ok {
			h.responsesErrorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", BodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.responsesErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	if len(body) == 0 {
		h.responsesErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	h.backend.ObserveRequest(c, "", false)

	if !gjson.ValidBytes(body) {
		LogRequestBodyParseFailure(reqLog, body, nil)
		h.responsesErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	// Apply prompt replacements before model parsing, moderation, and session hashing so all use the same body.
	body = h.prompt.ApplyUserPromptReplacementToBody(c.Request.Context(), body, "openai_responses")

	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.responsesErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	apiKey, err = resolveClientGroupForRequest(c, h.backend, apiKey, protocol.ProtocolOpenAIResponses)
	if err != nil {
		writeClientGroupFallbackError(c, err, h.responsesErrorResponse)
		return
	}

	// Apply client effort policy before conversion; auditing retains the requested value.
	policyBody, _, policyErr := h.backend.Reasoning(c, apiKey, body)
	if policyErr != nil {
		h.backend.PolicyDenied(c)
		h.responsesErrorResponse(c, http.StatusForbidden, "permission_error", policyErr.Error())
		return
	}
	body = policyBody
	reqStream, ok := ParseOpenAICompatibleStream(body)
	if !ok {
		h.responsesErrorResponse(c, http.StatusBadRequest, "invalid_request_error", InvalidStreamFieldTypeMessage)
		return
	}
	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))

	h.backend.ObserveRequest(c, reqModel, reqStream)
	h.backend.ObserveEndpoint(c, reqStream)
	// Image capability and model rate limits use the group-mapped model and body.
	// The route plan captures the effective group and model mapping.
	groupMappingRoutePlan := h.backend.Plan(c.Request.Context(), apiKey, reqModel)
	groupMapping := groupMappingRoutePlan.Mapping()
	h.backend.BindPlan(c, groupMappingRoutePlan)
	forwardBody, imageIntent := h.backend.ImageIntent(apiKey, reqModel, body, groupMapping)
	requestCtx := c.Request.Context()
	if imageIntent {
		requestCtx = h.backend.ImageContext(requestCtx)
	}

	if decision := h.backend.Moderate(c, reqLog, apiKey, subject, moderation.ContentModerationProtocolOpenAIResponses, reqModel, body); decision != nil && decision.Blocked {
		h.responsesErrorResponse(c, ModerationHTTPStatus(decision), "content_policy_violation", decision.Message)
		return
	}

	h.backend.BindErrors(c)

	subscription, _ := SubscriptionFromContext(c)

	h.backend.AuthLatency(c, time.Since(requestStart).Milliseconds())

	userReleaseFunc, err := h.concurrency.AcquireUserSlotWithWait(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted)
	if err != nil {
		reqLog.Warn("gateway.responses.user_slot_acquire_failed", zap.Error(err))
		h.concurrencyError(c, err, "user", streamStarted)
		return
	}
	// Carry the newly acquired lease into the context that already holds image intent.
	if lease := scheduler.RequestLease(c.Request.Context()); lease != nil {
		requestCtx = scheduler.WithRequestLease(requestCtx, lease)
	}
	userReleaseFunc = scheduler.WrapRelease(c.Request.Context(), scheduler.ReleaseOnCancel, userReleaseFunc)
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// Recheck funding eligibility after waiting for the user slot.
	if err := h.backend.Eligibility(requestCtx, apiKey, subscription); err != nil {
		reqLog.Info("gateway.responses.billing_check_failed", zap.Error(err))
		status, code, message, retryAfter := BillingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.responsesErrorResponse(c, status, code, message)
		return
	}

	bodyRef := requeststate.NewRequestBodyRef(body)
	parsedReq, _ := requeststate.ParseGatewayRequest(bodyRef, "responses")
	if parsedReq == nil {
		parsedReq = &requeststate.ParsedRequest{Model: reqModel, Stream: reqStream, Body: bodyRef}
	}
	parsedReq.SessionContext = &requeststate.SessionContext{
		ClientIP:  clientip.GetClientIP(c),
		UserAgent: c.GetHeader("User-Agent"),
		APIKeyID:  apiKey.ID,
	}
	sessionHash := session.GenerateSessionHash(parsedReq, slog.Info)
	if isolationSessionID := MetadataSessionID(parsedReq.MetadataUserID); isolationSessionID != "" {
		if err := h.backend.Isolate(c.Request.Context(), apiKey, subject.UserID, isolationSessionID); err != nil {
			if errors.Is(err, session.ErrSessionIsolationConflict) {
				h.responsesErrorResponse(c, http.StatusForbidden, "permission_error", session.SessionIsolationConflictMessage)
			} else {
				h.responsesErrorResponse(c, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable")
			}
			return
		}
	}

	call := CompatibleTextCall{
		MessagesCall: MessagesCall{
			Key:           apiKey,
			Subject:       subject,
			Subscription:  subscription,
			Parsed:        parsedReq,
			Body:          body,
			Model:         reqModel,
			Stream:        reqStream,
			SessionKey:    sessionHash,
			StreamStarted: &streamStarted,
			Log:           reqLog,
			Route:         groupMappingRoutePlan,
			Mapping:       groupMapping,
		},
		RequestContext: requestCtx,
		ForwardBody:    forwardBody,
	}
	h.executeCompatible(c, call, execution.TextGenericResponses)
}

func (h *CompatibleTextHandler) ChatCompletions(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "openai")
	if !accepted {
		return
	}
	defer done()

	streamStarted := false

	requestStart := time.Now()

	apiKey, ok := h.backend.Access(c)
	if !ok {
		h.chatCompletionsErrorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		h.chatCompletionsErrorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := RequestLogger(
		c,
		"handler.gateway.chat_completions",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	body, err := ReadLenientJSONRequestBodyWithPrealloc(c.Request, h.options.MaxBodyBytes)
	if err != nil {
		if maxErr, ok := compatibleMaxBytesError(err); ok {
			h.chatCompletionsErrorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", BodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	if len(body) == 0 {
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	h.backend.ObserveRequest(c, "", false)

	if !gjson.ValidBytes(body) {
		LogRequestBodyParseFailure(reqLog, body, nil)
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	// Apply prompt replacements before model parsing, moderation, and session hashing so all use the same body.
	body = h.prompt.ApplyUserPromptReplacementToBody(c.Request.Context(), body, "chat_completions")

	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	apiKey, err = resolveClientGroupForRequest(c, h.backend, apiKey, protocol.ProtocolOpenAIChatCompletions)
	if err != nil {
		writeClientGroupFallbackError(c, err, h.chatCompletionsErrorResponse)
		return
	}

	// Apply the same group reasoning limits as the Messages and Responses entries.
	policyBody, _, policyErr := h.backend.Reasoning(c, apiKey, body)
	if policyErr != nil {
		h.backend.PolicyDenied(c)
		h.chatCompletionsErrorResponse(c, http.StatusForbidden, "permission_error", policyErr.Error())
		return
	}
	body = policyBody
	reqStream, ok := ParseOpenAICompatibleStream(body)
	if !ok {
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", InvalidStreamFieldTypeMessage)
		return
	}
	// Check Chat capability against the group-mapped model; logs and errors use the client model.
	// The route plan captures the effective group and model mapping.
	groupMappingRoutePlan := h.backend.Plan(c.Request.Context(), apiKey, reqModel)
	groupMapping := groupMappingRoutePlan.Mapping()
	h.backend.BindPlan(c, groupMappingRoutePlan)
	if h.backend.ChatImageModel(reqModel, groupMapping) {
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "This model is not supported on the Chat Completions endpoint")
		return
	}
	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))

	h.backend.ObserveRequest(c, reqModel, reqStream)
	h.backend.ObserveEndpoint(c, reqStream)

	if decision := h.backend.Moderate(c, reqLog, apiKey, subject, moderation.ContentModerationProtocolOpenAIChat, reqModel, body); decision != nil && decision.Blocked {
		h.chatCompletionsErrorResponse(c, ModerationHTTPStatus(decision), "content_policy_violation", decision.Message)
		return
	}

	h.backend.BindErrors(c)

	subscription, _ := SubscriptionFromContext(c)

	h.backend.AuthLatency(c, time.Since(requestStart).Milliseconds())

	userReleaseFunc, err := h.concurrency.AcquireUserSlotWithWait(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted)
	if err != nil {
		reqLog.Warn("gateway.cc.user_slot_acquire_failed", zap.Error(err))
		h.concurrencyError(c, err, "user", streamStarted)
		return
	}
	userReleaseFunc = scheduler.WrapRelease(c.Request.Context(), scheduler.ReleaseOnCancel, userReleaseFunc)
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// Recheck funding eligibility after waiting for the user slot.
	if err := h.backend.Eligibility(c.Request.Context(), apiKey, subscription); err != nil {
		reqLog.Info("gateway.cc.billing_check_failed", zap.Error(err))
		status, code, message, retryAfter := BillingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.chatCompletionsErrorResponse(c, status, code, message)
		return
	}

	bodyRef := requeststate.NewRequestBodyRef(body)
	parsedReq, _ := requeststate.ParseGatewayRequest(bodyRef, "chat_completions")
	if parsedReq == nil {
		parsedReq = &requeststate.ParsedRequest{Model: reqModel, Stream: reqStream, Body: bodyRef}
	}
	parsedReq.SessionContext = &requeststate.SessionContext{
		ClientIP:  clientip.GetClientIP(c),
		UserAgent: c.GetHeader("User-Agent"),
		APIKeyID:  apiKey.ID,
	}
	sessionHash := session.GenerateSessionHash(parsedReq, slog.Info)
	requestPlatform, _ := apikey.ForcePlatformFromContext(c.Request.Context())
	selectionSessionHash := sessionHash
	if requestPlatform == capability.PlatformGemini && selectionSessionHash != "" {
		selectionSessionHash = "gemini:" + selectionSessionHash
	}
	if isolationSessionID := MetadataSessionID(parsedReq.MetadataUserID); isolationSessionID != "" {
		if err := h.backend.Isolate(c.Request.Context(), apiKey, subject.UserID, isolationSessionID); err != nil {
			if errors.Is(err, session.ErrSessionIsolationConflict) {
				h.chatCompletionsErrorResponse(c, http.StatusForbidden, "permission_error", session.SessionIsolationConflictMessage)
			} else {
				h.chatCompletionsErrorResponse(c, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable")
			}
			return
		}
	}

	call := CompatibleTextCall{
		MessagesCall: MessagesCall{
			Key:           apiKey,
			Subject:       subject,
			Subscription:  subscription,
			Parsed:        parsedReq,
			Body:          body,
			Model:         reqModel,
			Stream:        reqStream,
			SessionKey:    sessionHash,
			StreamStarted: &streamStarted,
			Log:           reqLog,
			Route:         groupMappingRoutePlan,
			Mapping:       groupMapping,
		},
		RequestContext:       c.Request.Context(),
		RequestPlatform:      requestPlatform,
		SelectionSessionHash: selectionSessionHash,
	}
	h.executeCompatible(c, call, execution.TextGenericChat)
}

func (h *CompatibleTextHandler) executeCompatible(c *gin.Context, call CompatibleTextCall, kind execution.TextKind) {
	request := execution.Request{
		Hints:       requeststate.ExecutionHintsFromContext(c.Request.Context()),
		Routing:     requeststate.RoutingStateFromContext(c.Request.Context()),
		Route:       call.Route,
		UserID:      call.Subject.UserID,
		Concurrency: call.Subject.Concurrency,
		Stream:      call.Stream,
		Body:        call.Body,
		Model:       call.Model,
		Funding: execution.FundingState{
			Key:          call.Key,
			Subscription: call.Subscription,
		},
		SessionHash: call.SessionKey,
		AttemptBody: call.ForwardBody,

		Text: execution.TextState{Kind: kind, Parsed: call.Parsed, Platform: call.RequestPlatform, SelectionContext: call.RequestContext, SelectionSessionHash: call.SelectionSessionHash, Mapping: call.Mapping, AlternateBudget: kind == execution.TextGenericChat && call.RequestPlatform == capability.PlatformGemini},
	}
	output := &MessagesOutput{ResponseSink: ResponseSink{Writer: c.Writer}, HTTP: c, Log: call.Log, StreamStarted: call.StreamStarted}
	_, _ = h.executor.Execute(c.Request.Context(), request, output)
}
