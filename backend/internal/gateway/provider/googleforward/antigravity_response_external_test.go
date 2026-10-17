package googleforward_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// antigravityCompatErrorReader supplies a complete payload before its
// configured terminal failure.
type antigravityCompatErrorReader struct {
	data []byte // stream fixture payload bytes
	off  int    // next unread payload offset
	// err is returned after all payload bytes are consumed; nil therefore
	// produces a zero-byte successful read rather than EOF in this fixture.
	err error // post-payload fixture error value
}

// antigravityFailingWriter rejects output after a configurable count of
// forwarded fixture writes.
type antigravityFailingWriter struct {
	// ResponseWriter forwards headers and other writer operations while
	// Write enforces the fixture failure threshold; callers keep the
	// underlying recorder available through response handling.
	gin.ResponseWriter // borrowed HTTP response writer
	// failAfter limits calls forwarded through ResponseWriter; writes
	// counts them before delegation. Nonpositive allowance rejects the
	// first call, and injected failures leave the counter unchanged.
	failAfter int // forwarded write call allowance
	// writes advances before delegated Write, so it counts forwarded
	// attempts even when the underlying response writer returns its own
	// error.
	writes int // forwarded fixture write count
}

// cancelReadCloser returns request cancellation from every stream fixture read.
type cancelReadCloser struct{}

// TestAntigravityCompatEmptyStreamTriggersFailover compares Chat and Responses
// readers against malformed stream data and DONE. Both return same-provider
// retryable failover, with no collected result, response bytes, or content-type
// header.
func TestAntigravityCompatEmptyStreamTriggersFailover(t *testing.T) {
	tests := []struct {
		name string // table scenario name
		// selected protocol reader invocation
		run func(*googleforward.Antigravity, *gin.Context, *http.Response) (*antigravity.StreamResult, error)
	}{
		{
			name: "chat completions",

			run: func(svc *googleforward.Antigravity, c *gin.Context, resp *http.Response) (*antigravity.StreamResult, error) {
				return googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleChatCompletionsStreamingFromAntigravity(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "gemini-3.1-pro-high", true)
			},
		},

		{
			name: "responses",

			run: func(svc *googleforward.Antigravity, c *gin.Context, resp *http.Response) (*antigravity.StreamResult, error) {
				return googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleResponsesStreamingFromAntigravity(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "gemini-3.1-pro-high", bridge.ResponsesClientToolMapping{})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, nil)
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,

				Header: http.Header{"Content-Type": []string{"text/event-stream"}},

				Body: io.NopCloser(strings.NewReader("data: malformed\n\ndata: [DONE]\n\n")),
			}

			result, err := tt.run(svc, c, resp)

			require.Nil(t, result)
			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.RetryableOnSameProvider)
			require.Empty(t, recorder.Body.String())
			require.Empty(t, recorder.Header().Get("Content-Type"))
		})
	}
}

// TestAntigravityCompatUsageOnlyStreamTriggersFailover compares Chat and
// Responses readers against a usage-only stream. Both return retryable failover
// and leave client body/content type empty when no semantic content is
// delivered.
func TestAntigravityCompatUsageOnlyStreamTriggersFailover(t *testing.T) {
	tests := []struct {
		name string // table scenario name
		// selected protocol reader invocation
		run func(*googleforward.Antigravity, *gin.Context, *http.Response) (*antigravity.StreamResult, error)
	}{
		{
			name: "chat completions",

			run: func(svc *googleforward.Antigravity, c *gin.Context, resp *http.Response) (*antigravity.StreamResult, error) {
				return googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleChatCompletionsStreamingFromAntigravity(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "gemini-3.1-pro-high", true)
			},
		},

		{
			name: "responses",

			run: func(svc *googleforward.Antigravity, c *gin.Context, resp *http.Response) (*antigravity.StreamResult, error) {
				return googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleResponsesStreamingFromAntigravity(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "gemini-3.1-pro-high", bridge.ResponsesClientToolMapping{})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, nil)
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,

				Header: http.Header{"Content-Type": []string{"text/event-stream"}},

				Body: io.NopCloser(strings.NewReader(
					`data: {"response":{"responseId":"resp_3757","usageMetadata":{"promptTokenCount":8}}}` + "\n\n",
				)),
			}

			result, err := tt.run(svc, c, resp)

			require.Nil(t, result)
			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.RetryableOnSameProvider)
			require.Empty(t, recorder.Body.String())
			require.Empty(t, recorder.Header().Get("Content-Type"))
		})
	}
}

// TestAntigravityCompatChatStreamMapsToolCallAndUsage reads a wrapped function-
// call payload with terminal usage. The client receives Chat tool calls, tool
// finish reason, usage, and one DONE marker while completion records the
// supplied counts.
func TestAntigravityCompatChatStreamMapsToolCallAndUsage(t *testing.T) {
	svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, nil)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", nil)
	body := `data: {"response":{"responseId":"resp_3757","candidates":[{"content":{"parts":[{"functionCall":{"id":"call_3757","name":"get_weather","args":{"city":"Tokyo"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}` + "\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,

		Header: http.Header{"Content-Type": []string{"text/event-stream"}},

		Body: io.NopCloser(strings.NewReader(body)),
	}

	result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleChatCompletionsStreamingFromAntigravity(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "gemini-3.1-pro-high", true)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 8, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Contains(t, recorder.Body.String(), `"tool_calls"`)
	require.Contains(t, recorder.Body.String(), `"get_weather"`)
	require.Contains(t, recorder.Body.String(), `"finish_reason":"tool_calls"`)
	require.Contains(t, recorder.Body.String(), `"prompt_tokens":8`)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))
}

// TestAntigravityCompatFirstEventTimeoutTriggersFailover keeps the first
// upstream frame unavailable under a one-second interval. The bounded test wait
// observes retryable failover, no stream result, and no content-type commit.
func TestAntigravityCompatFirstEventTimeoutTriggersFailover(t *testing.T) {
	svc := newAntigravityCompatibilityFixture(
		googleforward.Options{MaxLineSize: 500 * 1024 * 1024, StreamInterval: 1},
		nil,
	)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", nil)
	reader, writer := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
	// outcome pairs collected stream observations with the reader error for
	// timeout assertions.
	type outcome struct {
		// result and err describe the same completed reader invocation.
		// The buffered channel publishes both together before the
		// timeout select checks failover and absence of stream
		// observations.
		//
		// collected stream result pointer
		result *antigravity.StreamResult
		err    error // terminal reader error value
	}
	done := make(chan outcome, 1)

	// Run the blocked reader concurrently and publish its result/error pair
	// through a buffered channel so the test can distinguish configured
	// idle timeout from a hung reader.
	go func() {
		result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleChatCompletionsStreamingFromAntigravity(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "gemini-3.1-pro-high", false)
		done <- outcome{result: result, err: err}
	}()

	select {
	case got := <-done:
		require.Nil(t, got.result)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.ErrorAs(t, got.err, &failoverErr)
		require.True(t, failoverErr.RetryableOnSameProvider)
		require.Empty(t, recorder.Header().Get("Content-Type"))
	case <-time.After(2 * time.Second):
		// Close both pipe ends when the bounded wait expires so the
		// blocked fixture reader can return instead of remaining
		// attached to this failed test.
		_ = writer.Close()
		_ = reader.Close()
		t.Fatal("compat stream ignored StreamInterval")
	}
	_ = writer.Close()
	_ = reader.Close()
}

// TestAntigravityCompatClientDisconnectDrainsUsage rejects client writes after
// an initial partial-content frame. The reader continues to terminal usage,
// returns final output count, and marks the client disconnected.
func TestAntigravityCompatClientDisconnectDrainsUsage(t *testing.T) {
	svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, nil)
	c, _ := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer = &antigravityFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
	body := strings.Join([]string{
		`data: {"response":{"responseId":"resp_3757","candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1}}}`,
		"",
		`data: {"response":{"responseId":"resp_3757","candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":15}}}`,
		"",
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,

		Header: http.Header{"Content-Type": []string{"text/event-stream"}},

		Body: io.NopCloser(strings.NewReader(body)),
	}

	result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleChatCompletionsStreamingFromAntigravity(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "gemini-3.1-pro-high", false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 15, result.Usage.OutputTokens)
}

// TestAntigravityCompatStreamErrorCommitsSingleTerminalFrame returns unexpected
// EOF after partial Responses content reaches output. The reader returns error
// with no result and marks semantic commitment while emitting exactly one error
// event.
func TestAntigravityCompatStreamErrorCommitsSingleTerminalFrame(t *testing.T) {
	svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, nil)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/responses", nil)
	body := []byte(`data: {"response":{"responseId":"resp_3757","candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1}}}` + "\n\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,

		Header: http.Header{"Content-Type": []string{"text/event-stream"}},

		Body: &antigravityCompatErrorReader{
			data: body,
			err:  io.ErrUnexpectedEOF,
		},
	}

	result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleResponsesStreamingFromAntigravity(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "gemini-3.1-pro-high", bridge.ResponsesClientToolMapping{})

	require.Error(t, err)
	require.Nil(t, result)
	require.True(t, gatewayhttp.IsResponseCommitted(c))
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: error"))
}

// TestAntigravityCompatKeepaliveAfterFirstEvent leaves a Responses stream idle
// after its first content frame. Client output includes a ping and SSE content
// type, and the reader completes after producer closure.
func TestAntigravityCompatKeepaliveAfterFirstEvent(t *testing.T) {
	svc := newAntigravityCompatibilityFixture(
		googleforward.Options{MaxLineSize: 500 * 1024 * 1024, StreamKeepalive: 1},
		nil,
	)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/responses", nil)
	reader, writer := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
	done := make(chan error, 1)

	// Read concurrently from the pipe while the test writes the first
	// content frame, allowing idle heartbeat output before producer closure
	// completes the response.
	go func() {
		_, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleResponsesStreamingFromAntigravity(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "gemini-3.1-pro-high", bridge.ResponsesClientToolMapping{})
		done <- err
	}()
	_, err := io.WriteString(
		writer,
		`data: {"response":{"responseId":"resp_3757","candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1}}}`+"\n\n",
	)
	require.NoError(t, err)
	time.Sleep(1200 * time.Millisecond)
	require.NoError(t, writer.Close())
	require.NoError(t, <-done)
	require.Contains(t, recorder.Body.String(), ": ping\n\n")
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.NoError(t, reader.Close())
}

// TestHandleGeminiStreamingResponse_NormalComplete feeds native partial content
// followed by terminal usage with cache reads. The result reports final
// input/output/cache buckets, and client output contains
// both text fragments.
func TestHandleGeminiStreamingResponse_NormalComplete(t *testing.T) {
	svc := newAntigravityStreamFixture(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}

	// Feed partial and terminal native payloads through the blocking pipe,
	// then close the producer so final usage replaces earlier cumulative
	// observations before assertions.
	go func() {
		defer func() { _ = pw.Close() }()
		// The first chunk contains partial content.
		fmt.Fprintln(pw, `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}`)
		fmt.Fprintln(pw, "")
		// The last chunk carries final content and complete usage.
		fmt.Fprintln(pw, `data: {"candidates":[{"content":{"parts":[{"text":" world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":8,"cachedContentTokenCount":2}}`)
		fmt.Fprintln(pw, "")
	}()

	result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleGeminiStreamingResponse(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now())
	_ = pr.Close()

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ClientDisconnect, "normal completion should not set clientDisconnect")
	require.NotNil(t, result.Usage)
	// Gemini usage: promptTokenCount=10, candidatesTokenCount=8,
	// cachedContentTokenCount=2 → InputTokens=10-2=8, OutputTokens=8,
	// CacheReadInputTokens=2
	require.Equal(t, 8, result.Usage.InputTokens)
	require.Equal(t, 8, result.Usage.OutputTokens)
	require.Equal(t, 2, result.Usage.CacheReadInputTokens)

	body := rec.Body.String()
	require.Contains(t, body, "Hello")
	require.Contains(t, body, "world")

	require.NotContains(t, body, "event: error")
}

// TestHandleClaudeStreamingResponse_NormalComplete feeds one wrapped terminal
// Gemini payload into Claude conversion. The result reports the supplied token
// counts, and output contains Claude start/stop lifecycle
// events.
func TestHandleClaudeStreamingResponse_NormalComplete(t *testing.T) {
	svc := newAntigravityStreamFixture(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}

	// Feed a wrapped v1internal terminal payload through the pipe so the
	// Claude converter can emit lifecycle events and report the supplied
	// token counts.
	go func() {
		defer func() { _ = pw.Close() }()
		// v1internal wraps Gemini data in the response field. The
		// wrapper supplies the usage metadata expected by the stream
		// processor.
		fmt.Fprintln(pw, `data: {"response":{"candidates":[{"content":{"parts":[{"text":"Hi there"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3}}}`)
		fmt.Fprintln(pw, "")
	}()

	result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleClaudeStreamingResponse(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "claude-sonnet-4-5")
	_ = pr.Close()

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ClientDisconnect, "normal completion should not set clientDisconnect")
	require.NotNil(t, result.Usage)
	// Gemini prompt and candidate counts become Claude input and output
	// counts.
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)

	// The processor emits Claude SSE events.
	body := rec.Body.String()
	require.Contains(t, body, "event: message_start", "should contain Claude message_start event")
	require.Contains(t, body, "event: message_stop", "should contain Claude message_stop event")

	require.NotContains(t, body, "event: error")
}

// TestHandleGeminiStreamingResponse_ThoughtsTokenCount feeds cumulative native
// candidate and thought counts with cache usage. The result subtracts cached
// input from prompt tokens and adds terminal thought tokens to output usage.
func TestHandleGeminiStreamingResponse_ThoughtsTokenCount(t *testing.T) {
	svc := newAntigravityStreamFixture(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}

	// Feed two cumulative native usage payloads before closing the pipe so
	// the terminal candidates and thought counts determine the asserted
	// output usage.
	go func() {
		defer func() { _ = pw.Close() }()
		fmt.Fprintln(pw, `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":50}}`)
		fmt.Fprintln(pw, "")
		fmt.Fprintln(pw, `data: {"candidates":[{"content":{"parts":[{"text":" world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":30,"thoughtsTokenCount":80,"cachedContentTokenCount":10}}`)
		fmt.Fprintln(pw, "")
	}()

	result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleGeminiStreamingResponse(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now())
	_ = pr.Close()

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Usage)
	// promptTokenCount=100, cachedContentTokenCount=10 → InputTokens=90
	require.Equal(t, 90, result.Usage.InputTokens)
	// candidatesTokenCount=30 + thoughtsTokenCount=80 → OutputTokens=110
	require.Equal(t, 110, result.Usage.OutputTokens)
	require.Equal(t, 10, result.Usage.CacheReadInputTokens)
}

// TestHandleClaudeStreamingResponse_ThoughtsTokenCount feeds wrapped terminal
// candidate and thought usage into Claude conversion. The result retains input
// tokens and combines both output components for the asserted count.
func TestHandleClaudeStreamingResponse_ThoughtsTokenCount(t *testing.T) {
	svc := newAntigravityStreamFixture(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}

	// Feed wrapped terminal usage containing candidate and thought tokens
	// so Claude output can project their combined count after reading the
	// complete fixture.
	go func() {
		defer func() { _ = pw.Close() }()
		fmt.Fprintln(pw, `data: {"response":{"candidates":[{"content":{"parts":[{"text":"Hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":10,"thoughtsTokenCount":25}}}`)
		fmt.Fprintln(pw, "")
	}()

	result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleClaudeStreamingResponse(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "gemini-2.5-pro")
	_ = pr.Close()

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Usage)
	// promptTokenCount=50 → InputTokens=50
	require.Equal(t, 50, result.Usage.InputTokens)
	// candidatesTokenCount=10 + thoughtsTokenCount=25 → OutputTokens=35
	require.Equal(t, 35, result.Usage.OutputTokens)
}

// TestHandleGeminiStreamingResponse_ClientDisconnect rejects native client
// writes while supplying content and usage upstream. Reading completes with
// ClientDisconnect set, and the recorder contains no injected write_failed
// error message.
func TestHandleGeminiStreamingResponse_ClientDisconnect(t *testing.T) {
	svc := newAntigravityStreamFixture(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Writer = &antigravityFailingWriter{ResponseWriter: c.Writer, failAfter: 0}

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}

	// Feed native content while the client writer rejects its first write,
	// then close the producer so the response reader can report
	// disconnection after draining.
	go func() {
		defer func() { _ = pw.Close() }()
		fmt.Fprintln(pw, `data: {"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":10}}`)
		fmt.Fprintln(pw, "")
	}()

	result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleGeminiStreamingResponse(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now())
	_ = pr.Close()

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.NotContains(t, rec.Body.String(), "write_failed")
}

// TestHandleGeminiStreamingResponse_ContextCanceled starts with a cancelled
// inbound context and cancellation reader. The result marks disconnection
// without a returned error or an injected native stream error event.
func TestHandleGeminiStreamingResponse_ContextCanceled(t *testing.T) {
	svc := newAntigravityStreamFixture(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)

	resp := &http.Response{StatusCode: http.StatusOK, Body: cancelReadCloser{}, Header: http.Header{}}

	result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleGeminiStreamingResponse(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.NotContains(t, rec.Body.String(), "event: error")
}

// TestHandleClaudeStreamingResponse_ClientDisconnect rejects client writes
// during wrapped Gemini-to-Claude output. Reading completes with a result and
// marks ClientDisconnect after the fixture stream has been drained.
func TestHandleClaudeStreamingResponse_ClientDisconnect(t *testing.T) {
	svc := newAntigravityStreamFixture(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Writer = &antigravityFailingWriter{ResponseWriter: c.Writer, failAfter: 0}

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}

	// Feed wrapped Claude-conversion content after client writes are forced
	// to fail, then close the producer so upstream drain completes before
	// disconnect assertions.
	go func() {
		defer func() { _ = pw.Close() }()
		// v1internal response wrapper.
		fmt.Fprintln(pw, `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":15}}}`)
		fmt.Fprintln(pw, "")
	}()

	result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleClaudeStreamingResponse(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "claude-sonnet-4-5")
	_ = pr.Close()

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
}

// TestHandleClaudeStreamingResponse_EmptyStream feeds malformed stream JSON
// without a parseable event. The returned failover allows same-provider replay
// while output contains none of the asserted Claude lifecycle events.
func TestHandleClaudeStreamingResponse_EmptyStream(t *testing.T) {
	svc := newAntigravityStreamFixture(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}

	// Feed malformed JSON lines and close the producer so the converter
	// reaches EOF with no parsed content and keeps the outer failover
	// window open.
	go func() {
		defer func() { _ = pw.Close() }()
		// Every data line contains invalid JSON and produces no parsed
		// event.
		fmt.Fprintln(pw, "data: not-valid-json")
		fmt.Fprintln(pw, "")
		fmt.Fprintln(pw, "data: also-invalid")
		fmt.Fprintln(pw, "")
	}()

	_, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleClaudeStreamingResponse(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "claude-sonnet-4-5")
	_ = pr.Close()

	// The caller receives a retryable failover error.
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameProvider)

	// An unparseable stream emits no Claude lifecycle events.
	body := rec.Body.String()
	require.NotContains(t, body, "event: message_start")
	require.NotContains(t, body, "event: message_stop")
	require.NotContains(t, body, "event: message_delta")
}

// TestHandleClaudeStreamingResponse_ContextCanceled starts Claude conversion
// with a cancelled request and cancellation reader. The result marks
// disconnection with no returned error and client output contains no injected
// error event.
func TestHandleClaudeStreamingResponse_ContextCanceled(t *testing.T) {
	svc := newAntigravityStreamFixture(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)

	resp := &http.Response{StatusCode: http.StatusOK, Body: cancelReadCloser{}, Header: http.Header{}}

	result, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, svc.Options, true)}).HandleClaudeStreamingResponse(upstream.NewOutputContext(gatewayhttp.NewGoogleBoundary(c, svc.Options, true).Sink()), resp, time.Now(), "claude-sonnet-4-5")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.NotContains(t, rec.Body.String(), "event: error")
}

// TestAntigravityClientWriter compares successful output, immediate failure,
// and another write after disconnection. The writer reports delivery status,
// records disconnection, and keeps subsequent writes unsuccessful for the
// exercised cases.
func TestAntigravityClientWriter(t *testing.T) {
	t.Run("normal write succeeds", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		flusher, _ := c.Writer.(http.Flusher)
		cw := antigravity.NewClientWriter(c.Writer, flusher, "test")

		ok := cw.Write([]byte("hello"))
		require.True(t, ok)
		require.False(t, cw.Disconnected())
		require.Contains(t, rec.Body.String(), "hello")
	})

	t.Run("write failure marks disconnected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		fw := &antigravityFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
		flusher, _ := c.Writer.(http.Flusher)
		cw := antigravity.NewClientWriter(fw, flusher, "test")

		ok := cw.Write([]byte("hello"))
		require.False(t, ok)
		require.True(t, cw.Disconnected())
	})

	t.Run("subsequent writes are no-op", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		fw := &antigravityFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
		flusher, _ := c.Writer.(http.Flusher)
		cw := antigravity.NewClientWriter(fw, flusher, "test")

		cw.Write([]byte("first"))
		ok := cw.Fprintf("second %d", 2)
		require.False(t, ok)
		require.True(t, cw.Disconnected())
	})
}

// TestUnwrapV1InternalResponse compares wrapped, unwrapped, scalar/null,
// malformed, nested, and large input. The adapter returns the exact expected
// single-envelope bytes for each exercised fixture after trimming exterior
// whitespace.
func TestUnwrapV1InternalResponse(t *testing.T) {
	svc := newAntigravityFixture(antigravityDependencies{})

	// Use a long pad string under the same response wrapper so the large case
	// exercises payload size while retaining the unwrap shape.
	largePadding := strings.Repeat("x", 50*1024)
	largeInput := []byte(fmt.Sprintf(`{"response":{"id":"big","pad":"%s"}}`, largePadding))
	largeExpected := fmt.Sprintf(`{"id":"big","pad":"%s"}`, largePadding)

	tests := []struct {
		name     string // table scenario name
		input    []byte // wrapped response fixture bytes
		expected string // expected single-envelope output
		wantErr  bool   // expected unwrap failure flag
	}{
		{
			name: "正常 response 包装",

			input: []byte(`{"response":{"id":"123","content":"hello"}}`),

			expected: `{"id":"123","content":"hello"}`,
		},
		{
			name:     "无 response 透传",
			input:    []byte(`{"id":"456"}`),
			expected: `{"id":"456"}`,
		},
		{
			name:     "空 JSON",
			input:    []byte(`{}`),
			expected: `{}`,
		},
		{
			name:     "response 为 null",
			input:    []byte(`{"response":null}`),
			expected: `null`,
		},
		{
			name:     "response 为基础类型 string",
			input:    []byte(`{"response":"hello"}`),
			expected: `"hello"`,
		},
		{
			name:     "非法 JSON",
			input:    []byte(`not json`),
			expected: `not json`,
		},
		{
			name: "嵌套 response 只解一层",

			input: []byte(`{"response":{"response":{"inner":true}}}`),

			expected: `{"response":{"inner":true}}`,
		},
		{
			name:     "大型 JSON >50KB",
			input:    largeInput,
			expected: largeExpected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).UnwrapV1InternalResponse(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expected, strings.TrimSpace(string(got)))
		})
	}
}

// BenchmarkUnwrapV1Internal_Old_Small repeatedly decodes the small wrapper and
// remarshals its response value through the comparison helper. Setup is
// excluded from timing, and returned values are discarded in the loop.
func BenchmarkUnwrapV1Internal_Old_Small(b *testing.B) {
	body := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hello world"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}}`)
	b.ResetTimer()
	for range b.N {
		_, _ = unwrapV1InternalResponseOld(body)
	}
}

// BenchmarkUnwrapV1Internal_New_Small repeatedly unwraps the small response
// fixture through the platform adapter. Setup is excluded from timing, and
// returned values are discarded during the benchmark loop.
func BenchmarkUnwrapV1Internal_New_Small(b *testing.B) {
	body := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hello world"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}}`)
	svc := newAntigravityFixture(antigravityDependencies{})
	b.ResetTimer()
	for range b.N {
		_, _ = googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).UnwrapV1InternalResponse(body)
	}
}

// BenchmarkUnwrapV1Internal_Old_Large repeatedly decodes the approximate large
// wrapper and remarshals its response value through the comparison helper.
// Fixture setup is excluded from timing, and loop results are discarded.
func BenchmarkUnwrapV1Internal_Old_Large(b *testing.B) {
	// Use an approximate 10 KiB target for comparison with the small unwrap
	// fixtures measured by the companion benchmark.
	body := generateLargeUnwrapJSON(10 * 1024)
	b.ResetTimer()
	for range b.N {
		_, _ = unwrapV1InternalResponseOld(body)
	}
}

// BenchmarkUnwrapV1Internal_New_Large repeatedly unwraps the approximate large
// fixture through the platform adapter. Fixture construction is excluded from
// timing, and returned values are discarded during the benchmark loop.
func BenchmarkUnwrapV1Internal_New_Large(b *testing.B) {
	// Use an approximate 10 KiB target for comparison with the small unwrap
	// fixtures measured by the companion benchmark.
	body := generateLargeUnwrapJSON(10 * 1024)
	svc := newAntigravityFixture(antigravityDependencies{})
	b.ResetTimer()
	for range b.N {
		_, _ = googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).UnwrapV1InternalResponse(body)
	}
}

// TestExtractImageSize_ValidSizes supplies supported one-, two-, and four-K
// image-size hints to extraction and billing normalization. The selected tier
// matches each exercised request value after both operations.
func TestExtractImageSize_ValidSizes(t *testing.T) {
	svc := newAntigravityFixture(antigravityDependencies{})

	// 1K
	body := []byte(`{"generationConfig":{"imageConfig":{"imageSize":"1K"}}}`)
	require.Equal(t, "1K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))

	// 2K
	body = []byte(`{"generationConfig":{"imageConfig":{"imageSize":"2K"}}}`)
	require.Equal(t, "2K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))

	// 4K
	body = []byte(`{"generationConfig":{"imageConfig":{"imageSize":"4K"}}}`)
	require.Equal(t, "4K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))
}

// TestExtractImageSize_CaseInsensitive supplies lowercase image-size hints to
// extraction and billing normalization. The returned tiers use the expected
// uppercase labels for both exercised request size variants.
func TestExtractImageSize_CaseInsensitive(t *testing.T) {
	svc := newAntigravityFixture(antigravityDependencies{})

	body := []byte(`{"generationConfig":{"imageConfig":{"imageSize":"1k"}}}`)
	require.Equal(t, "1K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))

	body = []byte(`{"generationConfig":{"imageConfig":{"imageSize":"4k"}}}`)
	require.Equal(t, "4K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))
}

// TestExtractImageSize_Default omits generation configuration, image
// configuration, and imageSize in separate inputs. Extraction plus billing
// normalization chooses the two-K default for every absent-hint fixture.
func TestExtractImageSize_Default(t *testing.T) {
	svc := newAntigravityFixture(antigravityDependencies{})

	// No generationConfig.
	body := []byte(`{"contents":[]}`)
	require.Equal(t, "2K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))

	// generationConfig has no imageConfig.
	body = []byte(`{"generationConfig":{"temperature":0.7}}`)
	require.Equal(t, "2K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))

	// imageConfig has no imageSize.
	body = []byte(`{"generationConfig":{"imageConfig":{}}}`)
	require.Equal(t, "2K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))
}

// TestExtractImageSize_InvalidJSON supplies malformed text and truncated JSON
// to extraction and billing normalization. Both inputs select the two-K default
// used when no valid image hint is available.
func TestExtractImageSize_InvalidJSON(t *testing.T) {
	svc := newAntigravityFixture(antigravityDependencies{})

	body := []byte(`not valid json`)
	require.Equal(t, "2K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))

	body = []byte(`{"broken":`)
	require.Equal(t, "2K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))
}

// TestExtractImageSize_EmptySize supplies empty and whitespace-only imageSize
// strings to extraction and billing normalization. Both request variants select
// the two-K default after their hint values are interpreted.
func TestExtractImageSize_EmptySize(t *testing.T) {
	svc := newAntigravityFixture(antigravityDependencies{})

	body := []byte(`{"generationConfig":{"imageConfig":{"imageSize":""}}}`)
	require.Equal(t, "2K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))

	// Whitespace-only size.
	body = []byte(`{"generationConfig":{"imageConfig":{"imageSize":"   "}}}`)
	require.Equal(t, "2K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))
}

// TestExtractImageSize_InvalidSize supplies unsupported three-, eight-K, and
// arbitrary size hints. Extraction plus billing normalization chooses the two-K
// default for each exercised unsupported request value.
func TestExtractImageSize_InvalidSize(t *testing.T) {
	svc := newAntigravityFixture(antigravityDependencies{})

	body := []byte(`{"generationConfig":{"imageConfig":{"imageSize":"3K"}}}`)
	require.Equal(t, "2K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))

	body = []byte(`{"generationConfig":{"imageConfig":{"imageSize":"8K"}}}`)
	require.Equal(t, "2K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))

	body = []byte(`{"generationConfig":{"imageConfig":{"imageSize":"invalid"}}}`)
	require.Equal(t, "2K", pricing.NormalizeImageBillingTierOrDefault(googleforward.AntigravityResponseForTest(svc, &googleforward.AttemptForTest{}).ExtractImageInputSize(body)))
}

// Read copies remaining fixture bytes across calls before returning the
// configured terminal error. off tracks the consumed prefix; after all bytes,
// nil error produces zero-byte success for this fixture.
func (r *antigravityCompatErrorReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	return 0, r.err
}

// Close succeeds because the error-reader fixture has no external resource to
// release.
func (r *antigravityCompatErrorReader) Close() error { return nil }

// Write forwards calls until writes reaches failAfter, then returns zero with a
// disconnect error. The counter advances before delegated output, allowing
// response tests to choose the failing client-write point.
func (w *antigravityFailingWriter) Write(p []byte) (int, error) {
	if w.writes >= w.failAfter {
		return 0, errors.New("write failed: client disconnected")
	}
	w.writes++
	return w.ResponseWriter.Write(p)
}

// unwrapV1InternalResponseOld decodes and remarshals a present response value
// for benchmark comparison. Invalid JSON returns its decoding error, while an
// absent response property returns the supplied body bytes.
func unwrapV1InternalResponseOld(body []byte) ([]byte, error) {
	var outer map[string]any
	if err := json.Unmarshal(body, &outer); err != nil {
		return nil, err
	}
	if resp, ok := outer["response"]; ok {
		return json.Marshal(resp)
	}
	return body, nil
}

// generateLargeUnwrapJSON constructs a wrapped fixture using approximate per-
// part encoding overhead until minSize is reached. The result is benchmark
// input, and callers must not treat minSize as its exact encoded length.
func generateLargeUnwrapJSON(minSize int) []byte {
	parts := make([]map[string]string, 0)
	current := 0
	for current < minSize {
		text := fmt.Sprintf("这是第 %d 段内容，用于填充 JSON 到目标大小。", len(parts)+1)
		parts = append(parts, map[string]string{"text": text})
		// The added overhead approximates per-part JSON syntax, so
		// minSize drives payload construction without promising an
		// exact encoded byte length.
		current += len(text) + 20
	}
	inner := map[string]any{
		"candidates": []map[string]any{
			{"content": map[string]any{"parts": parts}},
		},

		"usageMetadata": map[string]any{
			"promptTokenCount":     100,
			"candidatesTokenCount": 50,
		},
	}
	outer := map[string]any{"response": inner}
	// The fixture contains maps, slices, strings, and numbers that the JSON
	// encoder accepts; marshal supplies benchmark bytes after the
	// approximate size construction.
	b, _ := json.Marshal(outer)
	return b
}

// Read returns zero bytes and context.Canceled for every fixture invocation.
func (cancelReadCloser) Read([]byte) (int, error) { return 0, context.Canceled }

// Close succeeds because the cancellation fixture owns no external resource.
func (cancelReadCloser) Close() error { return nil }
