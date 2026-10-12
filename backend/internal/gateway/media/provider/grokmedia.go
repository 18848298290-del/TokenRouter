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

// Grok image and video execution receive a prepared authenticated request and
// media-specific response callbacks. The adapter binds a native target; native
// execution validates image output before transformation, writes retained
// media bytes and closes the response before releasing activity.

// GrokMediaOptions binds an authenticated image/video request to
// media-response handling. Endpoint chooses image validation, while
// prepared request storage and callback captures remain shared for this
// execution.
type GrokMediaOptions struct {
	ProviderID int64                  // selected gateway-provider identifier
	Endpoint   grok.GrokMediaEndpoint // native image or video operation
	// Request retains caller context, headers and body references. Keep it stable
	// during native exchange and redact its credential-bearing fields before
	// logging or serialization.
	Request *http.Request `json:"-"` // shared prepared authenticated request
	// StartedAt is used directly for returned duration with no zero-time default.
	// Callers set the origin appropriate to this request before native media
	// execution begins.
	StartedAt time.Time // required result duration origin
	// Do sends the prepared Grok media request using its context. A nil error
	// supplies a usable response and body, which native execution closes after
	// any early handling or read.
	Do func(*http.Request) (*http.Response, error) // prepared media exchange callback
	// Enter optionally registers the media exchange with application activity
	// tracking. Successful admission returns a callable completion function,
	// deferred until after the acquired response closes on every subsequent
	// return path.
	Enter func() (func(), error) // optional activity admission callback
	// AfterExchange optionally observes header latency and the exchange error.
	// Its result replaces that error before response handling; returning nil
	// requires that Do supplied a usable response and body.
	AfterExchange func(time.Duration, error) error // optional exchange policy callback
	// BeforeResponse runs with an acquired response before body reading. Handled
	// true or a returned error ends native handling, and the native executor
	// still closes that response body.
	BeforeResponse func(*http.Response) (bool, error) // optional early response handler
	// ReadBody supplies the response bytes used for image counting and optional
	// transformation. Errors stop native result handling while response/activity
	// cleanup remains scheduled.
	ReadBody func(io.Reader) ([]byte, error) // required media byte reader
	// CountImages is required for image generation/edit endpoints and runs before
	// TransformBody. A nonpositive count produces MissingImageOutput rather than
	// a served successful image result.
	CountImages func([]byte) int // image output validation callback
	// TransformBody runs after successful reading and image validation.
	// Returned bytes become output and MediaBody evidence, so the callback
	// keeps that storage valid for both uses.
	TransformBody func([]byte) []byte // optional media response transformation
	// CopyHeaders runs when output is present, before the media body is
	// emitted. The callback chooses permitted output headers; native execution
	// separately retains upstream header evidence in its result.
	CopyHeaders func(http.Header, http.Header) // optional response header filtering
}

// GrokMedia delegates selected image and video handling to Grok native
// execution.
type GrokMedia struct {
	// Options pairs a prepared Grok media request with its response callbacks.
	// Execute retains those references, so callers keep the request and
	// callback state stable throughout the exchange.
	Options GrokMediaOptions // captured native media attempt options
}

// String reports the selected provider with the GrokMedia operation label.
// That representation includes ProviderID and leaves the authenticated
// request out of formatted output, including its retained headers and body.
func (o GrokMediaOptions) String() string {
	return fmt.Sprintf("media GrokMedia provider=%d", o.ProviderID)
}

// GoString formats Grok media options with String's credential-free
// provider label.
func (o GrokMediaOptions) GoString() string { return o.String() }

// Execute binds one Grok native media target and delegates its image/video
// protocol attempt. Prepared Request controls exchange context/body, while
// input carries protocol/output evidence; native execution owns
// response/activity cleanup and returns retained media observations.
func (e GrokMedia) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (upstream.AttemptResult, error) {
	o := e.Options
	target := &grok.MediaTarget{
		ProviderID:     o.ProviderID,
		Endpoint:       o.Endpoint,
		Request:        o.Request,
		StartedAt:      o.StartedAt,
		Do:             o.Do,
		Enter:          o.Enter,
		AfterExchange:  o.AfterExchange,
		BeforeResponse: o.BeforeResponse,
		ReadBody:       o.ReadBody,
		CountImages:    o.CountImages,
		TransformBody:  o.TransformBody,
		CopyHeaders:    o.CopyHeaders,
	}
	input.Target = target
	return (grok.MediaExecutor{}).Execute(ctx, input, sink)
}
