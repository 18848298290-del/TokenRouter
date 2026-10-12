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

type OpenAITokenOptions struct {
	MaxBodyBytes int64
	MaxSwitches  int
}

// OpenAITokenBackend supplies readers, eligibility checks, and single-attempt token counting.
type OpenAITokenBackend interface {
	Access(*gin.Context) (*apikey.APIKey, bool)
	Dependencies(*gin.Context, *zap.Logger) bool
	AllowsMessages(*apikey.APIKey) bool
	PolicyDenied(*gin.Context)
	ObserveRequest(*gin.Context, string, bool)
	ObserveEndpoint(*gin.Context, bool)
	Plan(context.Context, *apikey.APIKey, string) routing.RoutePlan
	BindPlan(*gin.Context, routing.RoutePlan)
	MessageProviderModel(context.Context, *apikey.APIKey, string) string
	MappedBodyCache([]byte) func(bool, string) []byte
	Eligibility(context.Context, *apikey.APIKey, *billing.UserSubscription) error
	Platform(*apikey.APIKey) string
	SessionHash(*gin.Context, OpenAISessionInput, []byte) string
	AuthLatency(*gin.Context, int64)
	CountExecution(*gin.Context, OpenAICountCall) text.SingleCountPorts
	InputTokensExecution(*gin.Context, InputTokensCall) text.InputTokensPorts
}
type OpenAITokensHandler struct {
	RequestLifetime
	options OpenAITokenOptions
	backend OpenAITokenBackend
	prompt  MessagesPrompt
}

func NewOpenAITokensHandler(options OpenAITokenOptions, backend OpenAITokenBackend, prompt MessagesPrompt) *OpenAITokensHandler {
	return &OpenAITokensHandler{options: options, backend: backend, prompt: prompt}
}

// errorResponse uses shared output handling for committed Compact responses.
func (h *OpenAITokensHandler) errorResponse(c *gin.Context, status int, kind, message string) {
	writeOpenAITokenError(c, status, kind, message)
}

func (h *OpenAITokensHandler) anthropicErrorResponse(c *gin.Context, status int, kind, message string) {
	WriteAnthropicError(c, status, kind, "", message)
}

// writeOpenAITokenError uses the same HTTP error output as text entries.
func writeOpenAITokenError(c *gin.Context, status int, kind, message string) {
	writeOpenAIRequestError(c, status, kind, message, StopOpenAICompactSSEKeepaliveCommitted, func(c *gin.Context, k, m string, s int) { MarkOpsStreamError(c, k, m, s) }, func(c *gin.Context) (string, string) { return ErrorRequestID(c), ErrorRequestModel(c) })
}
