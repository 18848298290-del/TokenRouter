package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	openaiexecution "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
)

// Messages executes a Messages request and handles upstream recovery.
func (s *OpenAITextExecutor) Messages(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte, promptCacheKey, defaultMappedModel string, tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult) (*forwardcore.OpenAIResult, error) {
	result, err := openaiexecution.RunMessages(ctx, body, promptCacheKey, defaultMappedModel, &openAIMessagesExecutionAdapter{s: s, c: c, provider: provider, tls: tlsRouterMatch})
	out := openaiexecution.ToForwardResult(result)
	captureResponseModel(c, out)
	return out, err
}

// messagesError reads an upstream error and returns it in
// Anthropic error format.
func (s *OpenAITextExecutor) messagesError(
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	requestedModel ...string,
) (*forwardcore.OpenAIResult, error) {
	return s.Output.CompatError(resp, c, provider, WriteForwardAnthropicError, WriteForwardAnthropicErrorBody, requestedModel...)
}
