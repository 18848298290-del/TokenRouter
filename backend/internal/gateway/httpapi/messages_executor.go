package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/messageforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// MessagesExecutor creates an HTTP output adapter for each runtime call.
type MessagesExecutor struct {
	runtime *messageforward.Runtime
	filter  *egress.CompiledHeaderFilter
}

func NewMessagesExecutor(runtime *messageforward.Runtime, filter *egress.CompiledHeaderFilter) *MessagesExecutor {
	return &MessagesExecutor{runtime: runtime, filter: filter}
}

func (e *MessagesExecutor) ApplyBedrockCCCompat(c *gin.Context, body []byte, model string, target *gatewayadapter.ExecutionProvider, groupID *int64) []byte {
	return e.runtime.PrepareBedrockCompatibility(c.Request.Context(), c.Request.Header, body, model, target, groupID)
}

func (e *MessagesExecutor) Forward(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, parsed *requeststate.ParsedRequest) (*forward.MessagesResult, error) {
	return e.runtime.Execute(ctx, NewMessageForwardBoundary(c, e.filter), target, parsed)
}

func (e *MessagesExecutor) ForwardCountTokens(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, parsed *requeststate.ParsedRequest) error {
	return e.runtime.Count(ctx, NewMessageForwardBoundary(c, e.filter), target, parsed)
}

func (e *MessagesExecutor) ForwardAsChatCompletions(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte, _ *requeststate.ParsedRequest) (*forward.MessagesResult, error) {
	return e.runtime.Chat(ctx, NewMessageForwardBoundary(c, e.filter), target, body)
}

func (e *MessagesExecutor) ForwardAsResponses(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte, _ *requeststate.ParsedRequest) (*forward.MessagesResult, error) {
	return e.runtime.Responses(ctx, NewMessageForwardBoundary(c, e.filter), target, body)
}
