package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	textflow "github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// CountTokensBackend provides admission, observation, and count execution without generation concurrency slots.
type CountTokensBackend interface {
	Access(*gin.Context) (*apikey.APIKey, bool)
	CompatibilityMetrics(*zap.Logger)
	ObserveRequest(*gin.Context, string, bool)
	ObserveEndpoint(*gin.Context, bool)
	BindClient(*gin.Context, ClientDetection)
	BindThinking(*gin.Context, bool)
	Eligibility(context.Context, *apikey.APIKey, *billing.UserSubscription) error
	Execution(*gin.Context, *apikey.APIKey, *requeststate.ParsedRequest, string, *zap.Logger) textflow.CountPorts
	FailoverObservation(context.Context, string, map[string]any)
}
type CountTokensHandler struct {
	RequestLifetime

	maxBodyBytes int64
	maxSwitches  int
	backend      CountTokensBackend
	prompt       MessagesPrompt
}

func NewCountTokensHandler(limit int64, switches int, backend CountTokensBackend, prompt MessagesPrompt) *CountTokensHandler {
	return &CountTokensHandler{maxBodyBytes: limit, maxSwitches: switches, backend: backend, prompt: prompt}
}

func (h *CountTokensHandler) errorResponse(c *gin.Context, status int, kind, message string) {
	WriteAnthropicError(c, status, kind, "", message)
}

func countMaxBytesError(err error) (*http.MaxBytesError, bool) {
	var exceeded *http.MaxBytesError
	ok := errors.As(err, &exceeded)
	return exceeded, ok
}

// CountTokens handles POST /v1/messages/count_tokens.

// It checks subscription and balance eligibility without acquiring generation slots or recording usage.
func (h *CountTokensHandler) CountTokens(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "anthropic")
	if !accepted {
		return
	}
	defer done()

	apiKey, ok := h.backend.Access(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	_, ok = authctx.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := RequestLogger(
		c,
		"handler.gateway.count_tokens",
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	defer h.backend.CompatibilityMetrics(reqLog)

	body, err := ReadLenientJSONRequestBodyWithPrealloc(c.Request, h.maxBodyBytes)
	if err != nil {
		if maxErr, ok := countMaxBytesError(err); ok {
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

	// Apply prompt replacement before parsing so the session hash and upstream request use the same body.
	body = h.prompt.ApplyUserPromptReplacementToBody(c.Request.Context(), body, "anthropic_messages")

	bodyRef := requeststate.NewRequestBodyRef(body)
	parsedReq, err := requeststate.ParseGatewayRequest(bodyRef, capability.PlatformAnthropic)
	if err != nil {
		LogRequestBodyParseFailure(reqLog, body, err)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	body = parsedReq.Body.Bytes()
	// Reuse the parsed Messages body for strict Claude Code detection.
	h.backend.BindClient(c, DetectClaudeCodeRequest(c, body, parsedReq, false))
	apiKey, err = resolveClientGroupForRequest(c, h.backend, apiKey, protocol.ProtocolAnthropicMessages)
	if err != nil {
		writeClientGroupFallbackError(c, err, h.errorResponse)
		return
	}

	reqLog = reqLog.With(zap.String("model", parsedReq.Model), zap.Bool("stream", parsedReq.Stream))
	// Save thinking state for Antigravity model resolution and model-level rate limits.
	h.backend.BindThinking(c, parsedReq.ThinkingEnabled)

	if parsedReq.Model == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	h.backend.ObserveRequest(c, parsedReq.Model, parsedReq.Stream)
	h.backend.ObserveEndpoint(c, parsedReq.Stream)

	subscription, _ := SubscriptionFromContext(c)

	if err := h.backend.Eligibility(c.Request.Context(), apiKey, subscription); err != nil {
		status, code, message, retryAfter := BillingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	parsedReq.SessionContext = &requeststate.SessionContext{
		ClientIP:  clientip.GetClientIP(c),
		UserAgent: c.GetHeader("User-Agent"),
		APIKeyID:  apiKey.ID,
	}
	sessionHash := session.GenerateSessionHash(parsedReq, slog.Info)

	textflow.RunCountTokens(h.backend.Execution(c, apiKey, parsedReq, sessionHash, reqLog), h.maxSwitches, h.backend.FailoverObservation)
}
