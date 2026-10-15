package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// qoder_compatible.go admits compatible Qoder requests and manages their
// user-capacity lifetime.
// Qoder compatible Messages and Responses requests retain client body and
// model alongside the group-mapped attempt body. Funding admission runs before
// user-slot acquisition, and the runtime bridge later chooses the client wire
// protocol while completion retains the original input and route mapping. The
// manually assembled Chat handler uses the same admission path.
//
// Wait registration can fail without rejecting a request, but an explicitly
// full queue stops before slot polling. Only successfully counted waiters are
// released. Slot cleanup uses cancellation for buffered requests and explicit
// completion for streams, allowing streaming work to finish after client
// cancellation; the attempt bridge owns provider resources and the serial text
// loop decides recovery and switching.

// Qoder endpoint values select client envelopes and wire conversion for the
// shared compatible admission and forwarding path.
const (
	QoderChat      QoderEndpoint = "chat_completions" // OpenAI Chat client format
	QoderMessages  QoderEndpoint = "messages"         // Anthropic Messages client format
	QoderResponses QoderEndpoint = "responses"        // OpenAI Responses client format
)

// QoderEndpoint identifies the client protocol expected from compatible Qoder
// output.
type QoderEndpoint string

// QoderCompatibleCall captures admitted client identity, route and input for
// serial attempts.
type QoderCompatibleCall struct {
	Endpoint QoderEndpoint       // selected client protocol format
	Key      *apikey.APIKey      // effective authenticated request key
	Subject  authctx.AuthSubject // authenticated request subject identity
	// Subscription is optional funding state retained for completion; nil lets
	// the bound funding service apply its balance-funded admission path.
	Subscription *billing.UserSubscription // optional request funding subscription
	// Body and AttemptBody distinguish original client input from group-mapped
	// forwarding bytes. Completion hashes Body while the attempt bridge forwards
	// AttemptBody, retaining client model identity separately.
	// Body: retained original client input.
	// AttemptBody: group-mapped provider attempt input.
	Body, AttemptBody []byte
	// Model: original requested client model
	// SessionHash: Qoder sticky session identity
	Model, SessionHash string
	Stream             bool // validated client stream intent
	// StreamStarted is shared with slot waits and HTTP error output; callers
	// provide a nonnil pointer for the lifetime of synchronous execution.
	StreamStarted *bool
	Plan          routing.RoutePlan // captured group model route
	Log           *zap.Logger       // request-scoped diagnostic logger
}

// QoderCompatibleBackend supplies admission and serial execution operations
// for Qoder HTTP requests.
type QoderCompatibleBackend interface {
	// Enter joins the backend request barrier and returns its cleanup callback.
	// Callers invoke cleanup after an accepted request finishes; an error rejects
	// admission before authentication and request-body work.
	Enter() (func(), error)
	// Access returns the effective request Key and presence flag for admission.
	// The handler requires a nonnil usable Key when present and rejects absent
	// authentication before reading the client body.
	Access(*gin.Context) (*apikey.APIKey, bool)
	// BindErrors installs configured passthrough error rules for this request. An
	// unconfigured rule service may leave the context unchanged, allowing later
	// output to use the shared protocol defaults.
	BindErrors(*gin.Context)
	// PrepareClient installs protocol-specific client context from valid JSON
	// input. Messages detects Claude identity before provider work; other
	// endpoints can leave that client-specific state unchanged while retaining
	// ordinary request metadata.
	PrepareClient(*gin.Context, []byte, QoderEndpoint)
	// ObserveRequest stores the client model and streaming intent in Ops state.
	ObserveRequest(*gin.Context, string, bool)
	// ObserveEndpoint stores the normalized endpoint and request execution
	// category for Ops.
	ObserveEndpoint(*gin.Context, bool)
	// ObserveAuthLatency records elapsed request admission time in request-local
	// Ops observations.
	ObserveAuthLatency(*gin.Context, time.Duration)
	// Plan resolves group/model routing and installs the plan for execution. The
	// runtime can return a zero plan when execution is absent; configured
	// execution retains the original client identity for completion.
	Plan(*gin.Context, *apikey.APIKey, string) routing.RoutePlan
	// AttemptBody returns the provider attempt bytes selected by group mapping.
	// An unconfigured execution or unmapped plan retains the original body, so
	// callers keep that body separately for completion input.
	AttemptBody([]byte, routing.RoutePlan) []byte
	// Eligibility checks request funding using the Key and optional subscription.
	// An unconfigured funding service permits admission, while a returned error
	// stops the request before user-slot acquisition and provider selection.
	Eligibility(context.Context, *apikey.APIKey, *billing.UserSubscription) error
	// SessionHash derives Qoder sticky identity from explicit request seeds
	// before content fallback. Empty means no usable identity; callers retain
	// this result beside the client model for later provider selection and
	// binding.
	SessionHash(*gin.Context, QoderEndpoint, []byte, int64) string
	// Execution creates request-local serial attempt ports from captured
	// admission state. The caller keeps the Gin request and stream flag usable
	// through the loop, while completion inputs are captured before asynchronous
	// recording.
	Execution(*gin.Context, QoderCompatibleCall) textflow.QoderCompatiblePorts
	// ConcurrencyError presents user/provider admission failure in the chosen
	// client protocol. The supplied stream state determines whether plain JSON or
	// a streaming error can be written after a slot wait.
	ConcurrencyError(*gin.Context, error, string, bool, QoderEndpoint)
}

// QoderCompatibleHandler admits requests and delegates attempt recovery to the
// text loop.
type QoderCompatibleHandler struct {
	RequestLifetime // shared application request barrier

	backend QoderCompatibleBackend // shared compatible admission backend
	// concurrency is optional; a nil helper skips user queue registration and
	// slot acquisition while retaining the remaining admission and execution
	// flow.
	concurrency  *ConcurrencyHelper // optional user concurrency helper
	maxProviders int                // distinct provider attempt allowance
}

// NewQoderCompatibleHandler retains shared admission dependencies and the
// distinct-provider allowance. A nil concurrency helper skips user-slot
// handling; callers provide a usable backend, and the text loop still selects
// an initial provider for a nonpositive allowance.
func NewQoderCompatibleHandler(backend QoderCompatibleBackend, concurrency *ConcurrencyHelper, max int) *QoderCompatibleHandler {
	return &QoderCompatibleHandler{backend: backend, concurrency: concurrency, maxProviders: max}
}

// Messages runs compatible Qoder admission using the Anthropic Messages client
// format.
func (h *QoderCompatibleHandler) Messages(c *gin.Context) { h.handle(c, QoderMessages) }

// Responses runs compatible Qoder admission using the OpenAI Responses client
// format.
func (h *QoderCompatibleHandler) Responses(c *gin.Context) { h.handle(c, QoderResponses) }

// ChatCompletions runs the shared Qoder compatible path for manually assembled
// Chat handlers. Production native Chat admission uses QoderChatHandler with
// its Executor binding, so this entry still requires a configured compatible
// backend.
func (h *QoderCompatibleHandler) ChatCompletions(c *gin.Context) { h.handle(c, QoderChat) }

// errorResponse delegates a plain Qoder error using the chosen client
// protocol.
func (h *QoderCompatibleHandler) errorResponse(c *gin.Context, status int, kind, message string, endpoint QoderEndpoint) {
	WriteQoderError(c, status, kind, message, endpoint)
}

// handleConcurrencyError delegates slot failure presentation to the bound
// compatible backend. The endpoint and started flag preserve the request’s
// response format after queue waits that may have written keepalive output.
func (h *QoderCompatibleHandler) handleConcurrencyError(c *gin.Context, err error, kind string, started bool, endpoint QoderEndpoint) {
	h.backend.ConcurrencyError(c, err, kind, started, endpoint)
}

// qoderReleaseMode selects cleanup behavior from client stream intent.
// Streaming slots wait for explicit completion, while buffered slots also
// release on cancellation so interrupted synchronous requests do not retain
// those resources.
func qoderReleaseMode(stream bool) scheduler.ReleaseMode {
	if stream {
		return scheduler.ReleaseOnCompletion
	}
	return scheduler.ReleaseOnCancel
}

// handle admits a compatible Qoder request under both handler and backend
// request barriers. It requires authenticated Key and subject state, validates
// client model/stream fields, and captures routing plus original and mapped
// bodies before provider selection. Funding is checked before the user wait.
//
// An optional concurrency helper registers a waiter before slot acquisition.
// Counter errors permit waiting without a recorded entry; an explicit queue
// refusal stops immediately, and only counted entries are released. After
// acquiring the slot, cleanup uses the request’s stream mode and the serial
// text loop owns attempts, partial completion, refresh and switching.
func (h *QoderCompatibleHandler) handle(c *gin.Context, endpoint QoderEndpoint) {
	format := "openai"
	if endpoint == QoderMessages {
		format = "anthropic"
	}
	done, accepted := h.BeginRequest(c, format)
	if !accepted {
		return
	}
	defer done()

	{
		done, err := h.backend.Enter()
		if err != nil {
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Service is shutting down", endpoint)
			return
		}
		defer done()
	}

	streamStarted := false
	requestStart := time.Now()

	apiKey, ok := h.backend.Access(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key", endpoint)
		return
	}
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found", endpoint)
		return
	}
	h.backend.BindErrors(c)
	reqLog := RequestLogger(
		c,
		"handler.qoder_gateway."+string(endpoint),
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	body, err := ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := countMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", BodyTooLargeMessage(maxErr.Limit), endpoint)
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body", endpoint)
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty", endpoint)
		return
	}
	if !gjson.ValidBytes(body) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body", endpoint)
		return
	}
	h.backend.PrepareClient(c, body, endpoint)
	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || strings.TrimSpace(modelResult.String()) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required", endpoint)
		return
	}
	reqModel := strings.TrimSpace(modelResult.String())
	reqStream, ok := ParseOpenAICompatibleStream(body)
	if !ok {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", InvalidStreamFieldTypeMessage, endpoint)
		return
	}
	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))
	h.backend.ObserveRequest(c, reqModel, reqStream)
	h.backend.ObserveEndpoint(c, reqStream)
	plan := h.backend.Plan(c, apiKey, reqModel)
	forwardBody := h.backend.AttemptBody(body, plan)

	subscription, _ := SubscriptionFromContext(c)
	h.backend.ObserveAuthLatency(c, time.Since(requestStart))

	if err := h.backend.Eligibility(c.Request.Context(), apiKey, subscription); err != nil {
		reqLog.Info("qoder.billing_check_failed", zap.Error(err))
		status, code, message, retryAfter := BillingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message, endpoint)
		return
	}
	sessionHash := h.backend.SessionHash(c, endpoint, body, apiKey.ID)

	maxWait := scheduler.CalculateMaxWait(subject.Concurrency)
	waitCounted := false
	if h.concurrency != nil {
		waitEntry, err := h.concurrency.EnterUserWait(c.Request.Context(), subject.UserID, maxWait)
		canWait := waitEntry.Allowed
		// A counter-store error permits slot acquisition without a recorded waiter;
		// only an explicit full-queue result rejects this request before slot polling.
		if err != nil {
			reqLog.Warn("qoder.user_wait_counter_increment_failed", zap.Error(err))
		} else if !canWait {
			h.errorResponse(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", endpoint)
			return
		} else {
			waitCounted = true
		}
		// Track the counted entry until acquisition or request exit; this flag
		// prevents a failed registration or early release from decrementing it again.
		defer func() {
			if waitCounted {
				waitEntry.Release()
			}
		}()

		userRelease, err := h.concurrency.AcquireUserSlotWithWait(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted)
		if err != nil {
			reqLog.Warn("qoder.user_slot_acquire_failed", zap.Error(err))
			h.handleConcurrencyError(c, err, "user", streamStarted, endpoint)
			return
		}
		if waitCounted {
			waitEntry.Release()
			waitCounted = false
		}
		// Streams retain their slot until explicit execution completion, allowing
		// upstream draining after client cancellation; buffered requests also release on cancellation.
		userRelease = scheduler.WrapRelease(c.Request.Context(), qoderReleaseMode(reqStream), userRelease)
		if userRelease != nil {
			defer userRelease()
		}
	}

	call := QoderCompatibleCall{Endpoint: endpoint, Key: apiKey, Subject: subject, Subscription: subscription, Body: body, AttemptBody: forwardBody, Model: reqModel, Stream: reqStream, StreamStarted: &streamStarted, SessionHash: sessionHash, Plan: plan, Log: reqLog}
	textflow.RunQoderCompatible(h.backend.Execution(c, call), h.maxProviders)
}
