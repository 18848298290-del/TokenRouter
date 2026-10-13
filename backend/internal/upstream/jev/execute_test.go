package jev

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/systemone"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// A Jev response can need client alias restoration while retaining the vendor's
// actual version and usage observations. The fixtures compare buffered bodies
// and failing sink writes, and check that HTTP failures expose policy data
// before any answer bytes reach the sink.

// outputProbe records emitted bytes and can report a downstream write failure.
type outputProbe struct {
	body []byte // accumulated emitted answer bytes
	fail bool   // simulate payload write failure
}

// TestExecuteResponseModelOverride supplies one vendor version with optional
// client aliases. It checks the selected and declared upstream identities, the
// top-level response model, and retention of an unrelated version string.
func TestExecuteResponseModelOverride(t *testing.T) {
	response := `{"model":"jev-1.13.0","answers":{"available":{"type":"noul","noul":0.9}},"usage":{"input_tokens":10,"output_tokens":0},"extra":"jev-1.13.0"}`
	for _, responseModel := range []string{"", "jev-latest", "TS/jev-latest"} {
		t.Run(responseModel, func(t *testing.T) {
			target := &Target{Model: "jev-latest", URL: "https://example.com/v1/systemone", Do: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			}}
			result, err := (Executor{}).Execute(context.Background(), upstream.AttemptInput{Protocol: protocol.ProtocolSystemOne, Body: systemone.ProbeBody("jev-latest", ""), ResponseModel: responseModel, Target: target}, nil)
			require.NoError(t, err)
			require.Equal(t, "jev-latest", result.UpstreamModel)
			require.Equal(t, "jev-1.13.0", result.UpstreamResponseModel)
			want := "jev-1.13.0"
			if responseModel != "" {
				want = responseModel
			} else {
				require.Equal(t, response, string(result.MediaBody))
			}
			require.Contains(t, string(result.MediaBody), `"model":"`+want+`"`)
			require.Contains(t, string(result.MediaBody), `"extra":"jev-1.13.0"`)
		})
	}
}

// TestRetryAfterResetTime compares equivalent numeric seconds and a future HTTP
// date against a fixed clock. It also checks rejection of invalid, nonpositive,
// nonfinite, oversized, and past values.
func TestRetryAfterResetTime(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	for _, value := range []string{"17", now.Add(17 * time.Second).Format(http.TimeFormat)} {
		reset := RetryAfterResetTime(http.Header{"Retry-After": []string{value}}, now)
		require.NotNil(t, reset)
		require.Equal(t, now.Add(17*time.Second), *reset)
	}
	for _, value := range []string{"", "invalid", "-1", "0", "NaN", "Inf", "1e100", now.Add(-time.Hour).Format(http.TimeFormat)} {
		require.Nil(t, RetryAfterResetTime(http.Header{"Retry-After": []string{value}}, now), value)
	}
}

// Begin accepts response metadata so the probe can exercise payload emission.
func (s *outputProbe) Begin(upstream.OutputHead) error { return nil }

// Emit appends the event bytes and optionally returns a client-disconnect
// error. The accumulated bytes remain available even when the simulated write
// fails, allowing assertions about already-prepared answer content.
func (s *outputProbe) Emit(e upstream.OutputEvent) error {
	// Copy event bytes before returning from Emit so the probe retains them
	// for assertions after the executor's synchronous sink call.
	s.body = append(s.body, e.Data...)
	if s.fail {
		return errors.New("client disconnected")
	}
	return nil
}

// TestExecuteKeepsUsageAfterClientWriteFailure sends a real local HTTP request
// with configured headers and exercises successful and failing emits. It checks
// retained usage and declared upstream model alongside client alias restoration
// and unrelated answer data.
func TestExecuteKeepsUsageAfterClientWriteFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/systemone", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		require.Equal(t, "configured", r.Header.Get("X-Test"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Contains(t, string(body), `"model":"jev-latest"`)
		w.Header().Set("X-Request-Id", "upstream-test")
		_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{"available":{"type":"noul","noul":0.9}},"usage":{"input_tokens":100,"output_tokens":20},"extra":"kept"}`)
	}))
	// Close the local fixture after both sink cases finish so its listener
	// and connections are released even when an assertion fails.
	defer server.Close()
	for _, fail := range []bool{false, true} {
		sink := &outputProbe{fail: fail}
		target := &Target{ProviderID: 1, Model: "jev-latest", URL: EndpointURL(server.URL, "systemone"), Token: "test-key", Do: server.Client().Do, ApplyHeaders: func(h http.Header) { h.Set("X-Test", "configured") }}
		result, err := (Executor{}).Execute(context.Background(), upstream.AttemptInput{Protocol: protocol.ProtocolSystemOne, Body: systemone.ProbeBody("jev-latest", ""), ResponseModel: "client-alias", Target: target}, sink)
		require.Equal(t, fail, err != nil)
		require.True(t, result.Served)
		require.True(t, result.HasUsage)
		require.Equal(t, 100, result.Usage.InputTokens)
		require.Equal(t, "jev-1.13.0", result.UpstreamResponseModel)
		require.Contains(t, string(sink.body), `"model":"client-alias"`)
		require.Contains(t, string(sink.body), `"extra":"kept"`)
	}
}

// TestExecuteInvalidUsageStillServesAnswers supplies valid answer JSON with no
// usage field. It checks successful sink delivery and Served=true while
// HasUsage remains false, keeping answer completion separate from accounting
// availability.
func TestExecuteInvalidUsageStillServesAnswers(t *testing.T) {
	sink := &outputProbe{}
	target := &Target{Model: "jev-latest", URL: "https://example.com/v1/systemone", Do: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"jev-latest","answers":{"available":{"type":"noul","noul":1}}}`))}, nil
	}}
	r, err := (Executor{}).Execute(context.Background(), upstream.AttemptInput{Protocol: protocol.ProtocolSystemOne, Body: systemone.ProbeBody("jev-latest", ""), Target: target}, sink)
	require.NoError(t, err)
	require.True(t, r.Served)
	require.False(t, r.HasUsage)
	require.NotEmpty(t, sink.body)
}

// TestExecuteHTTPFailureAndResponseLimit supplies five unsuccessful HTTP
// statuses with a Retry-After header. It checks buffered HTTPError metadata,
// empty sink output, and endpoint construction for root and versioned bases.
func TestExecuteHTTPFailureAndResponseLimit(t *testing.T) {
	for _, status := range []int{401, 422, 429, 529, 503} {
		target := &Target{URL: "https://example.com/v1/systemone", Do: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"17"}}, Body: io.NopCloser(strings.NewReader(`{"error":"busy"}`))}, nil
		}}
		sink := &outputProbe{}
		r, err := (Executor{}).Execute(context.Background(), upstream.AttemptInput{Protocol: protocol.ProtocolSystemOne, Body: systemone.ProbeBody("jev-latest", ""), Target: target}, sink)
		var failure *HTTPError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, status, failure.Status)
		require.Equal(t, "17", failure.Header.Get("Retry-After"))
		require.False(t, r.Served)
		require.Empty(t, sink.body)
	}
	for _, base := range []string{"https://example.com", "https://example.com/v1", "https://example.com/v1/"} {
		require.Equal(t, "https://example.com/v1/systemone", EndpointURL(base, "systemone"))
	}
}
