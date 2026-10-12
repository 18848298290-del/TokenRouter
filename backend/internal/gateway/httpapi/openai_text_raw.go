package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	openaiexecution "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
)

// RawChat forwards Chat Completions to {base_url}/v1/chat/completions.
// It rewrites model IDs, forwards SSE or JSON with compatibility repairs, and extracts usage.
// This route serves API key providers without Responses support and CN providers fixed to chat_completions.
// OAuth request transforms and prompt_cache_key injection belong to the separate OAuth route.
func (s *OpenAITextExecutor) RawChat(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	defaultMappedModel string,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*forwardcore.OpenAIResult, error) {
	adapter := &openAIRawChatAdapter{openAIRawFallbackAdapter: &openAIRawFallbackAdapter{openAIMessagesExecutionAdapter: &openAIMessagesExecutionAdapter{s: s, c: c, provider: provider, tls: tlsRouterMatch}, kind: openaiexecution.NativeChat}}
	result, err := openaiexecution.RunRawChat(ctx, body, defaultMappedModel, adapter)
	out := openaiexecution.ToForwardResult(result)
	captureResponseModel(c, out)
	return out, err
}

// MessagesViaRawChat converts Messages to Chat Completions and returns Anthropic events or JSON.
// Its stream state machine handles the Messages client protocol separately from ResponsesViaRawChat.
func (s *OpenAITextExecutor) MessagesViaRawChat(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	defaultMappedModel string,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*forwardcore.OpenAIResult, error) {
	adapter := &openAIRawFallbackAdapter{openAIMessagesExecutionAdapter: &openAIMessagesExecutionAdapter{s: s, c: c, provider: provider, tls: tlsRouterMatch}, kind: openaiexecution.NativeMessages}
	result, err := openaiexecution.MessagesViaRawChat(ctx, body, defaultMappedModel, adapter)
	out := openaiexecution.ToForwardResult(result)
	captureResponseModel(c, out)
	return out, err
}

// ResponsesViaRawChat bridges Responses requests to
// upstreams that support only /v1/chat/completions.
func (s *OpenAITextExecutor) ResponsesViaRawChat(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*forwardcore.OpenAIResult, error) {
	adapter := &openAIRawFallbackAdapter{openAIMessagesExecutionAdapter: &openAIMessagesExecutionAdapter{s: s, c: c, provider: provider, tls: tlsRouterMatch}, kind: openaiexecution.NativeResponses}
	result, err := openaiexecution.ResponsesViaRawChat(ctx, body, adapter)
	out := openaiexecution.ToForwardResult(result)
	captureResponseModel(c, out)
	return out, err
}
