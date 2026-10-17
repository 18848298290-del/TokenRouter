package jev

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/systemone"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// execute.go exchanges SystemOne requests and records validated answers
// separately from usage and delivery observations.
// SystemOne answers can be valid even when their token usage is absent or
// malformed. The executor buffers the complete bounded response and validates
// answers before beginning downstream output, then retains usage validity as
// separate evidence for gateway completion. An HTTP failure returns its status,
// cloned headers, and buffered body so the gateway can apply provider policy.
//
// Client-facing model aliases can differ from both the selected upstream model
// and the version declared in the answer. The result keeps those identities
// separately while the body rewrite changes the top-level response model.
// Served and usage evidence are recorded before sink delivery, so a downstream
// write failure can still carry a valid completed answer and accounting
// observations back to the caller.

// Target supplies the selected provider's request endpoint, model identity, and
// transport callbacks for one buffered SystemOne exchange.
type Target struct {
	ProviderID int64 // selected upstream provider identifier
	// URL and Model describe the already-selected exchange. Callers put the
	// selected model into Body before Execute; Model records that identity
	// for completion independently of the model declared by the response.
	// URL: resolved SystemOne request endpoint. Model: selected upstream
	// model name.
	URL, Model string
	Token      string `json:"-"` // provider API credential
	// ReadLimit bounds buffered response bytes. Nonpositive values select
	// sixteen MiB; the read reserves an extra byte to detect an oversized
	// body. Callers keep positive limits below MaxInt64 so that size probe
	// fits.
	ReadLimit int64
	// Enter optionally admits the exchange to the caller's activity
	// tracking. A successful call returns a nonnil release function,
	// invoked on every subsequent exit after response cleanup when a body
	// was acquired.
	Enter func() (func(), error)
	// ApplyHeaders optionally edits the outbound headers after Bearer and
	// JSON headers are set, so permitted overrides use the caller's policy.
	ApplyHeaders func(http.Header)
	// Do sends the request. A nil error supplies a nonnil response with a
	// usable body, which Execute closes after bounded reading and output.
	Do func(*http.Request) (*http.Response, error)
	// WriteHeaders optionally selects upstream headers into a new output
	// map. Execute sets the JSON Content-Type after this callback finishes
	// copying.
	WriteHeaders func(http.Header, http.Header)
}

// HTTPError carries a buffered upstream HTTP failure for gateway policy and
// diagnostic handling.
type HTTPError struct {
	Status int // received HTTP status code
	// Header holds Execute's clone of upstream response headers, allowing
	// gateway policy to inspect them after the response body has been
	// closed.
	Header http.Header
	Body   []byte // bounded upstream error bytes
}

// Executor performs one buffered SystemOne exchange and records answer validity
// separately from usage validity.
type Executor struct{}

// TargetID returns the provider identifier attached to this selected target.
func (t *Target) TargetID() int64 { return t.ProviderID }

// String formats the selected provider identifier for diagnostic output.
func (t *Target) String() string { return fmt.Sprintf("jev target provider=%d", t.ProviderID) }

// GoString returns String's provider identifier description for Go-syntax
// formatting.
func (t *Target) GoString() string { return t.String() }

// Error describes the buffered upstream failure by its HTTP status.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("SystemOne upstream returned HTTP %d", e.Status)
}

// EndpointURL builds a SystemOne path using the shared OpenAI endpoint rules.
// Pass the unprefixed endpoint name; the builder handles root bases and bases
// that already contain a recognized version suffix.
func EndpointURL(base, endpoint string) string {
	return httpclient.BuildOpenAIEndpointURL(base, "/v1/"+endpoint)
}

// RetryAfterResetTime interprets Retry-After as positive finite seconds or a
// future HTTP date relative to now. Invalid, nonpositive, and duration-sized
// overflow values return nil; numeric fractions convert to nanoseconds with
// fractional nanoseconds truncated.
func RetryAfterResetTime(headers http.Header, now time.Time) *time.Time {
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds >= float64(math.MaxInt64)/float64(time.Second) {
			return nil
		}
		// Convert the finite seconds delay to nanoseconds after the
		// range check; fractional nanoseconds truncate when forming the
		// duration used by cooldown consumers.
		reset := now.Add(time.Duration(seconds * float64(time.Second)))
		return &reset
	}
	if reset, err := http.ParseTime(raw); err == nil && reset.After(now) {
		return &reset
	}
	return nil
}

// Execute validates and buffers one SystemOne answer, then returns execution,
// model, and usage observations even when downstream delivery fails. It
// requires a configured Target with a successful Do response containing a
// usable body, and accepts only the SystemOne protocol. A nil sink returns the
// answer in MediaBody; a nonnil sink receives one JSON response event.
//
// Answer validation and usage validity are independent. A validated answer sets
// Served before delivery, while HasUsage comes from the codec's separate token
// checks. A configured response alias is applied to the body after the actual
// upstream response model has been recorded, letting completion inspect both
// identities.
//
// Nonpositive ReadLimit selects sixteen MiB. The response body and optional
// Enter release are closed on exit after successful acquisition. RetryCommitted
// is set before Begin, HTTPCommitted follows a successful Begin, and either
// sink failure sets ClientDisconnect while retaining the observed answer data.
func (Executor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
	defer func() { result.CapturePerformance(ctx, failure) }()
	target, ok := input.Target.(*Target)
	if !ok || target == nil || target.Do == nil || input.Protocol != protocol.ProtocolSystemOne {
		return result, errors.New("SystemOne target is not configured")
	}
	request, err := systemone.ParseRequest(input.Body)
	if err != nil {
		return result, err
	}
	if target.Enter != nil {
		done, enterErr := target.Enter()
		if enterErr != nil {
			return result, enterErr
		}
		// Keep the activity admission until parsing and downstream
		// delivery finish, including failures, so caller shutdown
		// tracking covers the complete exchange.
		defer done()
	}
	started := time.Now()
	defer func() {
		result.Duration = time.Since(started)
		result.Cancelled = errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(input.Body))
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+target.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if target.ApplyHeaders != nil {
		target.ApplyHeaders(req.Header)
	}
	response, err := target.Do(req)
	if err != nil {
		result.FailureClass = "transport"
		return result, err
	}
	// Release the transport's response ownership on every later exit,
	// including read or decode failure and client write failure; close
	// errors do not replace the exchange result.
	defer func() { _ = response.Body.Close() }()
	limit := target.ReadLimit
	if limit <= 0 {
		limit = 16 << 20
	}
	// Read one byte beyond the configured limit so an oversized answer is
	// rejected before output rather than delivering a silently truncated
	// JSON document.
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return result, err
	}
	if int64(len(body)) > limit {
		return result, errors.New("SystemOne response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, &HTTPError{Status: response.StatusCode, Header: response.Header.Clone(), Body: body}
	}
	decoded, err := systemone.ParseResponse(body, request.Questions)
	if err != nil {
		return result, err
	}
	result.Served = true
	result.HasUsage = decoded.HasUsage
	result.Usage = decoded.Usage
	result.Model = input.ResponseModel
	// When no client alias was supplied, retain the request's model as the
	// completion identity; the response's declared version stays in its
	// separate observation.
	if result.Model == "" {
		result.Model = request.Model
	}
	result.UpstreamModel = target.Model
	result.UpstreamResponseModel = decoded.Model
	result.RequestID = response.Header.Get("x-request-id")
	// Accept the alternate upstream correlation header when x-request-id is
	// absent, keeping that external identifier available for gateway
	// diagnostics.
	if result.RequestID == "" {
		result.RequestID = response.Header.Get("request-id")
	}
	result.UpstreamHeaders = response.Header.Clone()
	// Apply the client alias only after recording the declared upstream
	// model, so response presentation cannot erase the version used by
	// completion and request diagnostics.
	if input.ResponseModel != "" && input.ResponseModel != decoded.Model {
		body, err = systemone.ReplaceModel(body, input.ResponseModel)
		if err != nil {
			return result, err
		}
	}
	if sink == nil {
		result.MediaBody = body
		return result, nil
	}
	header := make(http.Header)
	if target.WriteHeaders != nil {
		target.WriteHeaders(header, response.Header)
	}
	header.Set("Content-Type", "application/json")
	// Record retry closure before calling the sink. A failed Begin or Emit
	// still returns served-answer and usage evidence for the gateway's
	// completion choices.
	result.RetryCommitted = true
	if err = sink.Begin(upstream.OutputHead{Status: response.StatusCode, Header: header}); err == nil {
		result.HTTPCommitted = true
		err = sink.Emit(upstream.OutputEvent{Data: body, Semantic: true, CommitForRetry: true, Terminal: true})
	}
	result.ClientDisconnect = err != nil
	return result, err
}
