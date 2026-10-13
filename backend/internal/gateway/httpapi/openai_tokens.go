package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/text"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Token-count HTTP entries read authenticated routing and funding state before
// opening dedicated count attempts. The backend interface supplies synchronous
// observation and selection adapters; the handler keeps generation completion
// out of its available operations and shares protocol-specific error output.

// OpenAITokenOptions defines request-body and provider-switch budgets for
// auxiliary counting.
type OpenAITokenOptions struct {
	MaxBodyBytes int64 // maximum inbound body bytes
	// MaxSwitches bounds provider changes in Responses preflight. A
	// nonpositive allowance still permits the initial selection and its
	// same-provider retry budget.
	MaxSwitches int
}

// OpenAITokenBackend supplies request readers and counting adapters for HTTP
// token entries.
type OpenAITokenBackend interface {
	// Access returns the effective authenticated API key and whether it is
	// available. Implementations return request-owned key data so routing
	// and count execution can retain it independently of authentication
	// cache entries.
	Access(*gin.Context) (*apikey.APIKey, bool)
	// Dependencies reports whether the token entry has its required startup
	// services. A false result includes dependency diagnostics and writes
	// an unavailable response when the HTTP writer can still accept an
	// error.
	Dependencies(*gin.Context, *zap.Logger) bool
	// AllowsMessages reports whether the effective key permits the Messages
	// counting protocol. Missing key or group data leaves this adapter
	// check permissive; authentication and request-subject validation
	// remain the entry handler's earlier responsibility.
	AllowsMessages(*apikey.APIKey) bool
	// PolicyDenied records a local protocol-policy limitation for request
	// observations.
	PolicyDenied(*gin.Context)
	// ObserveRequest records the model and stream identity for request
	// observations.
	ObserveRequest(*gin.Context, string, bool)
	// ObserveEndpoint records the auxiliary endpoint's non-generating
	// request type for observations.
	ObserveEndpoint(*gin.Context, bool)
	// Plan resolves the effective key and requested model into a request
	// route. The result carries the group mapping consumed by counting;
	// BindPlan publishes that plan to the request context before provider
	// selection.
	Plan(context.Context, *apikey.APIKey, string) routing.RoutePlan
	// BindPlan publishes the supplied route plan on the HTTP request
	// context. Later provider selection and model tracing read that
	// context; callers bind the plan belonging to the same effective key.
	BindPlan(*gin.Context, routing.RoutePlan)
	// MessageProviderModel supplies the provider-layer model after group
	// mapping for Messages counting. Its result is used for selection and
	// forwarding, while the original requested model remains available for
	// client-facing errors and observations.
	MessageProviderModel(context.Context, *apikey.APIKey, string) string
	// MappedBodyCache returns a request-local reader for bodies with an
	// optional mapped model. Returned bytes can reuse the original or
	// cached body storage. Callers treat that storage as immutable while
	// the cache remains in use.
	MappedBodyCache([]byte) func(bool, string) []byte
	// Eligibility checks current funding admission for the effective key
	// and optional subscription. A failure is formatted by the entry
	// handler before selection starts. Counting uses this admission read
	// independently of any generation settlement.
	Eligibility(context.Context, *apikey.APIKey, *billing.UserSubscription) error
	// Platform supplies an optional platform filter for the counting
	// selector.
	Platform(*apikey.APIKey) string
	// SessionHash derives the sticky selection identity from the counting
	// request body. The session input identifies the requested purpose;
	// concrete token adapters may use the common token-session hash for
	// every supplied purpose.
	SessionHash(*gin.Context, OpenAISessionInput, []byte) string
	// AuthLatency records elapsed authentication and funding preparation
	// time in milliseconds.
	AuthLatency(*gin.Context, int64)
	// CountExecution creates request-local operations for one selection and
	// Messages-compatible count. The caller passes funding-approved routing
	// inputs. The returned operations own their count output and do not
	// submit generation completion work.
	CountExecution(*gin.Context, OpenAICountCall) text.SingleCountPorts
	// InputTokensExecution creates request-local operations for the
	// Responses counting retry loop. Each forward releases any returned
	// selection resource before it completes. The loop uses the selected
	// provider's retry allowance and handler switch budget.
	InputTokensExecution(*gin.Context, InputTokensCall) text.InputTokensPorts
}

// OpenAITokensHandler serves auxiliary token counts with protocol-specific HTTP
// errors.
type OpenAITokensHandler struct {
	RequestLifetime                    // shared request activity admission
	options         OpenAITokenOptions // auxiliary request-counting budgets
	backend         OpenAITokenBackend // app-bound token entry operations
	prompt          MessagesPrompt     // Messages prompt-replacement capability
}

// NewOpenAITokensHandler binds auxiliary counting budgets, backend operations,
// and prompt replacement. Dependencies remain shared references supplied by app
// assembly. The constructor performs no readiness checks; handlers invoke
// backend dependency validation when required.
func NewOpenAITokensHandler(options OpenAITokenOptions, backend OpenAITokenBackend, prompt MessagesPrompt) *OpenAITokensHandler {
	return &OpenAITokensHandler{options: options, backend: backend, prompt: prompt}
}

// errorResponse writes an OpenAI token-entry error through the shared request
// output.
func (h *OpenAITokensHandler) errorResponse(c *gin.Context, status int, kind, message string) {
	writeOpenAITokenError(c, status, kind, message)
}

// anthropicErrorResponse writes a Messages-compatible token-entry error using
// Anthropic framing.
func (h *OpenAITokensHandler) anthropicErrorResponse(c *gin.Context, status int, kind, message string) {
	WriteAnthropicError(c, status, kind, "", message)
}

// writeOpenAITokenError writes token-entry errors with request correlation and
// Compact output handling. A committed Compact keepalive uses the shared
// streaming-aware writer and Ops error marking; callers supply the desired
// status, error kind, and safe client message.
func writeOpenAITokenError(c *gin.Context, status int, kind, message string) {
	writeOpenAIRequestError(c, status, kind, message, StopOpenAICompactSSEKeepaliveCommitted, func(c *gin.Context, k, m string, s int) { MarkOpsStreamError(c, k, m, s) }, func(c *gin.Context) (string, string) { return ErrorRequestID(c), ErrorRequestModel(c) })
}
