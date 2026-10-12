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

// Target contains transport parameters for one selected provider request.
type Target struct {
	ProviderID   int64
	URL, Model   string
	Token        string `json:"-"`
	ReadLimit    int64
	Enter        func() (func(), error)
	ApplyHeaders func(http.Header)
	Do           func(*http.Request) (*http.Response, error)
	WriteHeaders func(http.Header, http.Header)
}

// HTTPError carries upstream status, headers, and body for gateway error policy.
type HTTPError struct {
	Status int
	Header http.Header
	Body   []byte
}

// Executor executes one SystemOne request and records usage validity separately from answer validity.
type Executor struct{}

// TargetID returns the selected provider ID.
func (t *Target) TargetID() int64 { return t.ProviderID }

// String exposes the provider ID for diagnostics while omitting request credentials.
func (t *Target) String() string { return fmt.Sprintf("jev target provider=%d", t.ProviderID) }

// GoString uses the same redacted description as String.
func (t *Target) GoString() string { return t.String() }

// Error returns an HTTP status description suitable for diagnostic logs.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("SystemOne upstream returned HTTP %d", e.Status)
}

// EndpointURL uses shared OpenAI URL rules for root and version-prefixed bases.
func EndpointURL(base, endpoint string) string {
	return httpclient.BuildOpenAIEndpointURL(base, "/v1/"+endpoint)
}

// RetryAfterResetTime returns a future retry time from Retry-After, or nil for invalid values.
func RetryAfterResetTime(headers http.Header, now time.Time) *time.Time {
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds >= float64(math.MaxInt64)/float64(time.Second) {
			return nil
		}
		reset := now.Add(time.Duration(seconds * float64(time.Second)))
		return &reset
	}
	if reset, err := http.ParseTime(raw); err == nil && reset.After(now) {
		return &reset
	}
	return nil
}

// Execute buffers the answer before delivery and returns usage for gateway settlement decisions.
func (Executor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
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
	defer func() { _ = response.Body.Close() }()
	limit := target.ReadLimit
	if limit <= 0 {
		limit = 16 << 20
	}
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
	if result.Model == "" {
		result.Model = request.Model
	}
	result.UpstreamModel = target.Model
	result.UpstreamResponseModel = decoded.Model
	result.RequestID = response.Header.Get("x-request-id")
	if result.RequestID == "" {
		result.RequestID = response.Header.Get("request-id")
	}
	result.UpstreamHeaders = response.Header.Clone()
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
	result.RetryCommitted = true
	if err = sink.Begin(upstream.OutputHead{Status: response.StatusCode, Header: header}); err == nil {
		result.HTTPCommitted = true
		err = sink.Emit(upstream.OutputEvent{Data: body, Semantic: true, CommitForRetry: true, Terminal: true})
	}
	result.ClientDisconnect = err != nil
	return result, err
}
