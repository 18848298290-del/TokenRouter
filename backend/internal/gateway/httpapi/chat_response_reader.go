package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// chat_response_reader.go binds HTTP output to buffered and streaming
// Responses-to-Chat conversion. Responses upstream events can serve a Chat
// client that requests either JSON or SSE output. Gateway output binds
// request-specific error and observation callbacks to the upstream converter,
// then adds the selected billing model to its usage result for completion
// processing.

// ChatBuffered collects a Responses terminal event and writes converted Chat JSON.
// The caller supplies a live response and request context and owns response-body
// cleanup. Errors retain the upstream reader's classification; a nil upstream
// result stays nil while successful results receive billingModel.
func (p *OpenAIResponseOutput) ChatBuffered(
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	originalModel string,
	billingModel string,
	upstreamModel string,
	startTime time.Time,
) (*forwardcore.OpenAIResult, error) {
	result, err := openai.ReadChatBuffered(resp, upstream.NewDeferredOutputContext(NewResponseSink(c)), p.ChatOptions(c, provider, resp, originalModel, billingModel, upstreamModel), originalModel, upstreamModel, startTime)
	return gatewayprovider.ChatForwardResult(result, billingModel), err
}

// ChatStreaming writes Responses events as Chat SSE and returns observed usage.
// requestBodyLen informs silent-refusal detection, while startTime anchors timing
// measurements. The caller owns response-body cleanup and considers any returned
// partial result alongside the error before deciding whether another attempt is safe.
func (p *OpenAIResponseOutput) ChatStreaming(
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	originalModel string,
	billingModel string,
	upstreamModel string,
	startTime time.Time,
	requestBodyLen int,
) (*forwardcore.OpenAIResult, error) {
	result, err := openai.ReadChatStreaming(resp, upstream.NewDeferredOutputContext(NewResponseSink(c)), p.ChatOptions(c, provider, resp, originalModel, billingModel, upstreamModel), originalModel, upstreamModel, startTime, requestBodyLen)
	return gatewayprovider.ChatForwardResult(result, billingModel), err
}
