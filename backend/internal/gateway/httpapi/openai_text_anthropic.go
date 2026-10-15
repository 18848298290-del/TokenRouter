package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	openaiexecution "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
)

// openai_text_anthropic.go dispatches Messages, Chat and Responses requests to
// native Anthropic execution.
// A selected Anthropic-compatible upstream can serve Messages directly or
// receive requests converted from Chat or Responses. These HTTP entry facades
// create adapters with the corresponding client protocol kind so the shared
// native runner chooses the right conversion and error output.

// NativeMessages executes one selected provider through its native Anthropic
// endpoint. The native runner maps the model, cleans the body and owns the
// response lifetime; callers retain the HTTP context until its synchronous
// output and usage observation finish.
func (s *OpenAITextExecutor) NativeMessages(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	defaultMappedModel string,
) (*forwardcore.OpenAIResult, error) {
	adapter := &openAINativeAnthropicAdapter{openAIMessagesExecutionAdapter: &openAIMessagesExecutionAdapter{s: s, c: c, provider: provider}, kind: openaiexecution.NativeMessages}
	result, err := openaiexecution.ForwardNativeMessages(ctx, body, defaultMappedModel, adapter)
	return openaiexecution.ToForwardResult(result), err
}

// NativeChat executes Chat input through the selected native Anthropic
// endpoint. The runner converts Chat through Responses into Anthropic and
// converts returned events back into Chat output; callers inspect the returned
// result and error for completion eligibility.
func (s *OpenAITextExecutor) NativeChat(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	defaultMappedModel string,
) (*forwardcore.OpenAIResult, error) {
	adapter := &openAINativeAnthropicAdapter{openAIMessagesExecutionAdapter: &openAIMessagesExecutionAdapter{s: s, c: c, provider: provider}, kind: openaiexecution.NativeChat}
	result, err := openaiexecution.ForwardNativeChat(ctx, body, defaultMappedModel, adapter)
	return openaiexecution.ToForwardResult(result), err
}

// NativeResponses executes Responses input through the selected native
// Anthropic endpoint. The runner converts the request and Anthropic response
// events for the Responses client, owns the upstream body lifetime, and may
// return observed usage with a failure.
func (s *OpenAITextExecutor) NativeResponses(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	defaultMappedModel string,
) (*forwardcore.OpenAIResult, error) {
	adapter := &openAINativeAnthropicAdapter{openAIMessagesExecutionAdapter: &openAIMessagesExecutionAdapter{s: s, c: c, provider: provider}, kind: openaiexecution.NativeResponses}
	result, err := openaiexecution.ForwardNativeResponses(ctx, body, defaultMappedModel, adapter)
	return openaiexecution.ToForwardResult(result), err
}
