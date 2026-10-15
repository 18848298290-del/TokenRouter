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

// openai_protocol_execution_messages.go dispatches Messages execution through
// the selected provider's protocol runner.
// Messages input may reach a native Anthropic endpoint, a Chat endpoint, or
// Responses conversion under the selected provider policy. A new adapter
// carries this attempt through the shared runner, and returned observations
// feed request model capture before the result and error reach the caller.

// Messages executes one selected provider attempt using the Messages runner.
// The HTTP context and bound services remain live through synchronous output
// and model capture; callers inspect both returned observation and error
// before deciding completion or another provider.
func (s *OpenAITextExecutor) Messages(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte, promptCacheKey, defaultMappedModel string, tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult) (*forwardcore.OpenAIResult, error) {
	result, err := openaiexecution.RunMessages(ctx, body, promptCacheKey, defaultMappedModel, &openAIMessagesExecutionAdapter{s: s, c: c, provider: provider, tls: tlsRouterMatch})
	out := openaiexecution.ToForwardResult(result)
	captureResponseModel(c, out)
	return out, err
}

// messagesError handles an upstream HTTP rejection using Anthropic envelope
// and body writers. The caller owns response closure and may supply a
// scheduling model; CompatError can return a failover signal before final
// client error output.
func (s *OpenAITextExecutor) messagesError(
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	requestedModel ...string,
) (*forwardcore.OpenAIResult, error) {
	return s.Output.CompatError(resp, c, provider, WriteForwardAnthropicError, WriteForwardAnthropicErrorBody, requestedModel...)
}
