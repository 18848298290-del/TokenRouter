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

// openai_response_output.go binds Responses readers to shared policy services
// and request-local observations.
// Responses readers report observed usage, model and service tier separately
// from final completion decisions. The HTTP output binding supplies
// request-local observations and app-owned health, tool and session services
// to those readers; a failed read can therefore return observations for the
// entry to evaluate.

// OpenAIResponseDefaultMaxLineSize supplies the byte ceiling for native and compatible
// SSE lines when a reader has no positive configured line limit.
const OpenAIResponseDefaultMaxLineSize = 500 * 1024 * 1024

// OpenAIResponseOptions carries startup response-reader limits, timeout
// seconds and diagnostic settings. Individual option constructors decide when
// Configured enables their settings; error-body capture also reads its logging
// limit directly.
type OpenAIResponseOptions struct {
	// ImageStreamDataIntervalTimeout and ImageStreamKeepaliveInterval configure
	// image reader timers together. Image helpers disable either timer when
	// options are unconfigured or its own value is nonpositive.
	// ImageStreamDataIntervalTimeout: image data idle seconds.
	// ImageStreamKeepaliveInterval: image heartbeat interval seconds.
	ImageStreamDataIntervalTimeout, ImageStreamKeepaliveInterval int
	// Configured enables the option constructors' static settings. Helpers such
	// as errorBodyReadLimit can still read selected logging values independently.
	Configured bool
	// MaxLineSize bounds one SSE line in bytes. Text reader options require
	// Configured before applying a positive value; client-tool body wrappers
	// use positive values independently of that flag.
	MaxLineSize int
	// StreamDataIntervalTimeout and StreamKeepaliveInterval supply text timer
	// seconds when configured and positive. Grok's standard stream reader applies
	// its platform idle policy after these common settings are read.
	// StreamDataIntervalTimeout: text data idle seconds.
	// StreamKeepaliveInterval: text heartbeat interval seconds.
	StreamDataIntervalTimeout, StreamKeepaliveInterval int
	// OpenAIFirstOutputTimeoutSeconds enables the native first-output budget.
	// OpenAIHighEffortFirstOutputTimeoutSeconds replaces that positive base for
	// high, xhigh or max when its own value is positive; it cannot enable a
	// disabled base budget. Both values are configured whole seconds.
	// OpenAIFirstOutputTimeoutSeconds: base first-output timeout seconds.
	// OpenAIHighEffortFirstOutputTimeoutSeconds: high-effort override timeout seconds.
	OpenAIFirstOutputTimeoutSeconds, OpenAIHighEffortFirstOutputTimeoutSeconds int
	// upstream body logging enabled
	LogUpstreamErrorBody bool
	// LogUpstreamErrorBodyMaxBytes bounds logged excerpts. ReadErrorBody can also
	// expand its capture ceiling above 512 KiB when body logging is enabled.
	LogUpstreamErrorBodyMaxBytes int
	// ResponseHeadersEnabled controls NonStreamOptions content-type preservation:
	// configured disabled filtering retains the reader's supplied upstream type.
	ResponseHeadersEnabled bool
	// ReadLimit bounds buffered upstream response bytes. Nonpositive limits use
	// the shared 128 MiB reader default; callers keep positive limits below MaxInt64.
	ReadLimit int64
}

// OpenAIResponseOutput binds shared response services to synchronous HTTP
// reading and output. Callers keep its dependencies configured for the
// selected protocol and retain the request context until all reading callbacks
// finish.
type OpenAIResponseOutput struct {
	// Reasoning supplies shared optional lookup and caching. Its nil or missing
	// cache path returns absent lookup text and skips history publication.
	Reasoning *session.ReasoningHistory // shared reasoning history service
	Options   OpenAIResponseOptions     // startup response reader settings
	// OpenAI response health policy
	Health     *provideradapter.OpenAIResponseHealth
	GrokHealth *provideradapter.GrokHealth // Grok response health policy
	// Observer receives stream and first-output timeout feedback when present.
	// A nil observer skips that feedback while FirstOutputFailure still records Ops.
	// upstream timeout health observer
	Observer *provideradapter.UpstreamHealth
	// Headers supplies optional upstream metadata filtering. StreamOptions
	// stages request ID and turn state when nil; Chat and Raw skip filtered-header copying.
	Headers *egress.CompiledHeaderFilter // compiled upstream header filter
	// turn-state header ownership adapter
	Turns *CodexTurnStateHeaders
	// Corrector supplies optional Codex name repair. NonStreamOptions still
	// normalizes function arguments through CorrectResponseBody with a nil receiver.
	Corrector *openai.CodexToolCorrector // Codex tool payload corrector
	// shared proxy disconnect circuit
	ProxyCircuit *egress.ProxyStreamCircuit
	// TTFT reads the current first-token timing mode for each request. A nil
	// callback selects semantic timing through TTFTMode's normalization.
	TTFT func(context.Context) string
	// Redact supplies optional provider-identity cleanup for captured errors.
	// Without this callback, redact returns the supplied body bytes as received.
	Redact    func(context.Context, *gatewayadapter.ExecutionProvider, []byte) []byte
	Responses session.OpenAIWSStateStore // shared Responses session store
	// ResponseTTL supplies response-provider and owner binding lifetimes.
	// BindResponseProvider requires this callback when Responses storage is configured.
	ResponseTTL func() time.Duration
}

// TTFTMode reads and normalizes the current first-token timing mode for the
// request. Nil output or callback starts from semantic mode; gateway
// normalization determines the result for a configured callback value.
func (p *OpenAIResponseOutput) TTFTMode(ctx context.Context) string {
	mode := gateway.OpenAITTFTModeSemantic
	if p != nil && p.TTFT != nil {
		mode = p.TTFT(ctx)
	}
	return gateway.NormalizeOpenAITTFTMode(mode)
}

// redact delegates captured error-body cleanup to the configured callback. Nil
// output or callback returns the supplied bytes, so callers retain their
// existing ownership and cannot assume this path sanitized the body.
func (p *OpenAIResponseOutput) redact(ctx context.Context, target *gatewayadapter.ExecutionProvider, body []byte) []byte {
	if p == nil || p.Redact == nil {
		return body
	}
	return p.Redact(ctx, target, body)
}

// ExecutionErrorProvider extracts diagnostic provider identity for error
// output. A nil execution provider returns nil; the new value carries ID, name
// and platform without sharing the mutable provider record.
func ExecutionErrorProvider(value *gatewayadapter.ExecutionProvider) *UpstreamErrorProvider {
	if value == nil {
		return nil
	}
	return &UpstreamErrorProvider{ID: value.Record.ID, Name: value.Record.Name, Platform: value.Record.Platform}
}

// ReadStreamObservation delegates native Responses streaming and returns its
// observations with any read error. The entry decides completion eligibility,
// and the execution caller retains response-body cleanup while the reader
// invokes synchronous request-bound output callbacks.
func (p *OpenAIResponseOutput) ReadStreamObservation(ctx context.Context, resp *http.Response, c *gin.Context, target *gatewayadapter.ExecutionProvider, started time.Time, original, mapped, effort string) (*openai.StreamingResult, error) {
	return openai.ReadStreamingResponse(ctx, resp, upstream.NewOutputContext(NewResponseSink(c)), p.StreamOptions(ctx, c, target, effort), started, original, mapped, effort)
}

// NonStream delegates native Responses buffered reading and HTTP output for
// the selected attempt. The execution caller owns response closure and retains
// c until callbacks finish; result and error are returned for the entry's
// completion decision.
func (p *OpenAIResponseOutput) NonStream(ctx context.Context, resp *http.Response, c *gin.Context, target *gatewayadapter.ExecutionProvider, original, mapped string) (*openai.NonStreamingResult, error) {
	return openai.ReadNonStreamingResponse(ctx, resp, upstream.NewOutputContext(NewResponseSink(c)), p.NonStreamOptions(ctx, c, target), original, mapped)
}
