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

// messages_executor.go creates synchronous HTTP output adapters for calls to
// the shared Messages forwarding runtime.
// Anthropic, Bedrock compatibility, Count Tokens, Chat and
// Responses entry adapters share the bound runtime and response header filter,
// keeping platform attempts in that runtime while HTTP writers remain
// request-local.

// MessagesExecutor creates an HTTP output adapter for each runtime call.
type MessagesExecutor struct {
	runtime *messageforward.Runtime // shared Messages forwarding runtime
	// filter is shared across request-local output adapters. Nil selects shared
	// header filtering defaults, with passthrough defaults handled by MessageForwardBoundary.
	filter *egress.CompiledHeaderFilter
}

// NewMessagesExecutor retains the shared forwarding runtime and response
// header filter. It allocates no request state; callers supply a runtime
// before forwarding, and each method creates output for its active Gin
// request.
func NewMessagesExecutor(runtime *messageforward.Runtime, filter *egress.CompiledHeaderFilter) *MessagesExecutor {
	return &MessagesExecutor{runtime: runtime, filter: filter}
}

// ApplyBedrockCCCompat delegates Bedrock client compatibility using the
// current request context and live headers. It returns the prepared body;
// provider/model/group data select the runtime's applicable compatibility
// behavior for this attempt.
func (e *MessagesExecutor) ApplyBedrockCCCompat(c *gin.Context, body []byte, model string, target *gatewayadapter.ExecutionProvider, groupID *int64) []byte {
	return e.runtime.PrepareBedrockCompatibility(c.Request.Context(), c.Request.Header, body, model, target, groupID)
}

// Forward executes the selected provider with parsed Messages input and a new
// HTTP output adapter. The runtime owns platform preparation and response-body
// lifetime; outer orchestration interprets the returned result and error.
func (e *MessagesExecutor) Forward(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, parsed *requeststate.ParsedRequest) (*forward.MessagesResult, error) {
	return e.runtime.Execute(ctx, NewMessageForwardBoundary(c, e.filter), target, parsed)
}

// ForwardCountTokens delegates selected-provider counting with parsed input
// and request-local HTTP output. It returns the runtime error after response
// handling; counting does not create a normal text completion in this adapter.
func (e *MessagesExecutor) ForwardCountTokens(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, parsed *requeststate.ParsedRequest) error {
	return e.runtime.Count(ctx, NewMessageForwardBoundary(c, e.filter), target, parsed)
}

// ForwardAsChatCompletions delegates Chat conversion and output for the
// selected provider using body bytes. Its parsed-request argument is unused
// because runtime.Chat performs its own preparation for the client protocol.
func (e *MessagesExecutor) ForwardAsChatCompletions(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte, _ *requeststate.ParsedRequest) (*forward.MessagesResult, error) {
	return e.runtime.Chat(ctx, NewMessageForwardBoundary(c, e.filter), target, body)
}

// ForwardAsResponses delegates Responses conversion and output for the
// selected provider using body bytes. Its parsed-request argument is unused
// because runtime.Responses owns preparation and the attempt's conversion
// state.
func (e *MessagesExecutor) ForwardAsResponses(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte, _ *requeststate.ParsedRequest) (*forward.MessagesResult, error) {
	return e.runtime.Responses(ctx, NewMessageForwardBoundary(c, e.filter), target, body)
}
