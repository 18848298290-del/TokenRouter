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

// compatible_text.go prepares generic Responses and Chat requests for
// admission and synchronous text execution.
// OpenAI-shaped clients can enter the generic text executor while HTTP
// preparation shares Messages admission services. Prompt replacement and
// reasoning policy precede route planning and moderation. User-slot waiting
// follows moderation, and funding is checked again after waiting because the
// selected subscription or balance may have changed during admission.
//
// Responses can derive an image-intent context before user admission replaces
// the request context with a lease. Preparation carries that lease into the
// derived selection context before execution. Chat keeps its client model while
// checking the mapped model for image-only rejection, and separates a forced
// Gemini selection hash from the ordinary session hash retained in the call.

// CompatibleTextCall carries HTTP admission state into one synchronous text execution.
type CompatibleTextCall struct {
	MessagesCall // shared text admission data
	// RequestContext is the selection context. Responses carries any image hint
	// and acquired lease into it; Chat uses the current request context directly.
	RequestContext context.Context
	// ForwardBody holds the Responses image-preparation result, while Chat leaves
	// it nil. executeCompatible passes this slice as execution.Request.AttemptBody.
	ForwardBody []byte
	// RequestPlatform and SelectionSessionHash describe Chat selection. Forced
	// Gemini prefixes the selection hash while MessagesCall.SessionKey keeps the
	// unprefixed hash for execution data.
	// RequestPlatform identifies the forced provider platform.
	// SelectionSessionHash identifies the provider selection affinity key.
	RequestPlatform, SelectionSessionHash string
}

// CompatibleTextBackend supplies preparation, observation, and admission operations for generic text entries.
type CompatibleTextBackend interface {
	// Access supplies the key view used by subsequent HTTP preparation.
	// Successful callers need a nonnil key with authenticated ownership and group
	// data; a false flag produces the entry's authentication error before body handling.
	Access(*gin.Context) (*apikey.APIKey, bool)
	// ObserveRequest records model and streaming values for request diagnostics.
	// Preparation first uses an empty model and false stream, then publishes
	// parsed values before moderation so rejected requests retain their observed intent.
	ObserveRequest(*gin.Context, string, bool)
	// ObserveEndpoint records the selected streaming request type for HTTP diagnostics.
	ObserveEndpoint(*gin.Context, bool)
	// Reasoning applies the admitted group's reasoning policy to request bytes.
	// It returns prepared bytes and a change flag, or an error that the caller
	// reports as a local permission rejection before route planning.
	Reasoning(*gin.Context, *apikey.APIKey, []byte) ([]byte, bool, error)
	// PolicyDenied marks a local policy rejection for request observation consumers.
	PolicyDenied(*gin.Context)
	// Plan resolves the selected group's route and model mapping for this request.
	// Callers retain the client model separately, bind the returned plan, and
	// use its mapping for endpoint eligibility before executor selection.
	Plan(context.Context, *apikey.APIKey, string) routing.RoutePlan
	// BindPlan publishes the resolved route for later HTTP execution consumers.
	// It runs before moderation and execution, retaining the selected route in
	// request context for the executor even as preparation builds its other inputs.
	BindPlan(*gin.Context, routing.RoutePlan)
	// ImageIntent prepares Responses attempt bytes and reports whether image generation applies.
	// The returned bytes travel separately from the body used by moderation and
	// parsing. A true flag makes the caller derive image-intent selection context.
	ImageIntent(*apikey.APIKey, string, []byte, routing.GroupMappingResult) ([]byte, bool)
	// ImageContext adds image-generation intent to the supplied selection context.
	// Preparation can derive it before user-slot waiting, then carries the acquired
	// request lease into that context before the executor uses it.
	ImageContext(context.Context) context.Context
	// ChatImageModel identifies mapped image-generation models rejected by the Chat endpoint.
	ChatImageModel(string, routing.GroupMappingResult) bool
	// Moderate evaluates the prepared body using the entry's declared content protocol.
	// A nil decision allows preparation to continue. A blocked decision provides
	// the message and status classification for a local terminal response.
	Moderate(*gin.Context, *zap.Logger, *apikey.APIKey, authctx.AuthSubject, string, string, []byte) *moderation.Decision
	// BindErrors makes the configured error matcher available to downstream output adapters.
	BindErrors(*gin.Context)
	// AuthLatency records elapsed admission preparation time in milliseconds for observation.
	AuthLatency(*gin.Context, int64)
	// Eligibility checks current funding after the user slot has been obtained.
	// A failure stops before execution and is translated by BillingErrorDetails,
	// including any retry delay needed by the client.
	Eligibility(context.Context, *apikey.APIKey, *billing.UserSubscription) error
	// Isolate checks the supplied metadata session against the key and user scope.
	// The caller treats a session conflict as forbidden and other errors as
	// unavailable, stopping execution whenever isolation cannot be established.
	Isolate(context.Context, *apikey.APIKey, int64, string) error
	// MarkStream records intended terminal failure details after stream framing begins.
	MarkStream(*gin.Context, string, string, int)
	// FailoverObservation records the supplied executor event and its diagnostic attributes.
	FailoverObservation(context.Context, string, map[string]any)
}

// CompatibleTextHandler prepares Responses and Chat requests for the generic text executor.
type CompatibleTextHandler struct {
	executor execution.Executor // synchronous text execution dependency
	// RequestLifetime optionally gates requests against application shutdown. Its
	// release surrounds preparation and execution when assembly binds an activity barrier.
	RequestLifetime

	options     MessagesHTTPOptions   // request body limits
	backend     CompatibleTextBackend // admission and observation operations
	prompt      MessagesPrompt        // user prompt replacement
	concurrency *ConcurrencyHelper    // user slot admission
}

// NewCompatibleTextHandler binds preparation dependencies and the supplied executor.
// Assembly provides usable backend and prompt operations, plus concurrency for
// requests reaching user admission. Activity-barrier binding remains a separate
// assembly step before serving requests.
func NewCompatibleTextHandler(options MessagesHTTPOptions, backend CompatibleTextBackend, prompt MessagesPrompt, concurrency *ConcurrencyHelper, executor execution.Executor) *CompatibleTextHandler {
	return &CompatibleTextHandler{executor: executor, options: options, backend: backend, prompt: prompt, concurrency: concurrency}
}

// compatibleMaxBytesError extracts the HTTP body-size error from an error chain.
// The flag reports errors.As matching, including a typed nil pointer. Body
// readers normally supply a populated MaxBytesError whose limit shapes the rejection message.
func compatibleMaxBytesError(err error) (*http.MaxBytesError, bool) {
	var limit *http.MaxBytesError
	ok := errors.As(err, &limit)
	return limit, ok
}

// WriteCompatibleResponsesError writes code and message in the Responses error envelope.
func WriteCompatibleResponsesError(c *gin.Context, status int, kind, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": kind, "message": message}})
}

// WriteCompatibleChatError writes type and message in the Chat error envelope.
func WriteCompatibleChatError(c *gin.Context, status int, kind, message string) {
	c.JSON(status, gin.H{"error": gin.H{"type": kind, "message": message}})
}

// responsesErrorResponse sends a local preparation failure through the Responses error writer.
func (h *CompatibleTextHandler) responsesErrorResponse(c *gin.Context, status int, kind, message string) {
	WriteCompatibleResponsesError(c, status, kind, message)
}

// chatCompletionsErrorResponse sends a local preparation failure through the Chat error writer.
func (h *CompatibleTextHandler) chatCompletionsErrorResponse(c *gin.Context, status int, kind, message string) {
	WriteCompatibleChatError(c, status, kind, message)
}

// concurrencyError translates slot admission failure into protocol-aware terminal output.
// started selects stream output when waiting already committed framing; otherwise
// the writer uses an HTTP error. The backend records intended stream failure details when needed.
func (h *CompatibleTextHandler) concurrencyError(c *gin.Context, err error, slot string, started bool) {
	status, kind, code, message := ConcurrencyErrorResponse(err, slot)
	WriteAnthropicStreamError(c, status, kind, code, message, started, h.backend.MarkStream)
}

// Responses prepares authenticated Responses input and invokes generic text execution.
// Local validation, policy, and moderation errors return before user admission
// or executor work. Successful user admission arranges cancellation and deferred
// cleanup, and funding is rechecked after any wait.
//
// Image preparation can create a selection context before the slot acquisition
// updates the HTTP request context. The same acquired lease is attached to that
// derived context so selection and output retain the same admitted resource.
// Metadata-session isolation is checked before building the execution call,
// while the prepared moderation body and attempt body remain separate fields.
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

	// Apply group reasoning limits before planning and conversion. A local rejection
	// stops this request before it consumes a user slot or begins provider work.
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
	// Slot admission updated the HTTP context after image intent was captured.
	// Carry that lease into the selection context so downstream work sees the admitted request resource.
	if lease := scheduler.RequestLease(c.Request.Context()); lease != nil {
		requestCtx = scheduler.WithRequestLease(requestCtx, lease)
	}
	userReleaseFunc = scheduler.WrapRelease(c.Request.Context(), scheduler.ReleaseOnCancel, userReleaseFunc)
	// Cancellation can release the acquired slot while execution is progressing.
	// The wrapped callback also runs on handler return so every completed path relinquishes admission.
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// Funding can change while this request waits for a slot. Recheck the selected
	// key and subscription before session preparation or upstream execution starts.
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
	// Field parsing here supplies session inputs after entry validation already ran.
	// If it yields no parsed object, retain model, stream, and body for executor preparation.
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

// ChatCompletions prepares authenticated Chat input and invokes generic text execution.
// Group reasoning and mapped-image rejection precede moderation, user waiting,
// and the final funding check. Acquired user capacity is released on cancellation
// or when the handler returns.
//
// The client model remains the call's model while its route carries group mapping.
// Session identity comes from the prepared parsed body and request metadata.
// For a forced Gemini request, selection uses a prefixed affinity hash and the
// execution state receives a separate alternate budget, keeping that selection
// choice distinct from the ordinary session key retained for the call.
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

	// Apply group reasoning limits before planning and conversion. A local rejection
	// stops this request before it consumes a user slot or begins provider work.
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
	// Cancellation can release the acquired slot while execution is progressing.
	// The wrapped callback also runs on handler return so every completed path relinquishes admission.
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// Funding can change while this request waits for a slot. Recheck the selected
	// key and subscription before session preparation or upstream execution starts.
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
	// Field parsing here supplies session inputs after entry validation already ran.
	// If it yields no parsed object, retain model, stream, and body for executor preparation.
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
		// Forced Gemini selection uses its own affinity namespace. Retain the base
		// session hash for execution data while qualifying the key used to select a provider.
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

// executeCompatible submits the prepared call to the synchronous text executor.
// Call-owned body, parsed state, and stream marker remain available through
// execution. Assembly supplies an executor responsible for final response
// handling, and this wrapper discards its result and error after the call completes.
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
	// The bound executor decides terminal output and completion for this call.
	// Discard its result so another HTTP writer cannot repeat the response decision
	// after synchronous execution has finished.
	_, _ = h.executor.Execute(c.Request.Context(), request, output)
}
