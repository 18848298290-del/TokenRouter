package httpapi

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	forward "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// Native Anthropic and converted OpenAI output share request-level header
// filtering and tool restoration. The option adapters supply synchronous HTTP
// callbacks while their platform readers own buffering, terminal detection and
// cancellation; selected provider observations remain tied to the retained
// execution target.

// AnthropicOptions binds conversion output to this HTTP request and supplied
// error writer. Line-size and header-filter callbacks read bound output
// settings when invoked; reverse-tool mapping reads current request state. The
// upstream converter receives clock and entropy functions and owns
// protocol conversion.
func (p *OpenAIResponseOutput) AnthropicOptions(c *gin.Context, writeError func(*gin.Context, int, string, string)) forward.AnthropicOutputOptions {
	return forward.AnthropicOutputOptions{
		Runtime: bridge.Runtime{Now: time.Now, ReadRandom: rand.Read},
		MaxLineSize: func() int {
			if p.Options.Configured && p.Options.MaxLineSize > 0 {
				return p.Options.MaxLineSize
			}
			return OpenAIResponseDefaultMaxLineSize
		},
		StreamInterval: p.TextStreamInterval,
		CopyHeaders: func(dst, src http.Header) {
			if p.Headers != nil {
				egressadapter.WriteFilteredHeaders(dst, src, p.Headers)
			}
		},
		ReverseTools: func(body []byte) []byte { return ReverseToolNamesIfPresent(c, body) },
		Error:        func(status int, kind, message string) { writeError(c, status, kind, message) },
		Warn:         func(msg string, fields ...zap.Field) { logging.L().Warn(msg, fields...) },
	}
}

// AnthropicDirectOptions binds native Anthropic reading to the selected
// provider and request. ProviderID is captured immediately, while observer
// callbacks inspect the retained provider at invocation. Bounded-body reads
// leave cleanup with their caller; interval callbacks interpret configured
// seconds, and header/tool callbacks use current bound output and request
// state.
func (p *OpenAIResponseOutput) AnthropicDirectOptions(c *gin.Context, provider *gatewayprovider.ExecutionProvider) forward.NativeAnthropicOptions {
	return forward.NativeAnthropicOptions{
		ProviderID: provider.Record.ID,
		UpdateWindow: func(ctx context.Context, h http.Header) {
			if p.Observer != nil {
				gatewayprovider.ObserveExecutionSessionWindow(ctx, p.Observer, provider, h)
			}
		},
		ReadBody: func(r io.Reader) ([]byte, error) {
			return ReadUpstreamResponseBody(r, p.Options.ReadLimit, c, AnthropicResponseTooLarge)
		},
		InvalidJSON: func(ctx context.Context, r *http.Response, body []byte, err error, model string) error {
			return gatewayprovider.NonJSONUpstreamFailure(ctx, p.Observer, r, provider, body, err, model)
		},
		ForceCache: requeststate.IsForceCacheBilling, ClassifyCache: anthropic.ClassifyResponseInputAsCacheRead,
		CopyHeaders:  func(dst, src http.Header) { WriteAnthropicPassthroughHeaders(dst, src, p.Headers) },
		ReverseTools: func(body []byte) []byte { return ReverseToolNamesIfPresent(c, body) },
		MaxLineSize: func() int {
			if p.Options.Configured && p.Options.MaxLineSize > 0 {
				return p.Options.MaxLineSize
			}
			return OpenAIResponseDefaultMaxLineSize
		},
		StreamInterval: p.TextStreamInterval,
		KeepaliveInterval: func() time.Duration {
			if p.Options.Configured && p.Options.StreamKeepaliveInterval > 0 {
				// Convert the configured keepalive spacing from seconds for the native
				// Anthropic reader.
				return time.Duration(p.Options.StreamKeepaliveInterval) * time.Second
			}
			return 0
		},
		ExtractData: anthropic.ExtractSSEDataLine, IsTerminal: anthropic.StreamEventIsTerminal,
		Log: func(format string, args ...any) { logging.LegacyPrintf("service.gateway", format, args...) },
		HandleTimeout: func(ctx context.Context, model string) {
			if p.Observer != nil {
				p.Observer.Core.HandleStreamTimeout(ctx, gatewayprovider.ExecutionRecord(provider), model)
			}
		},
	}
}
