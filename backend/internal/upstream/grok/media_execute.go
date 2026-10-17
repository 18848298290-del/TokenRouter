package grok

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// media_execute.go executes Grok image and video requests and checks image
// output before delivery.
// Successful Grok image responses need observable image data before the gateway
// emits a response. Media execution checks that output before applying a body
// transformation, and returns MissingImageOutput to caller policy when an
// accepted HTTP response contains no counted images.

// MediaTarget provides a prepared media request and synchronous
// response-processing callbacks.
type MediaTarget struct {
	ProviderID int64 // selected provider identifier
	// Endpoint controls the image-count check independently of the accepted input
	// protocol; configure CountImages for image generation or editing.
	Endpoint GrokMediaEndpoint // selected media operation
	// Request owns the exchange context and body; the separate Execute context does
	// not replace this prepared request's cancellation policy.
	Request *http.Request `json:"-"` // prepared upstream request
	// StartedAt anchors final Duration at the caller's chosen start, which can
	// include preparation; AfterExchange measures the current HTTP call separately.
	StartedAt time.Time // supplied attempt start
	// Do supplies a response with a readable body when the final AfterExchange
	// error is nil; cleanup begins after that exchange is accepted.
	Do func(*http.Request) (*http.Response, error) // required HTTP exchange
	// Enter returns a callable release function after registration; Execute defers
	// it through response processing and body closure.
	Enter func() (func(), error) // optional activity registration
	// AfterExchange receives HTTP-call duration and transport error; its returned
	// error determines whether the response is accepted for cleanup and reading.
	AfterExchange func(time.Duration, error) error // optional exchange observation
	// BeforeResponse stops normal reading when handled or error is returned;
	// replacement of a live body requires cleanup of the displaced body.
	BeforeResponse func(*http.Response) (bool, error) // optional response policy
	// ReadBody consumes the accepted response using the caller's size policy;
	// returned bytes stay available for image checks, transformation and output.
	ReadBody func(io.Reader) ([]byte, error) // required response reader
	// CountImages examines original response bytes before TransformBody runs and
	// decides whether image output is sufficient.
	CountImages func([]byte) int // image output counter
	// TransformBody runs after image validation; its returned bytes are shared by
	// client output and MediaBody, so keep them stable through both consumers.
	TransformBody func([]byte) []byte // optional body conversion
	// CopyHeaders selects output headers before body emission; its source is the
	// upstream map also retained in the attempt result.
	CopyHeaders func(http.Header, http.Header) // optional response headers
}

// MissingImageOutput carries an image success response that contains no counted
// outputs.
type MissingImageOutput struct {
	// Body retains the bytes returned by ReadBody; caller policy can inspect them
	// when choosing failover after an empty image result.
	Body []byte // original response bytes
	// Headers is cloned from the accepted upstream response so caller failure
	// handling can retain an independent header map.
	Headers http.Header // cloned response headers
}

// MediaExecutor exchanges one prepared Grok image or video request.
type MediaExecutor struct{}

// TargetID returns the selected provider identifier for media attempt
// accounting.
func (t *MediaTarget) TargetID() int64 { return t.ProviderID }

// String returns a fixed diagnostic label for Grok media targets.
func (*MediaTarget) String() string { return "grok media target" }

// GoString returns the media target's fixed label for formatted diagnostics.
func (t *MediaTarget) GoString() string { return t.String() }

// Error returns the stable missing-image message used by caller failure policy.
func (*MissingImageOutput) Error() string { return "xAI upstream returned no image output" }

// Execute reads a prepared media response, checks image outputs, and optionally
// emits its transformed body. Request.Context carries cancellation; Enter
// supplies a callable release function. Accepted responses are closed after
// processing, and BeforeResponse can end the attempt before reading. Image
// endpoints require CountImages; a nonpositive count returns original bytes and
// cloned headers before output. Duration uses StartedAt, while AfterExchange
// measures this HTTP call. Successful results retain shared response headers
// and the emitted byte storage; copy either before independent mutation.
func (MediaExecutor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
	defer func() { result.CapturePerformance(ctx, failure) }()
	target, ok := input.Target.(*MediaTarget)
	if !ok || target == nil || target.Request == nil || target.Do == nil || target.ReadBody == nil {
		return result, errors.New("grok media target is not configured")
	}
	switch input.Protocol {
	case protocol.ProtocolImagesGenerations, protocol.ProtocolImagesEdits, protocol.ProtocolVideosGenerations, protocol.ProtocolVideosEdits, protocol.ProtocolVideosExtensions:
	default:
		return result, errors.New("unsupported grok media protocol")
	}
	if target.Enter != nil {
		done, err := target.Enter()
		if err != nil {
			return result, err
		}
		// Keep activity registered through reading and output; deferred body closure
		// runs before release so transport occupancy ends first.
		defer done()
	}
	started := time.Now()
	resp, err := target.Do(target.Request)
	if target.AfterExchange != nil {
		err = target.AfterExchange(time.Since(started), err)
	}
	if err != nil {
		return result, err
	}
	// Close the current response body at exit after policy, read or output paths
	// have finished using it.
	defer func() { _ = resp.Body.Close() }()
	if target.BeforeResponse != nil {
		handled, err := target.BeforeResponse(resp)
		if handled || err != nil {
			return result, err
		}
	}
	data, err := target.ReadBody(resp.Body)
	if err != nil {
		return result, err
	}
	if target.Endpoint == GrokMediaEndpointImagesGenerations || target.Endpoint == GrokMediaEndpointImagesEdits {
		// Count the untransformed response before committing output so caller policy
		// can switch providers when the image payload is empty.
		result.ObservedImages = target.CountImages(data)
		if result.ObservedImages <= 0 {
			return result, &MissingImageOutput{Body: data, Headers: resp.Header.Clone()}
		}
	}
	if target.TransformBody != nil {
		data = target.TransformBody(data)
	}
	if sink != nil {
		output := upstream.NewOutputContext(sink)
		if target.CopyHeaders != nil {
			target.CopyHeaders(output.Writer.Header(), resp.Header)
		}
		ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
		if ct == "" {
			ct = "application/json"
		}
		output.NextEvent(len(data) > 0, true)
		output.Data(resp.StatusCode, ct, data)
		result.ClientDisconnect = output.Err() != nil
	}
	result.RequestID = firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id"))
	result.UpstreamHeaders = resp.Header
	result.MediaBody = data
	result.Served = len(data) > 0
	// Measure media Duration from the supplied start, which may precede this HTTP
	// call; AfterExchange measures that current exchange independently.
	result.Duration = time.Since(target.StartedAt)
	return result, nil
}

// BuildMediaRequest creates the endpoint's HTTP method and includes bytes only
// when that endpoint requires a body. CLI headers precede Content-Type and
// overrides. Blank body content type selects application/json; callers validate
// targetURL and retain supplied body bytes throughout the exchange.
func BuildMediaRequest(ctx context.Context, endpoint GrokMediaEndpoint, targetURL, token, contentType string, body []byte, cliHeaders, overrides func(http.Header)) (*http.Request, error) {
	var reader io.Reader
	if endpoint.RequiresRequestBody() {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, endpoint.HTTPMethod(), targetURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if cliHeaders != nil {
		cliHeaders(req.Header)
	}
	if endpoint.RequiresRequestBody() {
		ct := strings.TrimSpace(contentType)
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}
	if overrides != nil {
		overrides(req.Header)
	}
	return req, nil
}
