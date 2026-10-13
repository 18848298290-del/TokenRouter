package httpapi

import (
	"crypto/rand"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Raw Chat output keeps native Chat payloads while applying provider-specific
// response repairs and request observations. The response reader invokes bound
// callbacks for missing Grok usage, truncation and reasoning caching, so those
// decisions use the same request and upstream identifiers as the forwarded
// response.

// RawOptions binds raw Chat reading to the active HTTP request and response.
// Callers retain c, resp and the selected tier until reading finishes and own
// response closure; a nil provider leaves ProviderID zero while retaining the
// bound callbacks.
func (p *OpenAIResponseOutput) RawOptions(c *gin.Context, resp *http.Response, provider *gatewayprovider.ExecutionProvider, billingModel, upstreamModel string, serviceTier *string, writeError func(*gin.Context, int, string, string)) openai.RawResponseOptions {
	options := openai.RawResponseOptions{
		Runtime: bridge.Runtime{Now: time.Now, ReadRandom: rand.Read},
		Scanner: p.Scanner,
		CC:      func() openai.CCResponseOptions { return p.CCOptions(c, writeError) },
		ReadBody: func(r io.Reader) ([]byte, error) {
			return ReadUpstreamResponseBody(r, p.Options.ReadLimit, c, OpenAIResponseTooLarge)
		},
		BodyLimitError: httpclient.ErrResponseBodyTooLarge,
		Headers: func(dst, src http.Header) {
			if p.Headers != nil {
				egressadapter.WriteFilteredHeaders(dst, src, p.Headers)
			}
		},
		WriteError: func(status int, kind, message string) { writeError(c, status, kind, message) },
		Observe:    func(body []byte, event string) { ObserveOpenAIServiceTierInContext(c, body, event) },
		ObserveSSE: func(body string) { ObserveOpenAISSEBody(c, body) },
		TransformLine: func(line string) string {
			// The provider gate restricts Ollama repairs to recognized API-key Chat
			// targets; other providers retain the framing supplied by their upstream.
			return gatewayprovider.ApplyOllamaCloudRawChatCompletionsSSELine(provider, line)
		},
		TransformBody: func(body []byte) []byte {
			// Native JSON repairs use the same Ollama provider gate as line repairs,
			// after the reader has already observed usage from the upstream bytes.
			return gatewayprovider.ApplyOllamaCloudRawChatCompletionsResponse(provider, body)
		},
		MissingUsage: func(model string, usage wire.ForwardUsage) error {
			// The reader supplies its observed model and usage; eligible Grok billing
			// models turn missing usage into a failure even when payload forwarding succeeds.
			if gatewayprovider.RequiresBillableGrokChatUsage(provider, billingModel, upstreamModel, model) && !gatewayprovider.HasBillableGrokChatUsage(usage) {
				return NewGrokMissingUsageFailure(c, provider, requeststate.FirstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")))
			}
			return nil
		},
		ServiceTier:          func() string { return ObservedUpstreamResponseServiceTier(c) },
		ResolvedServiceTier:  func() *string { return ResolvedOpenAIUpstreamServiceTier(c, serviceTier) },
		NormalizeServiceTier: forwardcore.NormalizeObservedOpenAIServiceTier,
		TruncatedFailover: func(err error) error {
			return NewOpenAIRawTruncationFailure(c, provider, resp.Header.Get("x-request-id"), err)
		},
		RecordTruncation: func(err error) {
			RecordOpenAIRawTruncation(c, provider, resp.Header.Get("x-request-id"), err, "http_error")
		},
		SilentRefusal: func() error {
			return NewOpenAISilentRefusalFailoverError(c, ExecutionErrorProvider(provider), resp.Header.Get("x-request-id"))
		},
		CacheOutput: p.Reasoning.FromOutput,
		CacheEvents: p.Reasoning.FromEvents,
	}
	if provider != nil {
		options.ProviderID = provider.Record.ID
	}
	return options
}
