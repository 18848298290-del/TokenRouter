package grok

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// video_content.go opens Grok video downloads and retains activity admission
// until the returned stream closes.
// Video content lookup begins with an authenticated status request, then
// chooses a recognized signed video URL or the provider's authenticated relay
// endpoint. Signed downloads carry the requested Range while omitting bearer
// authentication and provider headers. Redirect responses are rejected after
// the caller's transport returns, so that transport must expose redirects
// rather than follow them automatically.
//
// An optional activity admission remains held across status lookup and
// download consumption. Failure after admission releases it before returning;
// success transfers release to VideoContent.Close along with body ownership.
// Reading to EOF does not release this activity. Response headers and status
// bytes remain associated with the resource, while sync.Once coordinates
// repeated Close calls and retains the first body-close result.

// VideoContentOptions supplies task status, download transport, and resource
// lifecycle hooks for Grok video content.
type VideoContentOptions struct {
	// StatusURL locates task state, RequestID distinguishes this task's relay
	// URL, and Range is forwarded after trimming. The caller checks task
	// ownership and validates its configured status and relay endpoints before
	// opening content.
	StatusURL, RequestID, Range string // status lookup URL; video task ID; requested byte range
	// Token authenticates status and relay exchanges and is omitted from signed
	// downloads. Its json exclusion prevents direct option serialization from
	// exposing it.
	Token string `json:"-"` // upstream bearer access token
	// Context is required for each request. The gateway caller uses it to mark
	// redirect following disabled; Do must honor that policy for checks here to
	// apply.
	Context func(context.Context) context.Context // request context preparation callback
	// ContentURL is required when the status body yields no signed download URL.
	// It constructs the caller-validated relay target for this task and may
	// return an error.
	ContentURL func() (string, error) // authenticated relay URL callback
	// ApplyHeaders is required for status and authenticated relay requests.
	// Signed downloads skip it so provider identity headers cannot accompany
	// their signed URL.
	ApplyHeaders func(http.Header, string) // provider request header callback
	// Do is required and returns responses with bodies on nil error. It must
	// expose redirects rather than following them before this opener checks
	// status.
	Do func(*http.Request) (*http.Response, error) // status and download transport
	// ReadStatus is required and controls status-body size or parsing policy.
	// The opener closes that body after the callback returns, including when
	// reading fails.
	ReadStatus func(io.Reader) ([]byte, error) // status response reader callback
	// Latency is required after the status exchange begins. It receives elapsed
	// time through content response headers or an intervening failure, excluding
	// download consumption.
	Latency func(time.Duration) // content opening latency callback
	// TransportError is required when either exchange fails. Its mapped error is
	// returned after admitted activity is released. Returning nil also yields nil
	// content, so callers retain their own marker for a handled transport failure.
	TransportError func(error) error // transport failure mapping callback
	// HTTPError handles status failures and content failures other than 416.
	// It runs before body closure. Returning nil yields nil content and nil error,
	// so callers retain their own marker when this callback already handled the response.
	HTTPError func(*http.Response, string) error // HTTP failure mapping callback
	// Enter optionally admits download activity. Successful admission returns a
	// release callback transferred to VideoContent.Close; if admission itself
	// fails, Enter owns cleanup.
	Enter func() (func(), error) // optional download activity admission
}

// VideoContent exposes one download body together with its status evidence
// and close-owned activity.
type VideoContent struct {
	// StatusBody retains bytes returned by ReadStatus for completion/billing
	// consumers. The resource does not clone them, so callers coordinate any
	// mutation of shared storage.
	StatusBody []byte `json:"-"` // task status response bytes
	// Headers is the download response's map, returned without cloning. Callers
	// copy it before independent mutation if another response owner retains it.
	Headers    http.Header `json:"-"` // content response header map
	StatusCode int         // content response HTTP status
	// ContentLength keeps the response's reported byte count, including -1 for
	// unknown length. Forwarders retain that value rather than inventing a zero-
	// length body.
	ContentLength int64 // reported download length bytes
	// RequestID prefers the content response's x-request-id or xai-request-id
	// before the status response's identifier. It is separate from the video
	// task identifier.
	RequestID string // upstream exchange request identifier
	// body and done move from OpenVideoContent into the returned resource. Close
	// invokes body closure before activity release and once/closeErr retain that
	// outcome across repeated calls; callers close successful resources after
	// consumption.
	body     io.ReadCloser // opened download response body
	done     func()        // successful activity release callback
	once     sync.Once     // close operation coordination state
	closeErr error         // first body-close error result
}

// String returns a fixed video-options label that omits endpoint and
// credential values.
func (VideoContentOptions) String() string { return "grok video content options" }

// GoString returns the same credential-free video-options label for Go
// formatting.
func (o VideoContentOptions) GoString() string { return o.String() }

// Read delegates to the opened download body and returns its read result. It
// performs no automatic Close or activity release at EOF; callers keep the
// resource usable and close it after consumption finishes.
func (v *VideoContent) Read(data []byte) (int, error) { return v.body.Read(data) }

// Close closes the body and then releases successful download activity once.
// Repeated calls return the stored close error, and nil receivers succeed. A
// body-close error still invokes done; callers finish body use before closing
// according to the underlying stream's requirements.
func (v *VideoContent) Close() error {
	if v == nil {
		return nil
	}
	v.once.Do(func() {
		// Closing the stream precedes releasing activity, including on close error;
		// repeated Close calls reuse this result and do not release the same activity again.
		v.closeErr = v.body.Close()
		if v.done != nil {
			v.done()
		}
	})
	return v.closeErr
}

// String returns a fixed video-stream label that omits response and
// credential details.
func (*VideoContent) String() string { return "grok video content stream" }

// GoString returns the same credential-free video-stream label for Go
// formatting.
func (v *VideoContent) GoString() string { return v.String() }

// OpenVideoContent fetches task status and opens a signed or authenticated
// content stream. Supply Context, ApplyHeaders, Do, ReadStatus, Latency,
// TransportError, and HTTPError; ContentURL is required when status yields no
// signed content URL. The transport must expose redirects without following
// them, and return response bodies for successful exchanges.
//
// Status bytes are read and closed before content lookup. A recognized signed
// URL receives no bearer token or provider overrides; the relay does. Both
// paths retain Range. Content redirects fail, while HTTP 416 is a readable
// resource. If an error mapper returns nil after handling a failure, this
// opener returns nil content and nil error; callers track that handled
// outcome. A returned VideoContent owns its body and activity until Close,
// including after EOF. Failed opens release activity here. Headers and status
// bytes are returned directly rather than cloned.
func OpenVideoContent(ctx context.Context, options VideoContentOptions) (content *VideoContent, failure error) {
	var done func()
	if options.Enter != nil {
		var err error
		done, err = options.Enter()
		if err != nil {
			return nil, err
		}
	}
	defer func() {
		// Opening failure after admission cannot hand a resource to the caller.
		// Release activity here; successful opens move that obligation to VideoContent.Close.
		if content == nil && done != nil {
			done()
		}
	}()
	statusReq, err := http.NewRequestWithContext(options.Context(ctx), http.MethodGet, options.StatusURL, nil)
	if err != nil {
		return nil, err
	}
	statusReq.Header.Set("Authorization", "Bearer "+options.Token)
	statusReq.Header.Set("Accept", "application/json")
	options.ApplyHeaders(statusReq.Header, options.StatusURL)
	started := time.Now()
	latency := func() { options.Latency(time.Since(started)) }
	statusResp, err := options.Do(statusReq)
	if err != nil {
		latency()
		return nil, options.TransportError(err)
	}
	statusRequestID := firstNonEmpty(statusResp.Header.Get("x-request-id"), statusResp.Header.Get("xai-request-id"))
	if statusResp.StatusCode >= 300 {
		defer func() { _ = statusResp.Body.Close() }()
		latency()
		if statusResp.StatusCode < 400 {
			return nil, fmt.Errorf("grok media status redirect is not allowed")
		}
		return nil, options.HTTPError(statusResp, statusRequestID)
	}
	// Finish and close status lookup before opening the download, keeping its
	// response resource separate from the content body later transferred to the caller.
	statusBody, err := options.ReadStatus(statusResp.Body)
	_ = statusResp.Body.Close()
	if err != nil {
		latency()
		return nil, err
	}
	contentURL, err := (MediaCodec{}).GrokMediaSignedVideoContentURL(statusBody, options.RequestID)
	if err != nil {
		latency()
		return nil, err
	}
	signedContent := contentURL != ""
	// Missing or relay-shaped status URLs use the caller's authenticated content
	// route; the status helper has already rejected unsupported signed URL destinations.
	if !signedContent {
		contentURL, err = options.ContentURL()
		if err != nil {
			latency()
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(options.Context(ctx), http.MethodGet, contentURL, nil)
	if err != nil {
		latency()
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	if header := strings.TrimSpace(options.Range); header != "" {
		req.Header.Set("Range", header)
	}
	// Signed content needs its URL credentials alone. Provider tokens and header
	// overrides accompany the authenticated relay path, preventing their transfer to signed downloads.
	if !signedContent {
		req.Header.Set("Authorization", "Bearer "+options.Token)
		options.ApplyHeaders(req.Header, contentURL)
	}
	resp, err := options.Do(req)
	latency()
	if err != nil {
		return nil, options.TransportError(err)
	}
	requestID := firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id"), statusRequestID)
	// The caller's transport leaves redirects visible. Close this body before
	// rejecting a location so download ownership never transfers to another unchecked target.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("grok media signed content redirect is not allowed")
	}
	// A 416 is a content-range response that the forwarding caller can expose.
	// Other HTTP failures go through its error mapper while this opener closes their bodies.
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		defer func() { _ = resp.Body.Close() }()
		return nil, options.HTTPError(resp, requestID)
	}
	return &VideoContent{
		StatusBody:    statusBody,
		Headers:       resp.Header,
		StatusCode:    resp.StatusCode,
		ContentLength: resp.ContentLength,
		RequestID:     requestID,
		body:          resp.Body,
		done:          done,
	}, nil
}
