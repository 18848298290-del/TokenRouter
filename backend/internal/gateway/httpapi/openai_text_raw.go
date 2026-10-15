package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	openaiexecution "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
)

// openai_text_raw.go dispatches native and converted requests to Chat-only
// upstream runners.
// Chat-only upstream routes can serve native Chat input or converted Messages
// and Responses requests. The client protocol kind selects the runner and
// error writer, while returned observations are captured in HTTP request state
// before the result and any failure are handed back.

// RawChat executes native Chat Completions against the selected provider's
// resolved Chat URL. The runner maps models, applies provider compatibility,
// observes SSE or JSON usage and owns response closure; callers retain request
// state until model capture finishes.
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

// MessagesViaRawChat converts Messages input for a selected Chat endpoint and
// returns Anthropic output. A fresh adapter keeps this call's provider and TLS
// selection; the runner owns upstream cleanup, and result observations can
// accompany an error.
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

// ResponsesViaRawChat converts Responses input for a selected Chat-only
// upstream route. Its fresh adapter uses Responses error output and shared
// reasoning history; callers retain c during synchronous output and inspect
// the returned result alongside its error.
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
