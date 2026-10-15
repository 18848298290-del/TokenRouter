package httpapi

import (
	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// reasoning_effort_usage.go retains the client's requested reasoning effort
// for completion records across provider request rewrites. Provider policy can
// change the effort sent upstream, so binding reads canonical effort from the
// supplied body before that transformation and stores recognized values on the
// request context.
//
// OpenAI and Messages completion stamping fill the requested-effort field only
// when the result has not already supplied it. The result's existing value
// takes precedence over context evidence. Missing requests, absent canonical
// effort and nil results leave the available state unchanged. The model
// argument does not affect binding; the body determines which preference is
// retained.

// BindRequestedReasoningEffort stores canonical client effort before provider
// policy rewrites. Missing requests or unrecognized effort leave context
// unchanged; the model argument is unused, so only the supplied body determines
// the recorded value.
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

// StampOpenAIRequestedReasoningEffort fills an absent result effort from the
// request context. Existing result values take precedence; nil results or
// missing requests are ignored so completion capture retains any effort already
// set by execution.
func StampOpenAIRequestedReasoningEffort(result *forwardcore.OpenAIResult, c *gin.Context) {
	if result == nil || result.RequestedReasoningEffort != nil || c == nil || c.Request == nil {
		return
	}
	result.RequestedReasoningEffort = requeststate.RequestedReasoningEffortFromContext(c.Request.Context())
}

// StampForwardRequestedReasoningEffort fills an absent compatible result effort
// from request context. A populated result wins over that context, and nil
// results or missing requests leave the result untouched.
func StampForwardRequestedReasoningEffort(result *forwardcore.MessagesResult, c *gin.Context) {
	if result == nil || result.RequestedReasoningEffort != nil || c == nil || c.Request == nil {
		return
	}
	result.RequestedReasoningEffort = requeststate.RequestedReasoningEffortFromContext(c.Request.Context())
}
