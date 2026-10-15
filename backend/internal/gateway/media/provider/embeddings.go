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

// embeddings.go binds embedding request credentials, headers and response
// callbacks to native OpenAI execution.
// Embedding requests use a caller-selected destination, credentials and
// prepared byte payload. This adapter binds native execution callbacks; the
// native executor builds the POST, reads usage/result evidence and closes
// response resources before leaving application activity.

// EmbeddingsOptions supplies the destination, authorization and callbacks
// for a constructed embedding POST. Forwarded header storage and callback
// captures remain shared with the caller during native execution.
type EmbeddingsOptions struct {
	ProviderID int64 // selected gateway-provider identifier
	// Model supplies result metadata rather than rewriting input.Body. URL is the
	// POST destination; nonempty UserAgent overrides copied request headers
	// before ApplyHeaders applies provider policy.
	Model, URL, UserAgent string // result metadata model, embedding POST destination and outbound User-Agent override
	// Token supplies bearer authorization and is excluded from JSON.
	// ForwardHeaders and ApplyHeaders run afterward, so the caller's header
	// policy determines final request values.
	Token string `json:"-"` // upstream bearer credential
	// ForwardHeaders is read while constructing request headers and retains
	// caller map/slices. Keep it stable during Execute; the callback ApplyHeaders
	// can then override the constructed headers.
	ForwardHeaders http.Header `json:"-"` // shared forwarded request header values
	// StartedAt supplies duration origin; zero selects execution-time Now.
	// Native execution registers duration capture before response cleanup, so
	// acquired body cleanup runs first and is included in that duration.
	StartedAt time.Time // optional request duration origin
	// RequestContext returns context and cleanup used during POST construction.
	// Cleanup is invoked immediately after NewRequestWithContext, so it must
	// leave that request usable; production supplies a detached context with
	// no-op cleanup.
	RequestContext func(context.Context) (context.Context, context.CancelFunc) // request context construction callback
	// Enter optionally admits embedding work before request construction. On
	// successful admission it supplies a callable completion function; native
	// defers invoke that function after response cleanup and duration capture.
	Enter func() (func(), error) // optional activity admission callback
	// ApplyHeaders runs after authorization, forwarded headers and optional
	// User-Agent. Supply a usable callback that applies the selected provider's
	// final outbound header choices.
	ApplyHeaders func(http.Header) // final provider header policy
	// Do exchanges the constructed embedding POST under its assigned context.
	// Returning nil error requires a usable response and body, which native
	// execution closes after handling its bytes.
	Do func(*http.Request) (*http.Response, error) // constructed-request exchange callback
	// TransportError supplies the returned error after Do fails. A configured
	// callback is required on that path, and retry or output policy remains with
	// its consumer.
	TransportError func(error) error // exchange failure classification callback
	// ReadErrorBody captures status-error bytes under caller policy. Native
	// execution closes the old body and installs a replay reader before HTTPError
	// is invoked.
	ReadErrorBody func(*http.Response) []byte // error response byte capture
	// HTTPError handles a response at or above 400 using replayable captured
	// bytes. It returns the attempt error while upstream body and activity
	// cleanup remain with native execution.
	HTTPError func(*http.Response, []byte) error // status-error policy callback
	// ReadBody reads a nonerror response under caller limits. Read failure
	// reaches ReadFailure, while successful bytes supply forwarding and
	// embedding-usage extraction.
	ReadBody func(io.Reader) ([]byte, error) // successful response byte reader
	// ReadFailure supplies the returned failure when successful-response bytes
	// cannot be read. Caller policy decides provider effects and recovery; this
	// callback does not produce successful usage.
	ReadFailure func(error) error // body-read failure classification callback
	// WriteHeaders copies upstream headers into output when it is still
	// unwritten. The callback chooses allowed headers; body write reporting does
	// not prove downstream client receipt.
	WriteHeaders func(http.Header, http.Header) // response header filtering callback
}

// Embeddings binds gateway request and response policy to native embedding
// execution.
type Embeddings struct {
	// Options supplies the values used to construct an embedding POST. Execute
	// copies this struct while retaining its header map and callback captures,
	// which callers keep stable.
	Options EmbeddingsOptions // captured native media attempt options
}

// String formats the selected Embeddings provider as an operation label.
// The result includes ProviderID while leaving Token and forwarded header
// values out of ordinary formatted diagnostics.
func (o EmbeddingsOptions) String() string {
	return fmt.Sprintf("media Embeddings provider=%d", o.ProviderID)
}

// GoString returns the Embeddings provider label during Go-style option
// formatting.
func (o EmbeddingsOptions) GoString() string { return o.String() }

// Execute builds the native Embeddings target and delegates one protocol
// attempt using input.Body. Callers supply stable option references and
// configured callbacks; native execution constructs the request and closes
// response/activity resources before returning observed result or error.
func (e Embeddings) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (upstream.AttemptResult, error) {
	o := e.Options
	target := &openai.EmbeddingsTarget{
		ProviderID:     o.ProviderID,
		Model:          o.Model,
		URL:            o.URL,
		UserAgent:      o.UserAgent,
		Token:          o.Token,
		ForwardHeaders: o.ForwardHeaders,
		StartedAt:      o.StartedAt,
		RequestContext: o.RequestContext,
		Enter:          o.Enter,
		ApplyHeaders:   o.ApplyHeaders,
		Do:             o.Do,
		TransportError: o.TransportError,
		ReadErrorBody:  o.ReadErrorBody,
		HTTPError:      o.HTTPError,
		ReadBody:       o.ReadBody,
		ReadFailure:    o.ReadFailure,
		WriteHeaders:   o.WriteHeaders,
	}
	input.Target = target
	return (openai.EmbeddingsExecutor{}).Execute(ctx, input, sink)
}
