package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// alpha_search.go executes standalone and Responses-backed Alpha Search
// attempts and records successful search output.
// Standalone Alpha Search returns its upstream JSON directly, while a PAT can
// perform search through streaming Responses. The chosen path controls response
// conversion and whether unsuccessful responses contribute quota headers; a
// forwarded error response can therefore be committed without becoming a served
// search.
//
// Execution holds the optional admission release until reading and output are
// complete. Error handlers may replace resp.Body to replay captured bytes, so
// cleanup retains the original body that owns the connection. A successful
// Responses fallback extracts text and citations into the Alpha Search format,
// and the attempt counts one search only after a successful upstream status.

// AlphaSearchTarget holds the prepared search request and synchronous execution
// callbacks.
type AlphaSearchTarget struct {
	ProviderID int64 // selected upstream provider identifier
	// Request is the prepared authenticated HTTP request; its context controls
	// transport execution and its credentials require redacted diagnostic
	// formatting.
	Request *http.Request `json:"-"`
	// ResponsesFallback selects PAT hosted-search conversion; unsuccessful
	// Responses statuses skip quota updates, while successful SSE output becomes
	// standalone-search JSON.
	ResponsesFallback bool
	Model             string // selected upstream model identifier
	// Enter optionally admits execution and returns a release callback; Execute
	// defers that callback after successful admission through response delivery.
	Enter func() (func(), error)
	Do    func(*http.Request) (*http.Response, error) // required HTTP exchange callback
	// Latency observes time until Do returns and runs even on transport failure,
	// before TransportError translates that failure for the caller.
	Latency        func(time.Duration)
	TransportError func(error) error                  // required transport failure translator
	ReadBody       func(io.Reader) ([]byte, error)    // required response byte reader
	HTTPError      func(*http.Response, []byte) error // required upstream status handler
	UpdateQuota    func(http.Header)                  // required quota header observer
	Headers        func(http.Header, http.Header)     // required downstream header copier
}

// AlphaSearchExecutor performs standalone search or the prepared PAT Responses
// fallback.
type AlphaSearchExecutor struct{}

// TargetID returns the selected provider identifier for search execution. A nil
// target reports zero, allowing diagnostic formatting to describe missing
// configuration before request validation rejects it.
func (t *AlphaSearchTarget) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}

// String formats the provider identifier while excluding prepared request
// credentials.
func (t *AlphaSearchTarget) String() string {
	return fmt.Sprintf("openai alpha search target provider=%d", t.TargetID())
}

// GoString returns the target label used for Go-syntax formatting.
func (t *AlphaSearchTarget) GoString() string { return t.String() }

// Execute sends one prepared Alpha Search attempt and emits its response
// through sink. The target supplies all transport, read, health, quota, and
// header callbacks; Enter is optional and its release is deferred when
// admission succeeds. The prepared request owns the context used by Do.
//
// The original response body is closed even if HTTPError substitutes a replay
// body. ResponsesFallback converts successful SSE to search JSON and updates
// quota only on success; standalone responses update quota after accepted error
// handling too. A handled non-success status may commit HTTP output but leaves
// Served false and SearchCount zero, so outer completion logic can distinguish
// delivery from a successful search.
func (AlphaSearchExecutor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
	defer func() { result.CapturePerformance(ctx, failure) }()
	t, ok := input.Target.(*AlphaSearchTarget)
	if !ok || t == nil || t.Request == nil {
		return result, errors.New("openai alpha search target is not configured")
	}
	if input.Protocol != protocol.ProtocolAlphaSearch {
		return result, errors.New("unsupported alpha search protocol")
	}
	if t.Enter != nil {
		done, err := t.Enter()
		if err != nil {
			return result, err
		}
		defer done()
	}
	started := time.Now()
	resp, err := t.Do(t.Request)
	t.Latency(time.Since(started))
	if err != nil {
		return result, t.TransportError(err)
	}
	// HTTPError may replace resp.Body with a replay reader. Retain the original
	// body so cleanup releases the connection owned by this HTTP exchange.
	originalBody := resp.Body
	defer func() { _ = originalBody.Close() }()
	// Capture the named failure before body cleanup completes, so the result
	// records cancellation and upstream failure even when response handling returns early.
	defer func() {
		result.Duration = time.Since(started)
		result.Cancelled = errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded)
		if failure != nil {
			result.FailureClass = "upstream"
		}
	}()
	body, err := t.ReadBody(resp.Body)
	if err != nil {
		if t.ResponsesFallback {
			return result, fmt.Errorf("read alpha search responses fallback response: %w", err)
		}
		return result, fmt.Errorf("read alpha search response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		if err := t.HTTPError(resp, body); err != nil {
			return result, err
		}
	}
	output := upstream.NewDeferredOutputContext(sink)
	success := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
	// PAT Responses errors do not publish inference quota headers; the standalone
	// search path retains its header update after HTTPError accepts the response.
	if !t.ResponsesFallback || success {
		t.UpdateQuota(resp.Header)
	}
	if t.ResponsesFallback && success {
		converted, err := OpenAIAlphaSearchResponseFromResponsesSSE(body)
		if err != nil {
			return result, err
		}
		output.Data(http.StatusOK, "application/json", converted)
	} else {
		t.Headers(output.Writer.Header(), resp.Header)
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			// Missing media types receive the JSON default expected by this endpoint,
			// allowing downstream clients to interpret the forwarded search response bytes.
			contentType = "application/json"
		}
		output.Data(resp.StatusCode, contentType, body)
	}
	result.HTTPCommitted = output.Writer.Written()
	if !success {
		return result, nil
	}
	result.RequestID = strings.TrimSpace(resp.Header.Get("x-request-id"))
	result.CaptureRequestAttempt(resp)
	result.UpstreamHeaders = resp.Header
	result.Model = input.ResponseModel
	result.UpstreamModel = t.Model
	result.SearchCount = 1
	result.Served = true
	return result, nil
}
