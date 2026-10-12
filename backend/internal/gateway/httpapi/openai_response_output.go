package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

const OpenAIResponseDefaultMaxLineSize = 500 * 1024 * 1024

// OpenAIResponseOptions holds static settings. Timeout and interval integers are seconds;
// body and line limits are bytes. TTFT mode is read per request.
type OpenAIResponseOptions struct {
	ImageStreamDataIntervalTimeout, ImageStreamKeepaliveInterval               int
	Configured                                                                 bool
	MaxLineSize                                                                int
	StreamDataIntervalTimeout, StreamKeepaliveInterval                         int
	OpenAIFirstOutputTimeoutSeconds, OpenAIHighEffortFirstOutputTimeoutSeconds int
	LogUpstreamErrorBody                                                       bool
	LogUpstreamErrorBodyMaxBytes                                               int
	ResponseHeadersEnabled                                                     bool
	ReadLimit                                                                  int64
}

// OpenAIResponseOutput combines upstream response readers with HTTP output, health policy, and diagnostics.
type OpenAIResponseOutput struct {
	Reasoning    *session.ReasoningHistory
	Options      OpenAIResponseOptions
	Health       *provideradapter.OpenAIResponseHealth
	GrokHealth   *provideradapter.GrokHealth
	Observer     *provideradapter.UpstreamHealth
	Headers      *egress.CompiledHeaderFilter
	Turns        *CodexTurnStateHeaders
	Corrector    *openai.CodexToolCorrector
	ProxyCircuit *egress.ProxyStreamCircuit
	TTFT         func(context.Context) string
	Redact       func(context.Context, *gatewayadapter.ExecutionProvider, []byte) []byte
	Responses    session.OpenAIWSStateStore
	ResponseTTL  func() time.Duration
}

func (p *OpenAIResponseOutput) TTFTMode(ctx context.Context) string {
	mode := gateway.OpenAITTFTModeSemantic
	if p != nil && p.TTFT != nil {
		mode = p.TTFT(ctx)
	}
	return gateway.NormalizeOpenAITTFTMode(mode)
}

func (p *OpenAIResponseOutput) redact(ctx context.Context, target *gatewayadapter.ExecutionProvider, body []byte) []byte {
	if p == nil || p.Redact == nil {
		return body
	}
	return p.Redact(ctx, target, body)
}

// ExecutionErrorProvider returns diagnostic fields for the current attempt provider.
func ExecutionErrorProvider(value *gatewayadapter.ExecutionProvider) *UpstreamErrorProvider {
	if value == nil {
		return nil
	}
	return &UpstreamErrorProvider{ID: value.Record.ID, Name: value.Record.Name, Platform: value.Record.Platform}
}

// ReadStreamObservation returns the observed stream result and read error; the entry decides completion.
func (p *OpenAIResponseOutput) ReadStreamObservation(ctx context.Context, resp *http.Response, c *gin.Context, target *gatewayadapter.ExecutionProvider, started time.Time, original, mapped, effort string) (*openai.StreamingResult, error) {
	return openai.ReadStreamingResponse(ctx, resp, upstream.NewOutputContext(ResponseSink{Writer: c.Writer}), p.StreamOptions(ctx, c, target, effort), started, original, mapped, effort)
}

func (p *OpenAIResponseOutput) NonStream(ctx context.Context, resp *http.Response, c *gin.Context, target *gatewayadapter.ExecutionProvider, original, mapped string) (*openai.NonStreamingResult, error) {
	return openai.ReadNonStreamingResponse(ctx, resp, upstream.NewOutputContext(ResponseSink{Writer: c.Writer}), p.NonStreamOptions(ctx, c, target), original, mapped)
}
