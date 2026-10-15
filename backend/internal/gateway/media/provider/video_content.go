package provider

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// video_content.go opens Grok video content streams using task lookup,
// authorization and download callbacks.
// Video content retrieval first reads task status, then opens a signed URL
// or an authenticated provider download. The adapter delegates redirect
// rejection and Range handling to Grok; a returned stream retains download
// activity until its caller closes it.

// VideoContentOptions binds task lookup and download requests to gateway
// HTTP policy.
type VideoContentOptions struct {
	// StatusURL locates the task queried before download, and RequestID
	// selects its signed video URL. Range is trimmed and forwarded to the
	// content request when nonempty, allowing partial-download responses.
	StatusURL, RequestID, Range string // task-status lookup URL, video task identifier and HTTP Range header
	// Token authenticates status and fallback content requests. A signed
	// content URL receives no bearer credential or provider header callback;
	// JSON excludes the retained token value.
	Token string `json:"-"` // provider bearer authorization credential
	// Context is required for constructing both GET requests from the supplied
	// ctx. Its returned context controls each exchange and remains attached to
	// the opened content stream.
	Context func(context.Context) context.Context // request context selection callback
	// ContentURL is required when the status payload has no signed video URL.
	// Its error ends retrieval before a download request is constructed.
	ContentURL func() (string, error) // authenticated download URL fallback
	// ApplyHeaders is required for status and authenticated fallback requests
	// after standard headers are set. Signed downloads omit this callback so
	// provider credentials are not added to that destination.
	ApplyHeaders func(http.Header, string) // provider request header policy
	// Do performs both HTTP exchanges and requires a usable response and body
	// on nil error. Native retrieval closes status responses and transfers a
	// successful content body to the returned stream.
	Do func(*http.Request) (*http.Response, error) // status and content exchange callback
	// ReadStatus reads the initial task response under gateway limits. Native
	// retrieval closes that response before selecting or opening content,
	// including when reading fails.
	ReadStatus func(io.Reader) ([]byte, error) // bounded task status reader
	// Latency is required after status exchange begins and records elapsed
	// work through content response headers or earlier failure. Reading the
	// returned download body occurs after this measurement.
	Latency func(time.Duration) // pre-stream elapsed time observer
	// TransportError classifies failed status or content exchanges. It is
	// required on those paths; redirect rejection and error-status handling
	// remain separate decisions in native retrieval.
	TransportError func(error) error // exchange failure policy callback
	// HTTPError handles status codes at or above 400, using the observed
	// upstream request ID. Content status 416 passes through as an opened
	// stream instead of invoking this callback.
	HTTPError func(*http.Response, string) error // status-error handling with request ID
	// Enter optionally registers retrieval activity before status lookup.
	// After successful admission, retrieval failure invokes its completion
	// callback locally; a returned content stream retains that callback until
	// Close.
	Enter func() (func(), error) // optional video download activity admission
}

// String returns a fixed video-content option label for formatted output.
// It leaves retained task URLs, credentials and callback state out of
// diagnostics, including signed URL query data.
func (o VideoContentOptions) String() string { return "media VideoContentOptions" }

// GoString uses the fixed video-content label for Go-style option
// formatting.
func (o VideoContentOptions) GoString() string { return o.String() }

// native copies video download option values into Grok retrieval options.
// Callback captures remain shared, so the caller keeps their state valid
// throughout status lookup and content opening.
func (o VideoContentOptions) native() grok.VideoContentOptions {
	return grok.VideoContentOptions{StatusURL: o.StatusURL, RequestID: o.RequestID, Range: o.Range, Token: o.Token, Context: o.Context, ContentURL: o.ContentURL, ApplyHeaders: o.ApplyHeaders, Do: o.Do, ReadStatus: o.ReadStatus, Latency: o.Latency, TransportError: o.TransportError, HTTPError: o.HTTPError, Enter: o.Enter}
}

// OpenVideoContent checks task status and delegates opening its video
// download. Callers keep callback bindings valid, then read and close the
// returned stream; Close releases its body and activity registration.
// Returned status bytes and headers retain native response storage.
func OpenVideoContent(ctx context.Context, options VideoContentOptions) (*grok.VideoContent, error) {
	return grok.OpenVideoContent(ctx, options.native())
}
