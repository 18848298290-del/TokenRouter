package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

type MessagesHTTPOptions struct {
	MaxBodyBytes                   int64
	MaxSwitches, MaxGeminiSwitches int
}
type MessagesPrompt interface {
	ApplyUserPromptReplacementToBody(context.Context, []byte, string) []byte
}

// MessagesCall holds identity, routing, and execution state for one request.
type MessagesCall struct {
	Key                                      *apikey.APIKey
	Subject                                  authctx.AuthSubject
	Subscription                             *billing.UserSubscription
	Parsed                                   *requeststate.ParsedRequest
	Body, GeminiBody                         []byte
	Model, GeminiModel, Platform, SessionKey string
	Stream, ClaudeCode, HasBoundSession      bool
	BoundProviderID                          int64
	StreamStarted                            *bool
	Log                                      *zap.Logger
	Route                                    routing.RoutePlan
	Mapping                                  routing.GroupMappingResult
}

// MessagesBackend supplies admission, routing, and observation operations for Messages requests.
type MessagesBackend interface {
	Access(*gin.Context) (*apikey.APIKey, bool)
	CompatibilityMetrics(*zap.Logger)
	ObserveRequest(*gin.Context, string, bool)
	ObserveEndpoint(*gin.Context, bool)
	Reasoning(*gin.Context, *apikey.APIKey, []byte) ([]byte, bool, error)
	PolicyDenied(*gin.Context)
	Plan(context.Context, *apikey.APIKey, string) routing.RoutePlan
	BindPlan(*gin.Context, routing.RoutePlan)
	BindProbe(*gin.Context)
	Probe(*gin.Context) bool
	BindClient(*gin.Context, ClientDetection)
	BindThinking(*gin.Context, bool)
	ClientVersion(*gin.Context) string
	ClientVersionBounds(context.Context) (string, string)
	Moderate(*gin.Context, *zap.Logger, *apikey.APIKey, authctx.AuthSubject, string, []byte) *moderation.Decision
	BindErrors(*gin.Context)
	Eligibility(context.Context, *apikey.APIKey, *billing.UserSubscription) error
	ForcedPlatform(*gin.Context) (string, bool)
	Isolate(context.Context, *apikey.APIKey, int64, string) error
	CachedSession(context.Context, *int64, string) (int64, error)
	Prefetch(*gin.Context, int64, int64)
	PrepareGemini(context.Context, MessagesCall) (*requeststate.ParsedRequest, error)
	MarkStream(*gin.Context, string, string, int)
	FailoverObservation(context.Context, string, map[string]any)
}

// MessagesHandler uses application-bound dependencies and allocates execution state per request.
type MessagesHandler struct {
	executor execution.Executor
	RequestLifetime

	options     MessagesHTTPOptions
	backend     MessagesBackend
	prompt      MessagesPrompt
	concurrency *ConcurrencyHelper
}

func NewMessagesHandler(options MessagesHTTPOptions, backend MessagesBackend, prompt MessagesPrompt, concurrency *ConcurrencyHelper, executor execution.Executor) *MessagesHandler {
	return &MessagesHandler{options: options, backend: backend, prompt: prompt, concurrency: concurrency, executor: executor}
}

// Messages handles the Claude-compatible messages endpoint.
// POST /v1/messages.
func (h *MessagesHandler) Messages(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "anthropic")
	if !accepted {
		return
	}
	defer done()

	// Authentication middleware supplies the key and user context.
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
		"handler.gateway.messages",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	defer h.backend.CompatibilityMetrics(reqLog)

	body, err := ReadLenientJSONRequestBodyWithPrealloc(c.Request, h.options.MaxBodyBytes)
	if err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", BodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	h.backend.ObserveRequest(c, "", false)

	// Rewrite prompts before parsing, moderation, and session hashing so they use the same body.
	body = h.prompt.ApplyUserPromptReplacementToBody(c.Request.Context(), body, "anthropic_messages")

	bodyRef := requeststate.NewRequestBodyRef(body)
	parsedReq, err := requeststate.ParseGatewayRequest(bodyRef, capability.PlatformAnthropic)
	if err != nil {
		LogRequestBodyParseFailure(reqLog, body, err)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	body = parsedReq.Body.Bytes()
	reqModel := parsedReq.Model
	reqStream := parsedReq.Stream
	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))

	// Mark max_tokens=1 Haiku probes before client detection.
	// The Claude Code validator uses this marker to skip probe validation.
	if clientmeta.IsHaikuProbe(reqModel, parsedReq.MaxTokens) {
		h.backend.BindProbe(c)
	}

	detection := DetectClaudeCodeRequest(c, body, parsedReq, h.backend.Probe(c))
	h.backend.BindClient(c, detection)
	isClaudeCodeClient := detection.ClaudeCode

	// Apply configured version bounds to Claude Code clients.
	if !h.checkClientVersion(c, detection) {
		return
	}

	apiKey, err = resolveClientGroupForRequest(c, h.backend, apiKey, protocol.ProtocolAnthropicMessages)
	if err != nil {
		writeClientGroupFallbackError(c, err, h.errorResponse)
		return
	}
	if policyBody, changed, policyErr := h.backend.Reasoning(c, apiKey, body); policyErr != nil {
		h.backend.PolicyDenied(c)
		h.errorResponse(c, http.StatusForbidden, "permission_error", policyErr.Error())
		return
	} else if changed {
		if err := parsedReq.ReplaceBody(policyBody); err != nil {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to apply reasoning effort policy")
			return
		}
		body = parsedReq.Body.Bytes()
	}
	// Capture the group and model mapping in this request's route plan.
	groupMappingRoutePlan := h.backend.Plan(c.Request.Context(), apiKey, reqModel)
	groupMapping := groupMappingRoutePlan.Mapping()
	h.backend.BindPlan(c, groupMappingRoutePlan)

	// Antigravity uses thinking state to derive the final model key and model rate limits.
	h.backend.BindThinking(c, parsedReq.ThinkingEnabled)

	h.backend.ObserveRequest(c, reqModel, reqStream)
	h.backend.ObserveEndpoint(c, reqStream)

	if reqModel == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	if decision := h.backend.Moderate(c, reqLog, apiKey, subject, reqModel, body); decision != nil && decision.Blocked {
		h.errorResponse(c, ModerationHTTPStatus(decision), "content_policy_violation", decision.Message)
		return
	}

	streamStarted := false

	// Bind error rules for failures that do not trigger provider failover.
	h.backend.BindErrors(c)

	// The subscription may be nil for balance-funded requests.
	subscription, _ := SubscriptionFromContext(c)

	userReleaseFunc, err := h.concurrency.AcquireUserSlotWithWait(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted)
	if err != nil {
		reqLog.Warn("gateway.user_slot_acquire_failed", zap.Error(err))
		h.concurrencyError(c, err, "user", streamStarted)
		return
	}
	// Release on request completion or context cancellation.
	userReleaseFunc = scheduler.WrapRelease(c.Request.Context(), scheduler.ReleaseOnCancel, userReleaseFunc)
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// Funding eligibility may change while waiting for a user slot.
	if err := h.backend.Eligibility(c.Request.Context(), apiKey, subscription); err != nil {
		reqLog.Info("gateway.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := BillingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.streamingError(c, status, code, message, streamStarted)
		return
	}

	// Group features such as web search emulation read this ID.
	parsedReq.GroupID = apiKey.GroupID

	parsedReq.SessionContext = &requeststate.SessionContext{
		ClientIP:  clientip.GetClientIP(c),
		UserAgent: c.GetHeader("User-Agent"),
		APIKeyID:  apiKey.ID,
	}
	sessionHash := session.GenerateSessionHash(parsedReq, slog.Info)

	reqLog.Info("sticky.session_hash_generated",
		zap.String("session_hash", sessionHash),
		zap.String("metadata_user_id_raw", parsedReq.MetadataUserID),
	)

	// Dedicated endpoints can force a platform; ordinary requests select across platforms.
	platform := ""
	if forcePlatform, ok := h.backend.ForcedPlatform(c); ok {
		platform = forcePlatform
	} else if apiKey.Group != nil {
		platform = ""
	}
	sessionKey := sessionHash
	if platform == capability.PlatformGemini && sessionHash != "" {
		sessionKey = "gemini:" + sessionHash
	}
	if isolationSessionID := MetadataSessionID(parsedReq.MetadataUserID); isolationSessionID != "" {
		if err := h.backend.Isolate(c.Request.Context(), apiKey, subject.UserID, isolationSessionID); h.isolationError(c, err, streamStarted) {
			return
		}
	}

	var sessionBoundProviderID int64
	if sessionKey != "" {
		sessionBoundProviderID, _ = h.backend.CachedSession(c.Request.Context(), apiKey.GroupID, sessionKey)
		reqLog.Info("sticky.cache_lookup",
			zap.String("session_key", sessionKey),
			zap.Int64("bound_provider_id", sessionBoundProviderID),
		)
		if sessionBoundProviderID > 0 {
			prefetchedGroupID := int64(0)
			if apiKey.GroupID != nil {
				prefetchedGroupID = *apiKey.GroupID
			}
			h.backend.Prefetch(c, sessionBoundProviderID, prefetchedGroupID)
		}
	} else {
		reqLog.Info("sticky.no_session_key", zap.String("session_hash", sessionHash))
	}
	hasBoundSession := sessionKey != "" && sessionBoundProviderID > 0

	call := MessagesCall{Key: apiKey, Subject: subject, Subscription: subscription, Parsed: parsedReq, Body: body, Model: reqModel, Stream: reqStream, ClaudeCode: isClaudeCodeClient, Platform: platform, SessionKey: sessionKey, BoundProviderID: sessionBoundProviderID, HasBoundSession: hasBoundSession, StreamStarted: &streamStarted, Log: reqLog, Route: groupMappingRoutePlan, Mapping: groupMapping}
	kind := execution.TextMessages
	if platform == capability.PlatformGemini {
		attempt, err := h.backend.PrepareGemini(c.Request.Context(), call)
		if err != nil {
			reqLog.Warn("gateway.prepare_gemini_group_mapping_failed", zap.Error(err))
			h.streamingError(c, http.StatusBadRequest, "invalid_request_error", "Failed to apply group model mapping", streamStarted)
			return
		}
		call.GeminiBody = attempt.Body.Bytes()
		call.GeminiModel = attempt.Model
		kind = execution.TextGeminiMessages
	}
	request := execution.Request{
		Hints:   requeststate.ExecutionHintsFromContext(c.Request.Context()),
		Routing: requeststate.RoutingStateFromContext(c.Request.Context()),
		Route:   call.Route, UserID: subject.UserID, Concurrency: subject.Concurrency, Stream: reqStream, Body: body, Model: reqModel,
		Funding: execution.FundingState{Key: apiKey, Subscription: subscription}, SessionHash: sessionKey,
		Metadata: execution.RequestMetadata{ClaudeCode: isClaudeCodeClient},
		Text:     execution.TextState{Kind: kind, Parsed: parsedReq, Platform: platform, BoundProviderID: sessionBoundProviderID, HasBoundSession: hasBoundSession, GeminiBody: call.GeminiBody, GeminiModel: call.GeminiModel},
	}
	output := &MessagesOutput{ResponseSink: ResponseSink{Writer: c.Writer}, HTTP: c, Log: reqLog, StreamStarted: &streamStarted}
	_, _ = h.executor.Execute(c.Request.Context(), request, output)
}

func (h *MessagesHandler) errorResponse(c *gin.Context, status int, kind, message string) {
	WriteAnthropicError(c, status, kind, "", message)
}

func (h *MessagesHandler) streamingError(c *gin.Context, status int, kind, message string, started bool) {
	WriteAnthropicStreamError(c, status, kind, "", message, started, h.backend.MarkStream)
}

func (h *MessagesHandler) concurrencyError(c *gin.Context, err error, slot string, started bool) {
	status, kind, code, message := ConcurrencyErrorResponse(err, slot)
	WriteAnthropicStreamError(c, status, kind, code, message, started, h.backend.MarkStream)
}

func (h *MessagesHandler) isolationError(c *gin.Context, err error, started bool) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, session.ErrSessionIsolationConflict) {
		h.streamingError(c, http.StatusForbidden, "permission_error", session.SessionIsolationConflictMessage, started)
	} else {
		h.streamingError(c, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable", started)
	}
	return true
}

func (h *MessagesHandler) checkClientVersion(c *gin.Context, detected ClientDetection) bool {
	if !detected.ClaudeCode || strings.HasSuffix(c.Request.URL.Path, "/count_tokens") {
		return true
	}
	min, max := h.backend.ClientVersionBounds(c.Request.Context())
	if min == "" && max == "" {
		return true
	}
	message := clientmeta.ClaudeVersionRejection(h.backend.ClientVersion(c), min, max)
	if message == "" {
		return true
	}
	h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", message)
	return false
}

// SubscriptionFromContext reads the subscription stored in the HTTP context.
func SubscriptionFromContext(c *gin.Context) (*billing.UserSubscription, bool) {
	value, exists := c.Get("subscription")
	if !exists {
		return nil, false
	}
	sub, ok := value.(*billing.UserSubscription)
	return sub, ok
}

func MetadataSessionID(raw string) string {
	parsed := anthropic.ParseMetadataUserID(raw)
	if parsed == nil {
		return ""
	}
	return strings.TrimSpace(parsed.SessionID)
}

func ModerationHTTPStatus(decision *moderation.Decision) int {
	if decision == nil || decision.StatusCode < 400 || decision.StatusCode > 599 {
		return http.StatusForbidden
	}
	return decision.StatusCode
}
