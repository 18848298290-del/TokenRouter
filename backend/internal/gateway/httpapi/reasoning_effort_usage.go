package httpapi

import (
	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// BindRequestedReasoningEffort captures the client's effort before policy rewrites.
func BindRequestedReasoningEffort(c *gin.Context, body []byte, model string) {
	if c == nil || c.Request == nil {
		return
	}
	effort := requeststate.CanonicalRequestedReasoningEffort(body)
	if effort == nil {
		return
	}
	c.Request = c.Request.WithContext(requeststate.WithRequestedReasoningEffort(c.Request.Context(), *effort))
}

// StampOpenAIRequestedReasoningEffort fills an unset result effort from the request context.
func StampOpenAIRequestedReasoningEffort(result *forwardcore.OpenAIResult, c *gin.Context) {
	if result == nil || result.RequestedReasoningEffort != nil || c == nil || c.Request == nil {
		return
	}
	result.RequestedReasoningEffort = requeststate.RequestedReasoningEffortFromContext(c.Request.Context())
}

// StampForwardRequestedReasoningEffort fills an unset compatible result effort from the request context.
func StampForwardRequestedReasoningEffort(result *forwardcore.MessagesResult, c *gin.Context) {
	if result == nil || result.RequestedReasoningEffort != nil || c == nil || c.Request == nil {
		return
	}
	result.RequestedReasoningEffort = requeststate.RequestedReasoningEffortFromContext(c.Request.Context())
}
