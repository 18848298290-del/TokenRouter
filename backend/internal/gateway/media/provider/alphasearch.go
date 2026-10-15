package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// alphasearch.go binds prepared Alpha Search requests and response callbacks
// to the native OpenAI executor.
// Standalone Alpha Search and its Responses fallback share a prepared request
// but use different response and quota handling. The adapter passes captured
// callbacks to the native executor, which owns response-body cleanup while
// HTTP policy and completion stay with callers.

// AlphaSearchOptions binds the prepared search request to status,
// conversion and quota callbacks. The native executor reads these shared
// request references synchronously during one Alpha Search attempt.
type AlphaSearchOptions struct {
	ProviderID int64 // selected gateway-provider identifier
	// Request contains authorization and retained body/context references.
	// Execute passes it through; callers keep it stable and exclude credentials
	// from logging or expanded serialization.
	Request *http.Request `json:"-"` // shared prepared upstream request
	// ResponsesFallback selects successful SSE-to-search conversion. Quota
	// observation occurs on success for that path, while direct Alpha Search
	// observes headers for every forwarded response.
	ResponsesFallback bool   // use native Responses search fallback
	Model             string // upstream model for result metadata
	// Enter optionally registers this search exchange as active application
	// work. A successful call supplies a callable completion function, which
	// runs after the acquired original response body has closed.
	Enter func() (func(), error) // optional activity admission callback
	// Do sends the prepared Alpha Search request under its retained context. A
	// nil error requires a usable response and body; native execution
	// schedules closing that acquired body.
	Do func(*http.Request) (*http.Response, error) // prepared-request exchange callback
	// Latency is called after Do even when exchange fails. Supply a usable
	// callback for that duration before transport classification or response-body
	// reading proceeds.
	Latency func(time.Duration) // response-header latency observer
	// TransportError is required on exchange failure and supplies the returned
	// forwarding error. It leaves HTTP policy and provider-loop recovery with the
	// caller.
	TransportError func(error) error // transport failure classification callback
	// ReadBody reads the response before status handling or conversion. Its error
	// stops native execution, and original response storage is closed even if
	// later policy rewinds Body.
	ReadBody func(io.Reader) ([]byte, error) // bounded response byte reader
	// HTTPError handles status codes at or above 400 and can return a classified
	// failure. A nil result permits forwarding; cleanup still closes the
	// originally acquired body.
	HTTPError func(*http.Response, []byte) error // HTTP error handling callback
	// UpdateQuota is required for direct responses and successful Responses
	// fallback. Native execution invokes it before successful conversion/result
	// capture; observation alone does not establish settlement.
	UpdateQuota func(http.Header) // quota header observation callback
	// Headers copies into output headers for direct or unsuccessful-fallback
	// responses. Successful Responses conversion creates its JSON response
	// separately, and caller policy chooses which headers may pass.
	Headers func(http.Header, http.Header) // filtered response header copier
}

// AlphaSearch adapts selected gateway search callbacks for the OpenAI
// executor.
type AlphaSearch struct {
	// Options binds the selected provider request to Alpha Search execution.
	// Its request and callbacks remain shared, so the caller keeps them stable
	// until Execute returns.
	Options AlphaSearchOptions // captured native media attempt options
}

// String identifies the Alpha Search provider in ordinary formatted output.
// It prints ProviderID with the operation label, keeping the prepared
// request and its authorization headers out of that representation.
func (o AlphaSearchOptions) String() string {
	return fmt.Sprintf("media AlphaSearch provider=%d", o.ProviderID)
}

// GoString uses the Alpha Search label for Go-style formatting.
func (o AlphaSearchOptions) GoString() string { return o.String() }

// Execute builds one native Alpha Search target from captured options and
// delegates execution. Prepared Request supplies network context and body;
// input supplies protocol/response identity, while native execution owns
// activity and original response-body cleanup.
func (e AlphaSearch) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (upstream.AttemptResult, error) {
	o := e.Options
	target := &openai.AlphaSearchTarget{
		ProviderID:        o.ProviderID,
		Request:           o.Request,
		ResponsesFallback: o.ResponsesFallback,
		Model:             o.Model,
		Enter:             o.Enter,
		Do:                o.Do,
		Latency:           o.Latency,
		TransportError:    o.TransportError,
		ReadBody:          o.ReadBody,
		HTTPError:         o.HTTPError,
		UpdateQuota:       o.UpdateQuota,
		Headers:           o.Headers,
	}
	input.Target = target
	return (openai.AlphaSearchExecutor{}).Execute(ctx, input, sink)
}
