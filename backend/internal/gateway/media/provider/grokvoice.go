package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grokvoice.go binds prepared Grok voice requests and response callbacks to
// native audio execution.
// Grok voice execution forwards a prepared request and derives audio units
// from its base operation and retained payload. The adapter passes
// response/transport callbacks to the native executor, which closes response
// resources and reports audio evidence before releasing activity.

// GrokVoiceOptions binds a prepared voice request to response handling and
// audio estimation. BaseEndpoint selects the estimation family, while
// Request and callback captures remain shared during the exchange.
type GrokVoiceOptions struct {
	ProviderID int64 // selected gateway-provider identifier
	// Endpoint retains the operation label, BaseEndpoint supplies
	// result/audio-estimation family, and ContentType guides input audio parsing.
	// Request itself is prepared by the caller before this native adapter
	// executes.
	Endpoint, BaseEndpoint, ContentType string // voice operation label, audio billing operation and MIME
	// Request retains its credential-bearing headers, context and body. Keep it
	// stable during exchange and redact authorization data before expanded
	// formatting or serialization.
	Request *http.Request `json:"-"` // shared prepared voice request
	// Do exchanges the prepared voice request under its retained context. A
	// nil error requires a usable response and body, whose cleanup native
	// execution schedules before reading voice output.
	Do func(*http.Request) (*http.Response, error) // prepared voice exchange callback
	// Enter optionally tracks an admitted voice exchange as application work.
	// Successful admission returns a callable completion function, which
	// native cleanup invokes after closing any acquired voice response body.
	Enter func() (func(), error) // optional activity admission callback
	// AfterExchange optionally observes header latency and the exchange error.
	// Its result replaces that error before response handling; returning nil
	// requires that Do supplied a usable response and body.
	AfterExchange func(time.Duration, error) error // optional voice exchange policy
	// BeforeResponse can finish handling before body reading. Handled true or an
	// error ends native execution while its acquired response still reaches
	// scheduled cleanup.
	BeforeResponse func(*http.Response) (bool, error) // optional early voice response handler
	// ReadBody supplies the voice response bytes used for forwarding and audio
	// estimation alongside input.Body. A read error ends the attempt;
	// downstream write failure is observed separately from successful reading.
	ReadBody func(io.Reader) ([]byte, error) // required voice response byte reader
	// CopyHeaders runs when output is present before voice bytes are emitted. It
	// chooses permitted response headers; upstream header evidence remains in the
	// returned result separately.
	CopyHeaders func(http.Header, http.Header) // optional response header filtering
}

// GrokVoice connects gateway voice-response policy to the native Grok
// executor.
type GrokVoice struct {
	// Options captures the prepared voice request and its estimation labels.
	// Callers keep shared request and callback state stable while Execute
	// delegates byte handling to the Grok executor.
	Options GrokVoiceOptions // captured native media attempt options
}

// String returns the GrokVoice operation label and selected ProviderID for
// diagnostics. Prepared voice request headers and body are omitted from
// that formatted representation, so authorization data is not expanded.
func (o GrokVoiceOptions) String() string {
	return fmt.Sprintf("media GrokVoice provider=%d", o.ProviderID)
}

// GoString uses String's GrokVoice provider label when formatting options
// as Go.
func (o GrokVoiceOptions) GoString() string { return o.String() }

// Execute binds the captured Grok voice target and delegates one supported
// voice protocol attempt. Stable prepared request/options belong to the
// caller; native execution owns response/activity cleanup and returns model,
// duration and estimated audio usage.
func (e GrokVoice) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (upstream.AttemptResult, error) {
	o := e.Options
	target := &grok.VoiceTarget{
		ProviderID:     o.ProviderID,
		Endpoint:       o.Endpoint,
		BaseEndpoint:   o.BaseEndpoint,
		ContentType:    o.ContentType,
		Request:        o.Request,
		Do:             o.Do,
		Enter:          o.Enter,
		AfterExchange:  o.AfterExchange,
		BeforeResponse: o.BeforeResponse,
		ReadBody:       o.ReadBody,
		CopyHeaders:    o.CopyHeaders,
	}
	input.Target = target
	return (grok.VoiceExecutor{}).Execute(ctx, input, sink)
}
