package grok

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// voice_execute.go executes Grok Voice HTTP requests and returns response
// output with estimated audio usage.
// Grok Voice HTTP operations can return audio bytes or JSON, but usage depends
// on the selected base endpoint. Response policy runs before body output and
// audio estimation, so handled HTTP errors produce no local meter while accepted
// responses retain metering after a client-write failure.

// VoiceTarget supplies a prepared Voice request and synchronous
// response-processing callbacks.
type VoiceTarget struct {
	ProviderID int64 // selected provider identifier
	// Endpoint, BaseEndpoint and ContentType describe the requested route, its
	// metering operation and input encoding. Execute uses BaseEndpoint for model
	// fields and audio estimation; Endpoint is retained for the caller's route
	// identity. Endpoint: requested Voice path; BaseEndpoint: audio metering
	// operation; ContentType: original request encoding
	Endpoint, BaseEndpoint, ContentType string
	// Request.Context carries transport cancellation; Execute receives the
	// already-built request and does not replace that context.
	Request *http.Request `json:"-"` // prepared upstream request
	// Do supplies a response with a readable body when the final AfterExchange
	// error is nil; cleanup begins after that exchange is accepted.
	Do func(*http.Request) (*http.Response, error) // required HTTP exchange
	// Enter returns a callable release after registration; body cleanup precedes
	// the deferred release at attempt exit.
	Enter func() (func(), error) // optional activity registration
	// AfterExchange receives elapsed HTTP-call time and transport error; its
	// returned error controls whether response processing proceeds.
	AfterExchange func(time.Duration, error) error // optional exchange observation
	// BeforeResponse returns handled or error to stop normal reading and audio
	// metering; hooks own cleanup of any body they replace.
	BeforeResponse func(*http.Response) (bool, error) // optional response policy
	// ReadBody consumes the accepted response using the caller's size policy;
	// returned bytes then supply client output and audio-usage estimation.
	ReadBody func(io.Reader) ([]byte, error) // required response reader
	// CopyHeaders selects output headers before body emission; its source is the
	// upstream map also retained in the attempt result.
	CopyHeaders func(http.Header, http.Header) // optional response headers
}

// VoiceExecutor exchanges one Grok TTS, STT or custom-Voice request.
type VoiceExecutor struct{}

// TargetID returns the selected provider identifier for Voice attempt
// accounting.
func (t *VoiceTarget) TargetID() int64 { return t.ProviderID }

// String returns a fixed diagnostic label for Grok Voice targets.
func (*VoiceTarget) String() string { return "grok voice target" }

// GoString returns the Voice target's fixed label for formatted diagnostics.
func (t *VoiceTarget) GoString() string { return t.String() }

// Execute reads and optionally emits an accepted Voice body, then estimates its
// audio usage. The prepared Request carries cancellation and Enter returns a
// callable activity release. BeforeResponse can finish error policy before
// output or metering; accepted bodies close on return. Client-write failure
// records ClientDisconnect while usage estimation still runs, and custom-Voice
// operations return no audio meter. Returned UpstreamHeaders shares the
// response map; copy it before changing retained header values.
func (VoiceExecutor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
	defer func() { result.CapturePerformance(ctx, failure) }()
	target, ok := input.Target.(*VoiceTarget)
	if !ok || target == nil || target.Do == nil || target.Request == nil || target.ReadBody == nil {
		return result, errors.New("grok voice target is not configured")
	}
	switch input.Protocol {
	case protocol.ProtocolTTS, protocol.ProtocolSTT, protocol.ProtocolCustomVoices:
	default:
		return result, errors.New("unsupported grok voice protocol")
	}
	if target.Enter != nil {
		done, err := target.Enter()
		if err != nil {
			return result, err
		}
		// Keep activity registered until the current response body is closed,
		// including early policy returns and body-read failures.
		defer done()
	}
	started := time.Now()
	result.Model = target.BaseEndpoint
	result.UpstreamModel = target.BaseEndpoint
	resp, err := target.Do(target.Request)
	if target.AfterExchange != nil {
		err = target.AfterExchange(time.Since(started), err)
	}
	if err != nil {
		return result, err
	}
	// Close the body present at return after response hooks and readers finish;
	// replaced bodies require their owner's cleanup.
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
	if sink != nil {
		output := upstream.NewOutputContext(sink)
		if target.CopyHeaders != nil {
			target.CopyHeaders(output.Writer.Header(), resp.Header)
		}
		contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
		if contentType == "" {
			contentType = "application/json"
		}
		output.NextEvent(len(data) > 0, true)
		output.Data(resp.StatusCode, contentType, data)
		result.ClientDisconnect = output.Err() != nil
	}
	result.RequestID = firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id"))
	result.UpstreamHeaders = resp.Header
	// Accepted Voice output remains meterable after a client-write failure;
	// BaseEndpoint chooses character units for TTS or hours for STT.
	result.AudioUsage = EstimateGrokVoiceAudioUsage(target.BaseEndpoint, input.Body, target.ContentType, data, time.Since(started))
	result.HasUsage = result.AudioUsage != nil
	result.Served = len(data) > 0
	result.Duration = time.Since(started)
	return result, nil
}
