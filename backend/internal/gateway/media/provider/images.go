package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// images.go binds OpenAI image requests and response readers to native OAuth
// or API Key execution.
// OpenAI image response handling differs between OAuth and API-key transports
// and between streaming and buffered output. This adapter binds prepared
// request and image-reader callbacks to native execution, which retains
// observed image/usage evidence and cleans response resources before releasing
// activity.

// ImagesOptions pairs a prepared image request with credential-specific
// readers and error handling. OAuth and requested streaming mode select the
// reader path; request and callback state remain shared during execution.
type ImagesOptions struct {
	ProviderID int64 // selected gateway-provider identifier
	// OAuth selects image-specific stream/buffer readers and response-error
	// handling. API-key paths use their supported response format; the caller
	// prepares a request appropriate to that selected credential type.
	OAuth bool // select OAuth image response handling
	// Model supplies upstream result metadata, while ResponseFormat and
	// StreamPrefix guide OAuth image response conversion. Input separately
	// carries the client response model and streaming request choice.
	Model, ResponseFormat, StreamPrefix string // provider execution model, OAuth output encoding and OAuth event prefix
	// StartedAt supplies duration origin; zero uses native execution Now.
	// Duration is recorded before deferred cleanup of the final response body.
	// Status-error handling closes the acquired body earlier, before installing
	// its redacted replay reader.
	StartedAt time.Time // optional image duration origin
	// Request contains authorization plus retained context/body references. Keep
	// them stable while Execute uses the prepared request, and exclude
	// credentials from expanded logs or serialization.
	Request *http.Request `json:"-"` // shared prepared image request
	// Options is passed by value with its retained reader callbacks/state
	// references. Callers configure the required readers and keep shared
	// image-response state stable under their output ownership rules.
	Options openai.ImageResponseOptions // native image-reader callback options
	// Enter optionally admits this image attempt into application activity
	// tracking. Successful admission returns a callable completion function,
	// invoked after acquired response cleanup even when reader or HTTP policy
	// fails.
	Enter func() (func(), error) // optional activity admission callback
	// Do sends the prepared image request with its retained context and body.
	// A nil error requires a usable response and body, which native image
	// handling closes on its acquired-response paths.
	Do func(*http.Request) (*http.Response, error) // prepared image exchange callback
	// TransportError supplies the returned failure after Do fails. It is required
	// on that path; provider policy and client output stay with its gateway
	// consumer.
	TransportError func(error) error // exchange failure classification callback
	// ReadErrorBody captures error response bytes before the old body is closed.
	// Redacted bytes become a replay reader passed into HTTPError for status
	// policy.
	ReadErrorBody func(*http.Response) []byte // status-error byte capture callback
	// RedactErrorBody runs after status-error bytes are read and before
	// replay/body policy. It supplies the bytes exposed to HTTPError, while the
	// caller owns sanitization completeness.
	RedactErrorBody func([]byte) []byte // captured error byte sanitizer
	// HTTPError handles replayable redacted bytes for status codes at or above
	// 400. Its returned error ends native execution with response/activity
	// cleanup still scheduled.
	HTTPError func(*http.Response, []byte) error // status-error policy callback
	// ResponseError handles OAuth read failure before any completed image or in
	// buffered mode. It receives prior adjusted output size so caller policy can
	// decide recovery/output handling.
	ResponseError func(*http.Response, int, error) error // OAuth early/buffered failure policy
}

// Images binds gateway image-response policy to native OpenAI generation or
// editing.
type Images struct {
	// Options captures the prepared request and image-reader configuration for
	// an attempt. Execute retains request and reader-state references, so
	// callers keep them stable until native image handling finishes.
	Options ImagesOptions // captured native media attempt options
}

// String reports the selected Images provider with a fixed operation label.
// Formatting includes ProviderID while leaving the prepared authenticated
// request and nested image-reader state out of the diagnostic
// representation.
func (o ImagesOptions) String() string { return fmt.Sprintf("media Images provider=%d", o.ProviderID) }

// GoString returns the Images provider label for Go-style diagnostic
// formatting.
func (o ImagesOptions) GoString() string { return o.String() }

// Execute binds native image options and delegates a generation/edit attempt
// using the prepared Request. Callers keep referenced options stable; native
// handling captures output commitment, images/usage and partial errors while
// owning response and activity cleanup.
func (e Images) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (upstream.AttemptResult, error) {
	o := e.Options
	target := &openai.ImagesTarget{
		ProviderID:      o.ProviderID,
		OAuth:           o.OAuth,
		Model:           o.Model,
		ResponseFormat:  o.ResponseFormat,
		StreamPrefix:    o.StreamPrefix,
		StartedAt:       o.StartedAt,
		Request:         o.Request,
		Options:         o.Options,
		Enter:           o.Enter,
		Do:              o.Do,
		TransportError:  o.TransportError,
		ReadErrorBody:   o.ReadErrorBody,
		RedactErrorBody: o.RedactErrorBody,
		HTTPError:       o.HTTPError,
		ResponseError:   o.ResponseError,
	}
	input.Target = target
	return (openai.ImagesExecutor{}).Execute(ctx, input, sink)
}
