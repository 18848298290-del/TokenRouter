package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	openaiexecution "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
)

// NativeMessages forwards Anthropic Messages to a Chinese provider native Anthropic endpoint.
// It applies model mapping and body cleanup for empty text and web-search history blocks
// while retaining the Anthropic protocol.
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

// NativeChat accepts OpenAI Chat Completions through
// a Chinese provider native Anthropic endpoint.
//
// Conversion path:
//
//	Request: Chat Completions to Responses to Anthropic.
//	Response: Anthropic events to Responses events to Chat chunks.
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

// NativeResponses accepts OpenAI Responses through
// a Chinese provider native Anthropic endpoint.
//
// Conversion path:
//
//	Request: Responses to Anthropic.
//	Response: Anthropic events to Responses events.
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
