package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	httptestkit "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/testkit"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	selectionadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	responseupstream "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// The reader fails after emitting content and usage so partial results can be checked.
type grokObservationErrorReader struct{ err error }

type grokPingFilterTestReadCloser struct {
	reader     io.ReadCloser
	closeCount atomic.Int32
}

type firstOutputCloseTrackingBody struct {
	io.ReadCloser
	closed chan struct{}
	once   sync.Once
}

type stagedOpenAISSEReadCloser struct {
	segments   [][]byte
	gates      []<-chan struct{}
	waiting    []chan struct{}
	eofReached chan struct{}
	current    []byte
	index      int
}

type openAIResponseFlushReadError struct {
	payload []byte
	err     error
	sent    bool
}

type cancelReadCloser struct{}

type errReadCloser struct {
	err error
}

// streamSelectionDiagnosticSource supplies scheduler data for selection after stream execution.
// The diagnostic copy includes group and active state; the stream fixture holds transport fields.
type streamSelectionDiagnosticSource struct {
	value gatewayprovider.ExecutionProvider
	group routing.Group
}

func (r grokObservationErrorReader) Read([]byte) (int, error) { return 0, r.err }

// TestGrokNativeObservationRetainsPartialUsageAfterReadError checks usage retention after visible output and a
// read error.
func TestGrokNativeObservationRetainsPartialUsageAfterReadError(t *testing.T) {
	failure := errors.New("fixture truncated stream")
	payload := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"visible\",\"usage\":{\"input_tokens\":9,\"output_tokens\":2}}\n\n"
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 470, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey}}
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader(payload), grokObservationErrorReader{failure}))}
	service := newResponseOutputForTest(OpenAIResponseOptions{})
	result, err := service.ReadStreamObservation(context.Background(), response, c, provider, time.Now(), "grok-fixture", "grok-fixture", "")
	require.Error(t, err)
	require.Contains(t, recorder.Body.String(), "visible")
	require.NotNil(t, result)
	require.True(t, result.Served)
	require.True(t, result.HasUsage)
	require.True(t, result.HttpCommitted)
	require.NotNil(t, result.FirstSemanticOutput)
	require.False(t, result.ObservedOnly)
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func filterGrokPingTestInput(t *testing.T, input string) string {
	t.Helper()
	body := grok.NewGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader(input)),
		OpenAIResponseDefaultMaxLineSize,
	)
	output, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	return string(output)
}

func TestGrokResponsesBillingPingFilter(t *testing.T) {
	input := strings.Join([]string{
		": upstream keepalive",
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		"",
		"event: future.vendor_event",
		`data: {"type":"future.vendor_event","value":1}`,
		"",
		"event: ping",
		`data: {"type":"ping","x-opencode-type":"inference-cost","cost":2.75,"input-tokens":42}`,
		"",
		"event: ping",
		`data: {"type":"ping","cost":"0"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":3,"output_tokens":5}}}`,
		"",
	}, "\n")

	result := filterGrokPingTestInput(t, input)
	require.NotContains(t, result, "event: ping")
	require.NotContains(t, result, `"x-opencode-type":"inference-cost"`)
	require.NotContains(t, result, `{"type":"ping","cost":"0"}`)
	require.Equal(t, 2, strings.Count(result, ": ping\n\n"))
	require.Contains(t, result, ": upstream keepalive\n\n")
	require.Contains(t, result, "event: response.output_text.delta")
	require.Contains(t, result, `{"type":"response.output_text.delta","delta":"hello"}`)
	require.Contains(t, result, "event: future.vendor_event")
	require.Contains(t, result, `{"type":"future.vendor_event","value":1}`)
	require.Contains(t, result, "event: response.completed")
	require.Contains(t, result, `"usage":{"input_tokens":3,"output_tokens":5}`)
}

func TestGrokResponsesBillingPingFilterComposesWithClientToolStream(t *testing.T) {
	input := "event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n"
	filtered := grok.NewGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader(input)),
		OpenAIResponseDefaultMaxLineSize,
	)
	body := upstreamcore.NewResponsesClientToolStreamBody(filtered, bridge.ResponsesClientToolMapping{
		CustomTools: map[string]bool{"apply_patch": true},
	}, OpenAIResponseDefaultMaxLineSize)

	output, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Contains(t, string(output), ": ping\n\n")
	require.NotContains(t, string(output), "event: ping")
	require.Contains(t, string(output), "event: response.completed")
	require.Contains(t, string(output), `"id":"resp_1"`)
}

// TestGrokResponsesBillingPingFilterConvertsPingVariants checks that every compatible ping variant becomes an
// SSE comment.
// Strict Responses clients reject event: ping regardless of its payload (issue #5105).
func TestGrokResponsesBillingPingFilterConvertsPingVariants(t *testing.T) {
	frames := []string{
		"event: ping\ndata: {\"type\":\"ping\",\"x-opencode-type\":\"inference-cost\",\"cost\":\"0.06029240\"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0.06029240\"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"kind\":\"keepalive\"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"cost\":2}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"cost\":0.0001}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"cost\":\" 0 \"}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"x-opencode-type\":\"keepalive\",\"cost\":0}\n\n",
		"event: ping\ndata: {\"type\":\"ping\",\"x-opencode-type\":null,\"cost\":0}\n\n",
		"event: ping\ndata: {\"cost\":\"0\"}\n\n",
		"event: ping\ndata: {not-json}\n\n",
		"event: ping\n\n",
		"event: ping\n: vendor note\ndata: {\"type\":\"ping\"}\n\n",
	}
	result := filterGrokPingTestInput(t, strings.Join(frames, ""))
	require.Equal(t, strings.Repeat(": ping\n\n", len(frames)), result)
}

func TestGrokResponsesBillingPingFilterPreservesNonPingFrames(t *testing.T) {
	input := strings.Join([]string{
		"event: ping",
		`data: {"type":" ping ","cost":0}`,
		"",
		"event: ping",
		`data: {"type":"response.completed"}`,
		"",
		"event: custom",
		`data: {"type":"ping","x-opencode-type":"inference-cost"}`,
		"",
		`data: {"type":"ping","cost":"0"}`,
		"",
		": keepalive comment",
		"",
		"retry: 1000",
		"",
	}, "\n")

	require.Equal(t, input, filterGrokPingTestInput(t, input))
}

// TestGrokResponsesBillingPingFilterPassesThroughPingFrameWithUnknownField checks byte-for-byte replay of
// frames with unknown SSE fields.
func TestGrokResponsesBillingPingFilterPassesThroughPingFrameWithUnknownField(t *testing.T) {
	input := "event: ping\nid: 7\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n"
	require.Equal(t, input, filterGrokPingTestInput(t, input))
}

// TestGrokResponsesBillingPingFilterPassesThroughOversizedPingFrame checks unbuffered passthrough after the
// line or byte limit.
func TestGrokResponsesBillingPingFilterPassesThroughOversizedPingFrame(t *testing.T) {
	lines := []string{"event: ping"}
	for range grok.ResponsesPingFrameMaxLines {
		lines = append(lines, ": filler comment")
	}
	lines = append(lines, `data: {"type":"ping","cost":"0"}`, "")
	byLines := strings.Join(lines, "\n")
	require.Equal(t, byLines, filterGrokPingTestInput(t, byLines))

	byBytes := "event: ping\ndata: {\"type\":\"ping\",\"pad\":\"" +
		strings.Repeat("x", grok.ResponsesPingFrameMaxBytes) + "\"}\n\n"
	require.Equal(t, byBytes, filterGrokPingTestInput(t, byBytes))
}

func TestGrokResponsesBillingPingFilterConvertsMalformedPingFrames(t *testing.T) {
	input := "event: ping\r\ndata: {not-json}\r\n\r\n" +
		"event: ping\r\ndata: {\"type\":\"ping\",\"cost\":\"0\"} trailing\r\n\r\n" +
		"event: future.response.event\r\ndata: {\"type\":\"future.response.event\"}"
	want := ": ping\n\n" + ": ping\n\n" +
		"event: future.response.event\r\ndata: {\"type\":\"future.response.event\"}"
	require.Equal(t, want, filterGrokPingTestInput(t, input))
}

func TestGrokResponsesBillingPingFilterHandlesBareCRFrames(t *testing.T) {
	input := "event: ping\rdata: {\"type\":\"ping\",\"cost\":\"0\"}\r\r" +
		"event: future.event\rdata: {\"type\":\"future.event\"}\r\r"
	want := ": ping\n\n" + "event: future.event\rdata: {\"type\":\"future.event\"}\r\r"
	require.Equal(t, want, filterGrokPingTestInput(t, input))
}

func TestGrokResponsesBillingPingFilterConvertsPartialPingFrameAtEOF(t *testing.T) {
	input := "event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0\"}"
	require.Equal(t, ": ping\n\n", filterGrokPingTestInput(t, input))
}

// TestGrokResponsesBillingPingFilterDoesNotFilterNonGrokProviders checks that non-Grok output keeps ping and
// terminal frames.
func TestGrokResponsesBillingPingFilterDoesNotFilterNonGrokProviders(t *testing.T) {
	input := "event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"non-grok\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"output\":[]}}\n\n"
	body := io.NopCloser(strings.NewReader(input))
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	output := &OpenAIResponseOutput{Options: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	_, err := output.ReadStreamObservation(context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{Platform: capability.PlatformOpenAI}}, time.Now(), "model", "model", "")
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, input, recorder.Body.String())
}

func TestGrokResponsesBillingPingFilterPreservesUsageAndTerminalEvent(t *testing.T) {
	input := strings.Join([]string{
		"event: ping",
		`data: {"type":"ping","x-opencode-type":"inference-cost","cost":"0"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":3,"output_tokens":5}}}`,
		"",
	}, "\n")
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGrok}}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body: grok.NewGrokResponsesBillingPingFilterBody(
			io.NopCloser(strings.NewReader(input)), OpenAIResponseDefaultMaxLineSize,
		),
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc := &OpenAIResponseOutput{Options: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}, Corrector: responseupstream.NewCodexToolCorrector()}

	result, err := svc.ReadStreamObservation(context.Background(), resp, c, provider, time.Now(), "grok-4.5", "grok-4.5", "")
	require.NoError(t, err)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Equal(t, "resp_1", result.ResponseID)
	require.Contains(t, recorder.Body.String(), "response.completed")
	require.NotContains(t, recorder.Body.String(), "inference-cost")
	require.NotContains(t, recorder.Body.String(), "event: ping")
}

func (r *grokPingFilterTestReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }

func (r *grokPingFilterTestReadCloser) Close() error {
	r.closeCount.Add(1)
	return r.reader.Close()
}

func TestGrokResponsesBillingPingFilterCloseCancelsSourceOnce(t *testing.T) {
	upstreamReader, upstreamWriter := io.Pipe()
	source := &grokPingFilterTestReadCloser{reader: upstreamReader}
	body := grok.NewGrokResponsesBillingPingFilterBody(source, OpenAIResponseDefaultMaxLineSize)

	require.NoError(t, body.Close())
	require.Eventually(t, func() bool { return source.closeCount.Load() == 1 }, time.Second, time.Millisecond)
	_, err := upstreamWriter.Write([]byte("blocked"))
	require.Error(t, err)
	require.NoError(t, upstreamWriter.Close())
}

func TestGrokResponsesBillingPingFilterFlushesCompletedFrames(t *testing.T) {
	upstreamReader, upstreamWriter := io.Pipe()
	body := grok.NewGrokResponsesBillingPingFilterBody(upstreamReader, OpenAIResponseDefaultMaxLineSize)
	t.Cleanup(func() { require.NoError(t, body.Close()) })

	go func() {
		_, _ = io.WriteString(upstreamWriter, "event: future.event\ndata: {\"type\":\"future.event\"}\n\n")
	}()

	result := make(chan error, 1)
	go func() {
		buffer := make([]byte, 64)
		n, err := body.Read(buffer)
		if err == nil && !strings.Contains(string(buffer[:n]), "future.event") {
			err = errors.New("completed frame was not forwarded")
		}
		result <- err
	}()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("completed frame was buffered until upstream EOF")
	}
}

func TestGrokResponsesBillingPingFilterReportsOversizedLine(t *testing.T) {
	body := grok.NewGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader("data: 123456789\n\n")),
		8,
	)
	_, err := io.ReadAll(body)
	require.ErrorContains(t, err, "filter Grok Responses billing ping")
	require.NoError(t, body.Close())
}

func TestOpenAIStreamPairedFailureAppliesProviderSideEffectsOnce(t *testing.T) {
	const upstream = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" +
		"data: {\"type\":\"error\",\"error\":{\"status_code\":403,\"code\":\"workspace_suspended\",\"message\":\"workspace is suspended\"}}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"error\":{\"status_code\":403,\"code\":\"workspace_suspended\",\"message\":\"workspace is suspended\"}}}\n\n"

	t.Run("native", func(t *testing.T) {
		repo := &openAIStream403ProviderRepo{}
		svc := newWSFixture(wsFixtureInputs{options: &wsFixtureOptions{}, corrector: responseupstream.NewCodexToolCorrector(), health: newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)})
		provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 921, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
		recorder := newOpenAIResponseFlushRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(upstream)),
		}

		result, err := svc.Output.ReadStreamObservation(context.Background(), resp, c, provider, time.Now(), "gpt-5", "gpt-5", "")

		require.Error(t, err)
		require.NotNil(t, result)
		require.Equal(t, 1, repo.setErrorCalls)
	})

	t.Run("passthrough", func(t *testing.T) {
		repo := &openAIStream403ProviderRepo{}
		svc := newWSFixture(wsFixtureInputs{options: &wsFixtureOptions{}, health: newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)})
		provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 922, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		writer := &passthroughFlushTestWriter{
			ResponseWriter:  c.Writer,
			recorder:        recorder,
			failAfterWrites: -1,
		}
		c.Writer = writer
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(upstream)),
		}

		result, err := responseupstream.ReadPassthroughStreaming(context.Background(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(context.Background(), c, provider), time.Now(), "gpt-5", "gpt-5")

		require.Error(t, err)
		require.NotNil(t, result)
		require.Equal(t, 1, repo.setErrorCalls)
	})
}

func TestOpenAIStreamMetadataPreambleAndMessageOnlyOverloadFailOver(t *testing.T) {
	largeMetadata := strings.Repeat("x", 16*1024)
	stream := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_1","metadata":{"padding":"` + largeMetadata + `"}}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`,
		"",
		"event: response.reasoning_summary_part.added",
		`data: {"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`,
		"",
		"event: error",
		`data: {"type":"error","error":{"type":"service_unavailable_error","message":"Our servers are currently overloaded. Please try again later."}}`,
		"",
	}, "\n")

	tests := []struct {
		name string
		run  func(*OpenAIResponsesExecutor, *gin.Context, *http.Response, *gatewayprovider.ExecutionProvider) error
	}{
		{
			name: "native",
			run: func(svc *OpenAIResponsesExecutor, c *gin.Context, resp *http.Response, provider *gatewayprovider.ExecutionProvider) error {
				_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, provider, time.Now(), "model", "model", "")
				return err
			},
		},
		{
			name: "passthrough",
			run: func(svc *OpenAIResponsesExecutor, c *gin.Context, resp *http.Response, provider *gatewayprovider.ExecutionProvider) error {
				_, err := responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, provider), time.Now(), "model", "model")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(stream)),
				Header:     http.Header{"X-Request-Id": []string{"rid-message-only-overload"}},
			}
			provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Name: "acc"}}

			err := tt.run(svc, c, resp, provider)
			require.Error(t, err)
			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.RetryableOnSameProvider)
			require.True(t, failoverErr.RequestScopedTransient)
			require.Equal(t, http.StatusServiceUnavailable, failoverErr.ClientStatusCode)
			require.Contains(t, failoverErr.ClientMessage, "servers are currently overloaded")
			require.False(t, c.Writer.Written())
			require.Empty(t, rec.Body.String())
		})
	}
}

// TestOpenAIStreamCapacityShedErrorFramePrecedingFailedStillFailsOver covers created, in_progress, error, and
// response.failed capacity shedding.
// Expect same-provider retry and a request-scoped transient marker before any client output.
func TestOpenAIStreamCapacityShedErrorFramePrecedingFailedStillFailsOver(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"},"sequence_number":0}`,
			"",
			"event: response.in_progress",
			`data: {"type":"response.in_progress","response":{"id":"resp_1"},"sequence_number":1}`,
			"",
			"event: error",
			`data: {"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."},"sequence_number":2}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}},"sequence_number":3}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-shed-error-then-failed"}},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Name: "acc"}}, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameProvider)
	require.True(t, failoverErr.RequestScopedTransient)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

// TestOpenAIStreamCapacityShedAfterOutputRewritesCodeForClient checks a single response.failed with
// server_error after output starts.
// Codex backs off for server_error but ends the session for server_is_overloaded or slow_down.
func TestOpenAIStreamCapacityShedAfterOutputRewritesCodeForClient(t *testing.T) {
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","delta":"partial"}`,
			"",
			"event: error",
			`data: {"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."},"sequence_number":2}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}},"sequence_number":3}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-shed-after-output"}},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Name: "acc"}}, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))

	body := rec.Body.String()
	require.Contains(t, body, "partial")
	require.NotContains(t, body, "event: error")
	require.Equal(t, 1, strings.Count(body, "event: response.failed"))
	require.Equal(t, 1, strings.Count(body, `"code":"server_error"`))
	require.Contains(t, body, `"code":"server_error"`)
	require.NotContains(t, body, "server_is_overloaded")
	require.Contains(t, body, "Our servers are currently overloaded")
	require.True(t, logSink.ContainsMessage("gateway.failover_suppressed_after_semantic_output"))
	require.True(t, logSink.ContainsFieldValue("path", "native_sse"))
	require.True(t, logSink.ContainsFieldValue("upstream_request_id", "rid-shed-after-output"))
}

// TestHandleNonStreamingResponse_CompactClientStreamBridgesToSSE checks JSON-to-SSE bridging for body-signaled
// Compact requests (#3875).
// Codex remote compact v2 expects SSE; JSON causes stream closed before response.completed and reconnection.
func TestHandleNonStreamingResponse_CompactClientStreamBridgesToSSE(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, true)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_compact_json",
			"object":"response",
			"model":"gpt-5.1-codex",
			"status":"completed",
			"output":[{"id":"cmp_1","type":"compaction","status":"completed","encrypted_content":"compact-payload"}],
			"usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13}
		}`)),
	}

	result, err := svc.NonStream(context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Type: capability.ProviderTypeOAuth}}, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	events := httptestkit.ParseCompactSSE(t, rec.Body.String())
	require.Len(t, events, 2)
	require.Equal(t, "response.output_item.done", events[0][0])
	require.Equal(t, "compaction", gjson.Get(events[0][1], "item.type").String())
	require.Equal(t, "response.completed", events[1][0])
	require.Equal(t, "resp_compact_json", gjson.Get(events[1][1], "response.id").String())

	// Billing and response metadata are independent of the output format.
	require.NotNil(t, result.Usage)
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, "resp_compact_json", result.ResponseID)
}

// TestHandleNonStreamingResponse_PathBasedCompactStaysJSON checks JSON output without client-stream for Codex
// v1 unary and chained gateways.
func TestHandleNonStreamingResponse_PathBasedCompactStaysJSON(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, false)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_compact_json",
			"output":[{"id":"cmp_1","type":"compaction","encrypted_content":"compact-payload"}],
			"usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13}
		}`)),
	}

	result, err := svc.NonStream(context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Type: capability.ProviderTypeOAuth}}, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotContains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	body := rec.Body.String()
	require.Equal(t, "resp_compact_json", gjson.Get(body, "id").String())
	require.Equal(t, "compaction", gjson.Get(body, "output.0.type").String())
}

// TestHandleSSEToJSON_CompactClientStreamBridgesToSSE checks SSE reconstruction after extracting Compact
// upstream SSE into JSON.
func TestHandleSSEToJSON_CompactClientStreamBridgesToSSE(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, true)
	upstreamSSE := `data: {"type":"response.completed","response":{"id":"resp_compact_sse","object":"response","model":"gpt-5.1-codex","status":"completed","output":[{"id":"cmp_sse_1","type":"compaction","status":"completed","encrypted_content":"compact-sse-payload"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}` + "\n" + ""
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}

	result, err := svc.NonStream(context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Type: capability.ProviderTypeOAuth}}, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	events := httptestkit.ParseCompactSSE(t, rec.Body.String())
	require.Len(t, events, 2)
	require.Equal(t, "response.output_item.done", events[0][0])
	require.Equal(t, "compact-sse-payload", gjson.Get(events[0][1], "item.encrypted_content").String())
	require.Equal(t, "response.completed", events[1][0])
	require.Equal(t, "resp_compact_sse", gjson.Get(events[1][1], "response.id").String())
}

// TestHandleSSEToJSON_CompactRawOutputItemDoneRepairsEmptyTerminalOutput covers compaction in raw
// output_item.done with empty terminal output (#3777).
// Supplement output with the raw compaction item; otherwise Codex retries the missing item and each retry is
// billed (#3887).
func TestHandleSSEToJSON_CompactRawOutputItemDoneRepairsEmptyTerminalOutput(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, true)
	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"cmp_1","type":"compaction_summary","status":"completed","summary":[{"type":"summary_text","text":"compact summary"}],"encrypted_content":"compact-payload","opaque":{"kept":true}}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_compact","object":"response","model":"gpt-5.1-codex","status":"completed","output":[],"usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13}}}`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}

	result, err := svc.NonStream(context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Type: capability.ProviderTypeOAuth}}, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	events := httptestkit.ParseCompactSSE(t, rec.Body.String())
	require.Len(t, events, 2)
	require.Equal(t, "response.output_item.done", events[0][0])
	item := gjson.Get(events[0][1], "item")
	require.Equal(t, "compaction_summary", item.Get("type").String())
	require.Equal(t, "cmp_1", item.Get("id").String())
	require.Equal(t, "compact-payload", item.Get("encrypted_content").String())
	require.Equal(t, "compact summary", item.Get("summary.0.text").String())
	require.True(t, item.Get("opaque.kept").Bool(), "raw item 字段必须逐字节保留")
	require.Equal(t, "response.completed", events[1][0])
	require.Equal(t, "resp_compact", gjson.Get(events[1][1], "response.id").String())
	require.Len(t, gjson.Get(events[1][1], "response.output").Array(), 1)
	require.Equal(t, int64(13), gjson.Get(events[1][1], "response.usage.total_tokens").Int())

	require.NotNil(t, result.Usage)
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
}

// TestHandleSSEToJSON_PathBasedCompactRawOutputItemDoneRepairsJSON checks that path-based Compact JSON
// includes the compaction item.
func TestHandleSSEToJSON_PathBasedCompactRawOutputItemDoneRepairsJSON(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, false)
	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"cmp_v1","type":"compaction_summary","encrypted_content":"compact-v1-raw"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_compact_v1","object":"response","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}

	result, err := svc.NonStream(context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Type: capability.ProviderTypeOAuth}}, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	require.NotNil(t, result)

	// The response body contains the repaired JSON document.
	body := rec.Body.String()
	require.NotContains(t, body, "event:")
	require.NotContains(t, body, "data:")
	require.Equal(t, "resp_compact_v1", gjson.Get(body, "id").String())
	require.Equal(t, "compaction_summary", gjson.Get(body, "output.0.type").String())
	require.Equal(t, "compact-v1-raw", gjson.Get(body, "output.0.encrypted_content").String())
}

// TestReconstructResponseOutputFromSSE_PrefersRawDoneItems checks that complete done items take precedence
// over deltas and are counted once.
func TestReconstructResponseOutputFromSSE_PrefersRawDoneItems(t *testing.T) {
	bodyText := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"hel"}`,
		`data: {"type":"response.output_text.delta","delta":"lo"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[]}}`,
	}, "\n")

	outputJSON, ok := bridge.ReconstructResponseOutputFromSSE(bodyText)
	require.True(t, ok)
	items := gjson.ParseBytes(outputJSON).Array()
	require.Len(t, items, 1, "raw done item 与 delta 重建不得重复")
	require.Equal(t, "msg_1", items[0].Get("id").String())
	require.Equal(t, "hello", items[0].Get("content.0.text").String())
}

// TestReconstructResponseOutputFromSSE_CompactionAddedFallback checks compaction collection from added events
// when done events are absent.
func TestReconstructResponseOutputFromSSE_CompactionAddedFallback(t *testing.T) {
	bodyText := `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"cmp_add","type":"compaction","encrypted_content":"added-only"}}` + "\n" + `data: {"type":"response.completed","response":{"id":"resp_1","output":[]}}`

	outputJSON, ok := bridge.ReconstructResponseOutputFromSSE(bodyText)
	require.True(t, ok)
	items := gjson.ParseBytes(outputJSON).Array()
	require.Len(t, items, 1)
	require.Equal(t, "compaction", items[0].Get("type").String())
	require.Equal(t, "added-only", items[0].Get("encrypted_content").String())
}

// TestReconstructResponseOutputFromSSE_MixedDoneAndCompactionAdded checks missing compaction recovery and
// deduplication against done items.
func TestReconstructResponseOutputFromSSE_MixedDoneAndCompactionAdded(t *testing.T) {
	bodyText := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"cmp_mixed","type":"compaction","encrypted_content":"mixed"}}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"hi"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[]}}`,
	}, "\n")

	outputJSON, ok := bridge.ReconstructResponseOutputFromSSE(bodyText)
	require.True(t, ok)
	items := gjson.ParseBytes(outputJSON).Array()
	require.Len(t, items, 2)
	require.Equal(t, "msg_1", items[0].Get("id").String())
	require.Equal(t, "cmp_mixed", items[1].Get("id").String())

	// The done event already contains compaction; the added item uses different raw bytes and no ID
	// to check deduplication when Codex requires exactly one compaction item.
	bodyText = strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"compaction","status":"in_progress"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"compaction","status":"completed","encrypted_content":"final"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[]}}`,
	}, "\n")
	outputJSON, ok = bridge.ReconstructResponseOutputFromSSE(bodyText)
	require.True(t, ok)
	items = gjson.ParseBytes(outputJSON).Array()
	require.Len(t, items, 1)
	require.Equal(t, "final", items[0].Get("encrypted_content").String())
}

// TestHandleSSEToJSON_CompactSupplementsMissingCompactionIntoNonEmptyOutput checks adding raw compaction to
// terminal output that already contains a message.
func TestHandleSSEToJSON_CompactSupplementsMissingCompactionIntoNonEmptyOutput(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, true)
	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"cmp_sup","type":"compaction","encrypted_content":"supplement"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_sup","object":"response","status":"completed","output":[{"id":"msg_sup","type":"message","role":"assistant","content":[{"type":"output_text","text":"note"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}

	result, err := svc.NonStream(context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Type: capability.ProviderTypeOAuth}}, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	require.NotNil(t, result)

	events := httptestkit.ParseCompactSSE(t, rec.Body.String())
	require.Len(t, events, 3)
	itemTypes := []string{
		gjson.Get(events[0][1], "item.type").String(),
		gjson.Get(events[1][1], "item.type").String(),
	}
	require.Contains(t, itemTypes, "compaction")
	require.Contains(t, itemTypes, "message")
	require.Equal(t, "response.completed", events[2][0])
	require.Len(t, gjson.Get(events[2][1], "response.output").Array(), 2)
}

// TestReconstructResponseOutputFromSSE_NonCompactionAddedStillUsesDeltas checks that non-compaction added items
// use delta reconstruction because added messages are usually incomplete.
func TestReconstructResponseOutputFromSSE_NonCompactionAddedStillUsesDeltas(t *testing.T) {
	bodyText := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","content":[]}}`,
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[]}}`,
	}, "\n")

	outputJSON, ok := bridge.ReconstructResponseOutputFromSSE(bodyText)
	require.True(t, ok)
	items := gjson.ParseBytes(outputJSON).Array()
	require.Len(t, items, 1)
	require.Equal(t, "hi", items[0].Get("content.0.text").String())
}

func (b *firstOutputCloseTrackingBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return b.ReadCloser.Close()
}

func TestOpenAINativeFirstOutputTimeoutDisabledPreservesSynchronousStream(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{Response: OpenAIResponseOptions{OpenAIFirstOutputTimeoutSeconds: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}})
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_disabled"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_disabled","usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")))}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	result, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}, time.Now(), "model", "model", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "response.completed")
}

func TestOpenAINativeFirstOutputTimeoutIgnoresPreambleAndCleansReader(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{Response: OpenAIResponseOptions{OpenAIFirstOutputTimeoutSeconds: 1, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}})
	pr, pw := io.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_slow\"}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_slow\"}}\n\n"))
		time.Sleep(200 * time.Millisecond)
	}()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := &firstOutputCloseTrackingBody{ReadCloser: pr, closed: make(chan struct{})}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}, time.Now().Add(-2*time.Second), "model", "model", "")

	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusGatewayTimeout, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "first_output_timeout")
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.Empty(t, rec.Body.String())
	select {
	case <-body.closed:
	default:
		t.Fatal("first-output timeout did not close the upstream response body")
	}
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("stream reader/writer goroutine did not exit after first-output timeout")
	}
}

func TestOpenAINativeFirstOutputTimeoutDisarmsAfterSemanticOutput(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{OpenAIFirstOutputTimeoutSeconds: 1, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg, headers: compileHTTPFixtureHeaders(cfg)})
	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ok\"}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = pw.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{
		"X-Request-Id":                   []string{"request-winning"},
		"X-Ratelimit-Remaining-Requests": []string{"42"},
	}, Body: pr}

	result, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}, time.Now(), "model", "model", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.FirstTokenMs)
	require.Contains(t, rec.Body.String(), "response.output_text.delta")
	require.Contains(t, rec.Body.String(), "response.completed")
	require.Equal(t, "request-winning", rec.Result().Header.Get("X-Request-Id"))
	require.Equal(t, "42", rec.Result().Header.Get("X-Ratelimit-Remaining-Requests"))
}

func TestOpenAINativeFirstOutputTimeoutWaitsForCompleteSemanticEvent(t *testing.T) {
	const lineSize = 68106
	prefix := `data: {"type":"response.output_text.delta","delta":"`
	suffix := `"}`
	line := prefix + strings.Repeat("x", lineSize-len(prefix)-len(suffix)) + suffix
	require.Len(t, line, lineSize)
	assertOpenAINativeLargeOpenEventTimesOutWithoutLeak(t, line)
}

func TestOpenAINativeFirstOutputTimeoutDoesNotLeakLargePreambleEvent(t *testing.T) {
	const lineSize = 68106
	prefix := `data: {"type":"response.created","response":{"id":"resp_partial","padding":"`
	suffix := `"}}`
	line := prefix + strings.Repeat("x", lineSize-len(prefix)-len(suffix)) + suffix
	require.Len(t, line, lineSize)
	assertOpenAINativeLargeOpenEventTimesOutWithoutLeak(t, line)
}

func assertOpenAINativeLargeOpenEventTimesOutWithoutLeak(t *testing.T, line string) {
	t.Helper()
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{OpenAIFirstOutputTimeoutSeconds: 1, StreamKeepaliveInterval: 1, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg, headers: compileHTTPFixtureHeaders(cfg)})
	pr, pw := io.Pipe()
	body := &firstOutputCloseTrackingBody{ReadCloser: pr, closed: make(chan struct{})}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte(line + "\n"))
		select {
		case <-body.closed:
		case <-time.After(2 * time.Second):
		}
	}()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{
		"X-Request-Id":                   []string{"request-partial"},
		"X-Ratelimit-Remaining-Requests": []string{"1"},
	}, Body: body}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}, time.Now(), "model", "model", "")

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusGatewayTimeout, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "first_output_timeout")
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.NotContains(t, rec.Body.String(), "data:", "attempt JSON must remain private before the SSE boundary")
	require.NotContains(t, rec.Body.String(), `"type"`, "attempt JSON must remain private before the SSE boundary")
	for outputLine := range strings.SplitSeq(strings.TrimSpace(rec.Body.String()), "\n") {
		if outputLine != "" {
			require.True(t, strings.HasPrefix(outputLine, ":"), "only keepalive comments may precede failover: %q", outputLine)
		}
	}
	require.Empty(t, rec.Header().Values("X-Request-Id"))
	require.Empty(t, rec.Header().Values("X-Ratelimit-Remaining-Requests"))
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("partial-event writer did not exit after timeout closed the body")
	}
}

func TestOpenAINativeFirstOutputEOFDispatchesTerminalEventWithoutBlankLine(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{OpenAIFirstOutputTimeoutSeconds: 1, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg, headers: compileHTTPFixtureHeaders(cfg)})
	payload := `data: {"type":"response.completed","response":{"id":"resp_eof","usage":{"input_tokens":3,"output_tokens":2}}}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"X-Request-Id":                   []string{"request-eof"},
			"X-Ratelimit-Remaining-Requests": []string{"17"},
		},
		Body: io.NopCloser(strings.NewReader(payload)),
	}

	result, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}, time.Now(), "model", "model", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Nil(t, result.FirstTokenMs, "usage-only terminal event is not visible output")
	require.Equal(t, "resp_eof", result.ResponseID)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `"type":"response.completed"`)
	require.Contains(t, rec.Body.String(), `"id":"resp_eof"`)
	require.True(t, strings.HasSuffix(rec.Body.String(), "\n"))
	require.False(t, strings.HasSuffix(rec.Body.String(), "\n\n"), "EOF dispatch must not synthesize a blank line")
	require.Equal(t, "request-eof", rec.Result().Header.Get("X-Request-Id"))
	require.Equal(t, "17", rec.Result().Header.Get("X-Ratelimit-Remaining-Requests"))
}

func TestOpenAINativeFirstOutputStageOverflowFailsOverWithoutAttemptBytes(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{OpenAIFirstOutputTimeoutSeconds: 30, MaxLineSize: 2 * 1024 * 1024}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg, headers: compileHTTPFixtureHeaders(cfg)})
	const lineSize = 1024*1024 - 256
	prefix := `data: {"type":"response.output_text.delta","delta":"`
	suffix := `"}`
	line := prefix + strings.Repeat("x", lineSize-len(prefix)-len(suffix)) + suffix
	body := strings.Repeat(line+"\n", 9)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"X-Request-Id":                   []string{"request-overflow"},
			"X-Ratelimit-Remaining-Requests": []string{"1"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}, time.Now(), "model", "model", "")

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.Contains(t, string(failoverErr.ResponseBody), "staging limit exceeded")
	require.Empty(t, rec.Body.String())
	require.Empty(t, rec.Header().Values("X-Request-Id"))
	require.Empty(t, rec.Header().Values("X-Ratelimit-Remaining-Requests"))
}

func TestOpenAINativeFirstOutputScannerRejectsOversizedLineWithoutLeak(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{OpenAIFirstOutputTimeoutSeconds: 30, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg, headers: compileHTTPFixtureHeaders(cfg)})
	oversizedLine := "data: " + strings.Repeat("x", responseupstream.OpenAIFirstOutputStageMaxBytes+responseupstream.OpenAIFirstOutputScannerFramingAllowance+1024)
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_private\"}}\n\n" + oversizedLine + "\n"
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"X-Request-Id":                   []string{"request-too-large"},
			"X-Ratelimit-Remaining-Requests": []string{"1"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}, time.Now(), "model", "model", "")

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.Contains(t, string(failoverErr.ResponseBody), "line exceeds guarded first-output limit")
	require.Empty(t, rec.Body.String())
	require.Empty(t, rec.Header().Values("X-Request-Id"))
	require.Empty(t, rec.Header().Values("X-Ratelimit-Remaining-Requests"))
}

func TestOpenAINativeFirstOutputScannerAllowsLargeEventAfterSemanticBoundary(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{OpenAIFirstOutputTimeoutSeconds: 30, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg, headers: compileHTTPFixtureHeaders(cfg)})
	largeDelta := strings.Repeat("i", responseupstream.OpenAIFirstOutputStageMaxBytes+responseupstream.OpenAIFirstOutputScannerFramingAllowance+1024)
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"ready"}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"` + largeDelta + `"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_large_image","usage":{"input_tokens":4,"output_tokens":3}}}`,
		"",
	}, "\n")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"request-large-image"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	result, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}, time.Now(), "model", "model", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.FirstTokenMs)
	require.Equal(t, "resp_large_image", result.ResponseID)
	require.Equal(t, 4, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `"delta":"ready"`)
	require.Contains(t, rec.Body.String(), `"id":"resp_large_image"`)
	require.Contains(t, rec.Body.String(), strings.Repeat("i", 1024))
	require.Equal(t, "request-large-image", rec.Result().Header.Get("X-Request-Id"))
}

func TestOpenAINativeFirstOutputTimeoutDisabledKeepsPreamblePrivateAcrossKeepalive(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamKeepaliveInterval: 1, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}})
	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_stalled\"}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_stalled\"}}\n\n"))
		time.Sleep(2100 * time.Millisecond)
	}()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}, time.Now(), "model", "model", "")

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Contains(t, rec.Body.String(), ":\n\n")
	require.NotContains(t, rec.Body.String(), "response.created")
	require.NotContains(t, rec.Body.String(), "response.in_progress")
}

func TestOpenAINativeFirstOutputFailoverKeepsAttemptHeadersPrivateAfterKeepaliveCommit(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{OpenAIFirstOutputTimeoutSeconds: 2, StreamKeepaliveInterval: 1, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg, headers: compileHTTPFixtureHeaders(cfg)})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	firstBody, firstWriter := io.Pipe()
	trackedFirstBody := &firstOutputCloseTrackingBody{ReadCloser: firstBody, closed: make(chan struct{})}
	firstWriterDone := make(chan struct{})
	go func() {
		defer close(firstWriterDone)
		defer func() { _ = firstWriter.Close() }()
		_, _ = firstWriter.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_first\"}}\n\n"))
		select {
		case <-trackedFirstBody.closed:
		case <-time.After(4 * time.Second):
		}
	}()
	firstResp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"X-Request-Id":                   []string{"request-first"},
			"X-Ratelimit-Remaining-Requests": []string{"1"},
		},
		Body: trackedFirstBody,
	}

	_, firstErr := svc.Output.ReadStreamObservation(c.Request.Context(), firstResp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}, time.Now(), "model", "model", "")
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, firstErr, &failoverErr)
	require.Contains(t, rec.Body.String(), ":\n\n", "first attempt should have committed only a stable keepalive")
	require.NotContains(t, rec.Body.String(), "resp_first")

	secondResp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"X-Request-Id":                   []string{"request-second"},
			"X-Ratelimit-Remaining-Requests": []string{"99"},
		},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp_second","usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
		}, "\n"))),
	}
	result, secondErr := svc.Output.ReadStreamObservation(c.Request.Context(), secondResp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI}}, time.Now(), "model", "model", "")

	require.NoError(t, secondErr)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "resp_second")
	wireHeaders := rec.Result().Header
	require.Empty(t, wireHeaders.Values("X-Request-Id"))
	require.Empty(t, wireHeaders.Values("X-Ratelimit-Remaining-Requests"))
	require.Empty(t, rec.Header().Values("X-Request-Id"))
	require.Empty(t, rec.Header().Values("X-Ratelimit-Remaining-Requests"))
	select {
	case <-firstWriterDone:
	case <-time.After(time.Second):
		t.Fatal("first provider writer did not exit after timeout")
	}
}

func TestOpenAIFirstOutputStageDefaultLimitIsIndependentFromScannerLimit(t *testing.T) {
	stage := responseupstream.NewDefaultOpenAIFirstOutputStage()
	defer func() { require.NoError(t, stage.Close()) }()

	require.EqualValues(t, 8*1024*1024, stage.Limit())
	require.Greater(t, stage.Limit(), int64(68106))
	require.Less(t, stage.Limit(), int64(OpenAIResponseDefaultMaxLineSize))
}

func TestResponsesStreamAccessStateFailoverPrecedesPassthroughRule(t *testing.T) {
	stream := "event: response.failed\n" +
		`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"account_disabled","message":"Your provider is disabled"}}}` + "\n\n"
	tests := []struct {
		name string
		run  func(*wsExecutionFixture, *gin.Context, *http.Response, *gatewayprovider.ExecutionProvider) error
	}{
		{
			name: "native",
			run: func(svc *wsExecutionFixture, c *gin.Context, resp *http.Response, provider *gatewayprovider.ExecutionProvider) error {
				_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, provider, time.Now(), "gpt-5", "gpt-5", "")
				return err
			},
		},
		{
			name: "passthrough",
			run: func(svc *wsExecutionFixture, c *gin.Context, resp *http.Response, provider *gatewayprovider.ExecutionProvider) error {
				_, err := responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, provider), time.Now(), "gpt-5", "gpt-5")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			bindPassthroughRule(c, capability.PlatformOpenAI, []string{"provider is disabled"}, http.StatusTeapot)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(stream)),
			}
			svc := newWSFixture(wsFixtureInputs{options: &wsFixtureOptions{Output: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}})
			err := tt.run(svc, c, resp, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 11, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}})

			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.IsCredentialFailure())
			require.Equal(t, forwardcore.OpenAIUpstreamAccessStateReason, failoverErr.Reason)
			require.False(t, failoverErr.RetryableOnSameProvider)
			require.Equal(t, http.StatusBadGateway, failoverErr.ClientStatusCode)
			require.False(t, c.Writer.Written(), "passthrough rule must not commit a response before provider failover")
		})
	}
}

func TestResponsesStreamCyberPolicyPrecedesPassthroughRule(t *testing.T) {
	stream := "event: error\n" +
		`data: {"type":"error","error":{"code":"cyber_policy","message":"blocked by cyber policy"}}` + "\n\n"
	tests := []struct {
		name string
		run  func(*wsExecutionFixture, *gin.Context, *http.Response, *gatewayprovider.ExecutionProvider) error
	}{
		{
			name: "native",
			run: func(svc *wsExecutionFixture, c *gin.Context, resp *http.Response, provider *gatewayprovider.ExecutionProvider) error {
				_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, provider, time.Now(), "gpt-5", "gpt-5", "")
				return err
			},
		},
		{
			name: "passthrough",
			run: func(svc *wsExecutionFixture, c *gin.Context, resp *http.Response, provider *gatewayprovider.ExecutionProvider) error {
				_, err := responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, provider), time.Now(), "gpt-5", "gpt-5")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			bindPassthroughRule(c, capability.PlatformOpenAI, []string{"cyber policy"}, http.StatusTeapot)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(stream)),
			}
			svc := newWSFixture(wsFixtureInputs{options: &wsFixtureOptions{Output: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}})
			err := tt.run(svc, c, resp, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 12, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}})

			require.Error(t, err)
			var failoverErr *forwardcore.UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.NotNil(t, GetOpsCyberPolicy(c))
			require.NotEqual(t, http.StatusTeapot, rec.Code)
			require.Contains(t, rec.Body.String(), "cyber_policy")
		})
	}
}

// TestOpenAIResponsesStreaming_ResponseFailedCustomStatusFailsOver checks provider policy for failure events
// inside HTTP 200 streams.
func TestOpenAIResponsesStreaming_ResponseFailedCustomStatusFailsOver(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	failed := `{"type":"response.failed","response":{"status":"failed","error":{"status_code":422,"code":"configured","message":"configured failure"}}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: " + failed + "\n\n")),
	}
	repo := &openAIWSPolicyRepo{}
	options := rawChatCompletionsTestConfig()
	svc := newWSFixture(wsFixtureInputs{options: options, health: newUpstreamHealthForTest(repo, options, nil, providercore.HealthOptions{}, nil), corrector: responseupstream.NewCodexToolCorrector()})
	provider := rawChatCompletionsTestProvider()
	provider.Record.Credentials["custom_error_codes_enabled"] = true
	provider.Record.Credentials["custom_error_codes"] = []any{float64(http.StatusUnprocessableEntity)}
	_, err := svc.Output.ReadStreamObservation(context.Background(), resp, c, provider, time.Now(), "gpt-5.4", "gpt-5.4", "")

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusUnprocessableEntity, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameProvider)
	require.False(t, c.Writer.Written())
	require.Equal(t, 1, repo.setErrorCalls)
}

func newOpenAIResponseFlushRecorder() *openAIResponseFlushRecorder {
	return &openAIResponseFlushRecorder{
		header:          make(http.Header),
		failAfterWrites: -1,
		flushEvents:     make(chan int, 16),
	}
}

func (r *stagedOpenAISSEReadCloser) Read(data []byte) (int, error) {
	if len(r.current) == 0 {
		if r.index >= len(r.segments) {
			if r.eofReached != nil {
				close(r.eofReached)
				r.eofReached = nil
			}
			return 0, io.EOF
		}
		index := r.index
		r.index++
		if index < len(r.waiting) && r.waiting[index] != nil {
			close(r.waiting[index])
		}
		if index < len(r.gates) && r.gates[index] != nil {
			<-r.gates[index]
		}
		r.current = r.segments[index]
	}
	n := copy(data, r.current)
	r.current = r.current[n:]
	return n, nil
}

func (r *stagedOpenAISSEReadCloser) Close() error { return nil }

func (r *openAIResponseFlushReadError) Read(data []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(data, r.payload), nil
	}
	if r.err != nil {
		return 0, r.err
	}
	return 0, io.ErrUnexpectedEOF
}

func (r *openAIResponseFlushReadError) Close() error { return nil }

func TestOpenAIResponseFlush_SlowEventsFlushOnceAtBoundaries(t *testing.T) {
	events := []string{
		`data: {"type":"response.output_text.delta","delta":"a"}`,
		`data: {"type":"response.output_text.delta","delta":"b"}`,
		`data: {"type":"response.output_text.delta","delta":"c"}`,
		`data: [DONE]`,
	}
	body := strings.Join(events, "\n\n") + "\n\n"
	recorder := newOpenAIResponseFlushRecorder()

	result, err := runOpenAIResponseFlushTest(recorder, io.NopCloser(strings.NewReader(body)), OpenAIResponseOptions{})

	require.NoError(t, err)
	require.NotNil(t, result)
	gotBody, flushes := recorder.snapshot()
	require.Equal(t, body, gotBody)
	require.Len(t, flushes, len(events))
	for _, flushed := range flushes {
		require.True(t, strings.HasSuffix(flushed, "\n\n"), "flush must occur after a complete SSE event")
	}
}

func TestOpenAIResponseFlush_DataQueuedButBlankDrainsFlushesOnce(t *testing.T) {
	first := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\n"
	second := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"second\"}\n\n"
	terminal := "data: [DONE]\n\n"
	allowSecond := make(chan struct{})
	allowTerminal := make(chan struct{})
	terminalWaiting := make(chan struct{})
	reader := &stagedOpenAISSEReadCloser{
		segments: [][]byte{[]byte(first), []byte(second), []byte(terminal)},
		gates:    []<-chan struct{}{nil, allowSecond, allowTerminal},
		waiting:  []chan struct{}{nil, nil, terminalWaiting},
	}
	releaseFirstFlush := make(chan struct{})
	recorder := newOpenAIResponseFlushRecorder()
	recorder.blockFlush = 1
	recorder.flushBlocked = make(chan struct{})
	recorder.releaseFlush = releaseFirstFlush
	resultCh, errCh := runOpenAIResponseFlushTestAsync(recorder, reader, OpenAIResponseOptions{StreamDataIntervalTimeout: 30})

	waitOpenAIResponseFlushSignal(t, recorder.flushBlocked)
	close(allowSecond)
	waitOpenAIResponseFlushSignal(t, terminalWaiting)
	close(releaseFirstFlush)
	waitOpenAIResponseFlushCount(t, recorder, 2)
	close(allowTerminal)

	require.NoError(t, <-errCh)
	require.NotNil(t, <-resultCh)
	gotBody, flushes := recorder.snapshot()
	require.Equal(t, first+second+terminal, gotBody)
	require.Len(t, flushes, 3)
	require.Equal(t, first, flushes[0])
	require.Equal(t, first+second, flushes[1], "blank line that drains the queue must flush the complete event exactly once")
}

func TestOpenAIResponseFlush_BurstDoesNotIncreaseFlushes(t *testing.T) {
	first := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\n"
	burst := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"second"}`,
		`data: {"type":"response.output_text.delta","delta":"third"}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	allowBurst := make(chan struct{})
	eofReached := make(chan struct{})
	reader := &stagedOpenAISSEReadCloser{
		segments:   [][]byte{[]byte(first), []byte(burst)},
		gates:      []<-chan struct{}{nil, allowBurst},
		eofReached: eofReached,
	}
	releaseFirstFlush := make(chan struct{})
	recorder := newOpenAIResponseFlushRecorder()
	recorder.blockFlush = 1
	recorder.flushBlocked = make(chan struct{})
	recorder.releaseFlush = releaseFirstFlush
	resultCh, errCh := runOpenAIResponseFlushTestAsync(recorder, reader, OpenAIResponseOptions{StreamDataIntervalTimeout: 30})

	waitOpenAIResponseFlushSignal(t, recorder.flushBlocked)
	close(allowBurst)
	waitOpenAIResponseFlushSignal(t, eofReached)
	close(releaseFirstFlush)

	require.NoError(t, <-errCh)
	require.NotNil(t, <-resultCh)
	gotBody, flushes := recorder.snapshot()
	require.Equal(t, first+burst, gotBody)
	require.Len(t, flushes, 2, "queued burst must remain batched until its drained event boundary")
	require.Equal(t, first, flushes[0])
	require.Equal(t, first+burst, flushes[1])
}

func TestOpenAIResponseFlush_CommentAndEOFOnlyFlushCompleteResidual(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"a\"}\n\n" +
		": upstream-comment\n\n" +
		"data: [DONE]\n"
	recorder := newOpenAIResponseFlushRecorder()

	result, err := runOpenAIResponseFlushTest(recorder, io.NopCloser(strings.NewReader(body)), OpenAIResponseOptions{})

	require.NoError(t, err)
	require.NotNil(t, result)
	gotBody, flushes := recorder.snapshot()
	require.Equal(t, body, gotBody)
	require.Len(t, flushes, 3)
	require.True(t, strings.HasSuffix(flushes[0], "\n\n"))
	require.True(t, strings.HasSuffix(flushes[1], "\n\n"))
	require.True(t, strings.HasSuffix(flushes[2], "data: [DONE]\n"), "EOF must flush only the remaining bytes")
}

func TestOpenAIResponseFlush_TerminalReadErrorFlushesResidual(t *testing.T) {
	body := "data: [DONE]\n"
	recorder := newOpenAIResponseFlushRecorder()

	result, err := runOpenAIResponseFlushTest(recorder, &openAIResponseFlushReadError{payload: []byte(body)}, OpenAIResponseOptions{})

	require.NoError(t, err)
	require.NotNil(t, result)
	gotBody, flushes := recorder.snapshot()
	require.Equal(t, body, gotBody)
	require.Equal(t, []string{body}, flushes)
}

func TestOpenAIResponseFlush_OutputWithoutTerminalFlushesResidualWithoutFailover(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n"
	recorder := newOpenAIResponseFlushRecorder()

	result, err := runOpenAIResponseFlushTest(recorder, io.NopCloser(strings.NewReader(body)), OpenAIResponseOptions{})

	require.ErrorContains(t, err, "missing terminal event")
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.NotNil(t, result)
	gotBody, flushes := recorder.snapshot()
	require.Equal(t, body, gotBody)
	require.Equal(t, []string{body}, flushes)
}

func TestOpenAIResponseFlush_PreambleWithoutTerminalRemainsBufferedForFailover(t *testing.T) {
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n"
	recorder := newOpenAIResponseFlushRecorder()

	result, err := runOpenAIResponseFlushTest(recorder, io.NopCloser(strings.NewReader(body)), OpenAIResponseOptions{})

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.NotNil(t, result)
	gotBody, flushes := recorder.snapshot()
	require.Empty(t, gotBody)
	require.Empty(t, flushes)
}

func TestOpenAIResponseFlush_CanceledAfterOutputFlushesResidualWithoutErrorEvent(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n"
	recorder := newOpenAIResponseFlushRecorder()

	result, err := runOpenAIResponseFlushTest(recorder, &openAIResponseFlushReadError{payload: []byte(body), err: context.Canceled}, OpenAIResponseOptions{})

	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	gotBody, flushes := recorder.snapshot()
	require.Equal(t, body, gotBody)
	require.Equal(t, []string{body}, flushes)
	require.NotContains(t, gotBody, "stream_read_error")
}

func TestOpenAIResponseFlush_KeepaliveFlushesImmediately(t *testing.T) {
	recorder := newOpenAIResponseFlushRecorder()
	reader, writer := io.Pipe()
	resultCh, errCh := runOpenAIResponseFlushTestAsync(recorder, reader, OpenAIResponseOptions{StreamKeepaliveInterval: 1})

	waitOpenAIResponseFlushCount(t, recorder, 1)
	_, flushes := recorder.snapshot()
	require.Equal(t, ":\n\n", flushes[0])
	_, err := writer.Write([]byte("data: [DONE]\n\n"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	require.NoError(t, <-errCh)
	require.NotNil(t, <-resultCh)
	gotBody, flushes := recorder.snapshot()
	require.Equal(t, ":\n\ndata: [DONE]\n\n", gotBody)
	require.Len(t, flushes, 2)
}

func TestOpenAIResponseFlush_KeepaliveDoesNotSplitOpenEvent(t *testing.T) {
	const dataLine = `data: {"type":"response.output_text.delta","delta":"a"}`
	// Fill the 16-slot scanner queue so the main loop processes data before the controlled blank line.
	dataLines := make([]string, 17)
	for i := range dataLines {
		dataLines[i] = dataLine
	}
	partialEvent := strings.Join(dataLines, "\n") + "\n"
	completeEvent := partialEvent + "\n"
	terminal := "data: [DONE]\n\n"
	allowBlank := make(chan struct{})
	allowTerminal := make(chan struct{})
	blankWaiting := make(chan struct{})
	terminalWaiting := make(chan struct{})
	reader := &stagedOpenAISSEReadCloser{
		segments: [][]byte{[]byte(partialEvent), []byte("\n"), []byte(terminal)},
		gates:    []<-chan struct{}{nil, allowBlank, allowTerminal},
		waiting:  []chan struct{}{nil, blankWaiting, terminalWaiting},
	}
	recorder := newOpenAIResponseFlushRecorder()
	resultCh, errCh := runOpenAIResponseFlushTestAsync(recorder, reader, OpenAIResponseOptions{StreamKeepaliveInterval: 1})

	waitOpenAIResponseFlushSignal(t, blankWaiting)
	timer := time.NewTimer(1250 * time.Millisecond)
	select {
	case count := <-recorder.flushEvents:
		timer.Stop()
		t.Fatalf("keepalive flushed open event before its blank boundary: flush %d", count)
	case <-timer.C:
	}

	close(allowBlank)
	waitOpenAIResponseFlushSignal(t, terminalWaiting)
	waitOpenAIResponseFlushCount(t, recorder, 1)
	gotBody, flushes := recorder.snapshot()
	require.Equal(t, completeEvent, gotBody)
	require.Equal(t, []string{completeEvent}, flushes)

	close(allowTerminal)
	require.NoError(t, <-errCh)
	require.NotNil(t, <-resultCh)
	gotBody, flushes = recorder.snapshot()
	require.Equal(t, completeEvent+terminal, gotBody)
	require.Len(t, flushes, 2)
	require.Equal(t, completeEvent+terminal, flushes[1])
}

func TestOpenAIResponseFlush_FailedAndErrorEventsFlushAtBoundaries(t *testing.T) {
	t.Run("failed at EOF", func(t *testing.T) {
		body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"a\"}\n\n" +
			"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"safety_error\",\"message\":\"blocked\"},\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n"
		recorder := newOpenAIResponseFlushRecorder()

		result, err := runOpenAIResponseFlushTest(recorder, io.NopCloser(strings.NewReader(body)), OpenAIResponseOptions{})

		require.Error(t, err)
		require.NotNil(t, result)
		require.Equal(t, 3, result.Usage.InputTokens)
		gotBody, flushes := recorder.snapshot()
		expectedBody := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"a\"}\n\n" +
			"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"safety_error\",\"message\":\"blocked\"}}}\n"
		require.Equal(t, expectedBody, gotBody)
		require.Len(t, flushes, 2)
		require.Contains(t, flushes[1], "response.failed")
	})

	t.Run("bare error synthesizes failed before done", func(t *testing.T) {
		body := "data: {\"type\":\"error\",\"error\":{\"message\":\"failed\"}}\n\n" +
			"data: [DONE]\n\n"
		recorder := newOpenAIResponseFlushRecorder()

		result, err := runOpenAIResponseFlushTest(recorder, io.NopCloser(strings.NewReader(body)), OpenAIResponseOptions{})

		require.Error(t, err)
		require.NotNil(t, result)
		gotBody, flushes := recorder.snapshot()
		require.NotContains(t, gotBody, `"type":"error"`)
		require.Equal(t, 1, strings.Count(gotBody, `"type":"response.failed"`))
		require.Contains(t, gotBody, `"status":"failed"`)
		require.NotContains(t, gotBody, "[DONE]")
		require.Len(t, flushes, 1)
	})

	t.Run("bare error forwards following failed and drains terminal usage", func(t *testing.T) {
		errorEvent := "data: {\"type\":\"error\",\"error\":{\"code\":\"invalid_request\",\"message\":\"bad request\"}}\n\n"
		body := errorEvent +
			"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"usage\":{\"input_tokens\":9,\"output_tokens\":2},\"error\":{\"code\":\"invalid_request\",\"message\":\"bad request\"}}}\n\n" +
			"data: [DONE]\n\n"
		recorder := newOpenAIResponseFlushRecorder()

		result, err := runOpenAIResponseFlushTest(recorder, io.NopCloser(strings.NewReader(body)), OpenAIResponseOptions{})

		require.Error(t, err)
		require.NotNil(t, result)
		require.Equal(t, 9, result.Usage.InputTokens)
		require.Equal(t, 2, result.Usage.OutputTokens)
		gotBody, flushes := recorder.snapshot()
		require.NotContains(t, gotBody, `"type":"error"`)
		require.Equal(t, 1, strings.Count(gotBody, `"type":"response.failed"`))
		require.Contains(t, gotBody, `"id":"resp_failed"`)
		require.NotContains(t, gotBody, "[DONE]")
		require.Len(t, flushes, 1)
	})

	t.Run("non-retryable error event flushes at boundary", func(t *testing.T) {
		body := "data: {\"type\":\"error\",\"error\":{\"code\":\"invalid_request\",\"message\":\"bad request\"}}\n\n" +
			"data: [DONE]\n\n"
		recorder := newOpenAIResponseFlushRecorder()

		result, err := runOpenAIResponseFlushTest(recorder, io.NopCloser(strings.NewReader(body)), OpenAIResponseOptions{})

		require.Error(t, err)
		require.NotNil(t, result)
		gotBody, flushes := recorder.snapshot()
		require.NotContains(t, gotBody, `"type":"error"`)
		require.Equal(t, 1, strings.Count(gotBody, `"type":"response.failed"`))
		require.Len(t, flushes, 1)
	})
}

func TestOpenAIResponseFlush_BareErrorFollowedByCompletedUsesCompletedTerminal(t *testing.T) {
	body := "data: {\"type\":\"error\",\"error\":{\"code\":\"transient\",\"message\":\"retrying\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_recovered\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n"
	recorder := newOpenAIResponseFlushRecorder()

	result, err := runOpenAIResponseFlushTest(recorder, io.NopCloser(strings.NewReader(body)), OpenAIResponseOptions{})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	gotBody, _ := recorder.snapshot()
	require.NotContains(t, gotBody, `"type":"error"`)
	require.NotContains(t, gotBody, `"type":"response.failed"`)
	require.Contains(t, gotBody, `"type":"response.completed"`)
}

func TestOpenAIResponseFlush_CompatibleAPIKeyDoesNotUseCodexBareErrorSynthesis(t *testing.T) {
	body := "data: {\"type\":\"error\",\"error\":{\"code\":\"provider_error\",\"message\":\"provider failed\"}}\n\n"
	recorder := newOpenAIResponseFlushRecorder()
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	result, err := runOpenAIResponseFlushTestWithProvider(recorder, io.NopCloser(strings.NewReader(body)), OpenAIResponseOptions{}, provider)

	require.Error(t, err)
	require.NotNil(t, result)
	gotBody, _ := recorder.snapshot()
	require.Contains(t, gotBody, `"type":"error"`)
	require.NotContains(t, gotBody, `"type":"response.failed"`)
}

func TestOpenAIResponseFlush_RecentBareErrorAllowsCompletedBeforeIdleTimeout(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	recorder := newOpenAIResponseFlushRecorder()
	resultCh, errCh := runOpenAIResponseFlushTestAsync(recorder, reader, OpenAIResponseOptions{StreamDataIntervalTimeout: 1})

	// Place the bare error shortly before the first ticker firing. It is fresh
	// data, so the ticker must leave the stream open for an authoritative event.
	time.Sleep(700 * time.Millisecond)
	_, err := io.WriteString(writer, "data: {\"type\":\"error\",\"error\":{\"code\":\"transient\",\"message\":\"retrying\"}}\n\n")
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)
	_, err = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_late\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	require.NoError(t, <-errCh)
	result := <-resultCh
	require.NotNil(t, result)
	require.Equal(t, 5, result.Usage.InputTokens)
	gotBody, _ := recorder.snapshot()
	require.Contains(t, gotBody, `"type":"response.completed"`)
	require.NotContains(t, gotBody, `"type":"response.failed"`)
}

func TestOpenAIResponseFlush_BareErrorTimeoutSynthesizesFailed(t *testing.T) {
	tests := []struct {
		name string
		cfg  OpenAIResponseOptions
	}{
		{
			name: "stream interval timeout",
			cfg:  OpenAIResponseOptions{StreamDataIntervalTimeout: 1},
		},
		{
			name: "first output timeout",
			cfg:  OpenAIResponseOptions{OpenAIFirstOutputTimeoutSeconds: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer func() { _ = writer.Close() }()
			recorder := newOpenAIResponseFlushRecorder()
			resultCh, errCh := runOpenAIResponseFlushTestAsync(recorder, reader, tt.cfg)

			_, writeErr := io.WriteString(writer, "data: {\"type\":\"error\",\"error\":{\"code\":\"invalid_request\",\"message\":\"bad request\"}}\n\n")
			require.NoError(t, writeErr)

			select {
			case err := <-errCh:
				require.Error(t, err)
				require.Contains(t, err.Error(), "upstream response failed")
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for bare error terminal synthesis")
			}
			require.NotNil(t, <-resultCh)
			body, flushes := recorder.snapshot()
			require.NotContains(t, body, `"type":"error"`)
			require.Equal(t, 1, strings.Count(body, `"type":"response.failed"`))
			require.Len(t, flushes, 1)
		})
	}
}

func TestOpenAIResponseFlush_ReusedTypeKeepsSSEBytesAndTerminalSemantics(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		flushCount int
	}{
		{
			name:       "whitespace around done",
			body:       "data: \t[DONE]  \n\n",
			flushCount: 1,
		},
		{
			name:       "invalid JSON before done",
			body:       "data: {\"type\":\n\ndata: [DONE]\n\n",
			flushCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := newOpenAIResponseFlushRecorder()

			result, err := runOpenAIResponseFlushTest(recorder, io.NopCloser(strings.NewReader(tt.body)), OpenAIResponseOptions{})

			require.NoError(t, err)
			require.NotNil(t, result)
			gotBody, flushes := recorder.snapshot()
			require.Equal(t, tt.body, gotBody)
			require.Len(t, flushes, tt.flushCount)
		})
	}
}

func TestOpenAIResponseFlush_ClientDisconnectStillDrainsUsage(t *testing.T) {
	first := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"a\"}\n\n"
	terminal := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":2}}}}\n\n"
	recorder := newOpenAIResponseFlushRecorder()
	recorder.failAfterWrites = 1

	result, err := runOpenAIResponseFlushTest(recorder, io.NopCloser(strings.NewReader(first+terminal)), OpenAIResponseOptions{})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Equal(t, 2, result.Usage.CacheReadInputTokens)
	gotBody, flushes := recorder.snapshot()
	require.Equal(t, first, gotBody)
	require.Len(t, flushes, 1)
}

func runOpenAIResponseFlushTest(recorder *openAIResponseFlushRecorder, body io.ReadCloser, gatewayCfg OpenAIResponseOptions) (*responseupstream.StreamingResult, error) {
	return runOpenAIResponseFlushTestWithProvider(recorder, body, gatewayCfg, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}})
}

func runOpenAIResponseFlushTestWithProvider(recorder *openAIResponseFlushRecorder, body io.ReadCloser, gatewayCfg OpenAIResponseOptions, provider *gatewayprovider.ExecutionProvider) (*responseupstream.StreamingResult, error) {
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	output := newAuxiliaryFixture(auxiliaryFixtureInputs{}).Output
	output.Options = gatewayCfg
	output.Options.Configured = true
	output.Options.ReadLimit = 128 * 1024 * 1024
	output.Corrector = responseupstream.NewCodexToolCorrector()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}
	return output.ReadStreamObservation(context.Background(), resp, c, provider, time.Now(), "gpt-5", "gpt-5", "")
}

func runOpenAIResponseFlushTestAsync(recorder *openAIResponseFlushRecorder, body io.ReadCloser, gatewayCfg OpenAIResponseOptions) (<-chan *responseupstream.StreamingResult, <-chan error) {
	resultCh := make(chan *responseupstream.StreamingResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := runOpenAIResponseFlushTest(recorder, body, gatewayCfg)
		resultCh <- result
		errCh <- err
	}()
	return resultCh, errCh
}

func waitOpenAIResponseFlushCount(t *testing.T, recorder *openAIResponseFlushRecorder, want int) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case count := <-recorder.flushEvents:
			if count >= want {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for flush %d", want)
		}
	}
}

func waitOpenAIResponseFlushSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stream signal")
	}
}

func (c cancelReadCloser) Read(p []byte) (int, error) { return 0, context.Canceled }

func (c cancelReadCloser) Close() error { return nil }

func (r errReadCloser) Read([]byte) (int, error) { return 0, r.err }

func (r errReadCloser) Close() error { return nil }

func TestExtractOpenAIResponseIDFromJSONBytes(t *testing.T) {
	require.Equal(t, "resp_json", protocolopenai.ExtractOpenAIResponseIDFromJSONBytes([]byte(`{"id":"resp_json"}`)))
	require.Equal(t, "resp_sse", protocolopenai.ExtractOpenAIResponseIDFromJSONBytes([]byte(`{"type":"response.completed","response":{"id":"resp_sse"}}`)))
	require.Empty(t, protocolopenai.ExtractOpenAIResponseIDFromJSONBytes([]byte(`{"response":{}}`)))
	require.Empty(t, protocolopenai.ExtractOpenAIResponseIDFromJSONBytes([]byte(`not-json`)))
}

// TestExtractOpenAIUsage_CapturesImageInputTokens covers input_tokens_details.image_tokens in gpt-image-2
// edits (#4386).
// Image input tokens belong in ImageInputTokens so they receive image pricing.
func TestExtractOpenAIUsage_CapturesImageInputTokens(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":371,"input_tokens_details":{"image_tokens":352,"text_tokens":19},"output_tokens":439,"output_tokens_details":{"image_tokens":439,"text_tokens":0},"total_tokens":810}}`)
	usage, ok := protocolopenai.ExtractOpenAIUsageFromJSONBytes(body)
	require.True(t, ok)
	require.Equal(t, 371, usage.InputTokens)
	require.Equal(t, 352, usage.ImageInputTokens)
	require.Equal(t, 439, usage.OutputTokens)
	require.Equal(t, 439, usage.ImageOutputTokens)

	// Some upstreams use prompt_tokens; check the prompt_tokens_details fallback.
	promptStyle := []byte(`{"usage":{"prompt_tokens":100,"prompt_tokens_details":{"image_tokens":80}}}`)
	pu, ok := protocolopenai.ExtractOpenAIUsageFromJSONBytes(promptStyle)
	require.True(t, ok)
	require.Equal(t, 100, pu.InputTokens)
	require.Equal(t, 80, pu.ImageInputTokens)

	// Text-only usage leaves ImageInputTokens at zero.
	textOnly := []byte(`{"usage":{"input_tokens":50,"output_tokens":10}}`)
	tu, ok := protocolopenai.ExtractOpenAIUsageFromJSONBytes(textOnly)
	require.True(t, ok)
	require.Zero(t, tu.ImageInputTokens)
}

func TestExtractOpenAIUsage_ReadsClineDataEnvelope(t *testing.T) {
	body := []byte(`{"data":{"choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":8,"completion_tokens":27,"total_tokens":35,"prompt_tokens_details":{"cached_tokens":4}}},"success":true}`)

	usage, ok := protocolopenai.ExtractOpenAIUsageFromJSONBytes(body)

	require.True(t, ok)
	require.Equal(t, 8, usage.InputTokens)
	require.Equal(t, 27, usage.OutputTokens)
	require.Equal(t, 4, usage.CacheReadInputTokens)
}

func TestExtractOpenAIUsage_ReadsWrappedResponsesDataEnvelope(t *testing.T) {
	body := []byte(`{"data":{"response":{"usage":{"input_tokens":11,"output_tokens":5,"total_tokens":16,"input_tokens_details":{"cached_tokens":2}}}}}`)

	usage, ok := protocolopenai.ExtractOpenAIUsageFromJSONBytes(body)

	require.True(t, ok)
	require.Equal(t, 11, usage.InputTokens)
	require.Equal(t, 5, usage.OutputTokens)
	require.Equal(t, 2, usage.CacheReadInputTokens)
}

func TestExtractOpenAIUsage_PreservesResponseUsagePriority(t *testing.T) {
	body := []byte(`{"data":{"usage":{"prompt_tokens":100,"completion_tokens":50}},"response":{"usage":{"input_tokens":11,"output_tokens":5}}}`)

	usage, ok := protocolopenai.ExtractOpenAIUsageFromJSONBytes(body)

	require.True(t, ok)
	require.Equal(t, 11, usage.InputTokens)
	require.Equal(t, 5, usage.OutputTokens)
}

func TestOpenAIStreamingTimeout(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 1, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{},
	}

	start := time.Now()
	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, start, "model", "model", "")
	_ = pw.Close()
	_ = pr.Close()

	if err == nil || !strings.Contains(err.Error(), "stream data interval timeout") {
		t.Fatalf("expected stream timeout error, got %v", err)
	}
	if !strings.Contains(rec.Body.String(), "\"type\":\"error\"") || !strings.Contains(rec.Body.String(), "stream_timeout") {
		t.Fatalf("expected OpenAI-compatible error SSE event, got %q", rec.Body.String())
	}
}

func TestOpenAIStreamingContextCanceledReturnsIncompleteErrorWithoutInjectingErrorEvent(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       cancelReadCloser{},
		Header:     http.Header{},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", "")
	if err == nil || !strings.Contains(err.Error(), "stream usage incomplete") {
		t.Fatalf("expected incomplete stream error, got %v", err)
	}
	if strings.Contains(rec.Body.String(), "event: error") || strings.Contains(rec.Body.String(), "stream_read_error") {
		t.Fatalf("expected no injected SSE error event, got %q", rec.Body.String())
	}
}

func TestOpenAIStreamingReadErrorBeforeOutputReturnsFailover(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       errReadCloser{err: io.ErrUnexpectedEOF},
		Header:     http.Header{"X-Request-Id": []string{"rid-disconnect"}},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestOpenAIStreamingPostOutputDisconnectQuarantinesSharedProxyWithoutSameStreamFailover(t *testing.T) {
	proxyID := int64(4698)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 469801,
			Name:     "oauth-on-shared-proxy",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			ProxyID:  &proxyID,
		},
	}
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}})
	// Disable the production concurrent-truncation coalescing window so the two loops count as independent
	// failures.
	svc.Output.ProxyCircuit = egress.NewProxyStreamCircuit(egress.ProxyStreamCircuitSettings{
		FailureThreshold: 2,
		FailureWindow:    time.Minute,
		QuarantineTTL:    10 * time.Minute,
		MaxEntries:       16,
	})

	for _, readErr := range []error{
		io.ErrUnexpectedEOF,
		errors.New("http2: client connection lost"),
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body: &openAIStreamReadThenErrorCloser{
				reader: strings.NewReader(strings.Join([]string{
					"event: response.output_text.delta",
					`data: {"type":"response.output_text.delta","delta":"partial"}`,
					"",
				}, "\n")),
				err: readErr,
			},
			Header: http.Header{"X-Request-Id": []string{"rid-proxy-disconnect"}},
		}

		_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, provider, time.Now(), "gpt-5.6-sol", "gpt-5.6-sol", "")
		require.Error(t, err)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr), "post-output disconnect must not fail over inside the same stream")
		require.Contains(t, rec.Body.String(), "partial")
	}

	compatible, reason := selectionDiagnosticForStreamTest(t, svc, provider)
	require.False(t, compatible, "the next request must exclude providers sharing the quarantined proxy")
	require.Equal(t, "proxy_stream_quarantined", reason)
}

func TestOpenAIStreamingTerminalAndClientCancellationDoNotQuarantineProxy(t *testing.T) {
	proxyID := int64(4699)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 469901, Name: "oauth", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, ProxyID: &proxyID}}
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}})

	terminalRecorder := httptest.NewRecorder()
	terminalCtx, _ := gin.CreateTestContext(terminalRecorder)
	terminalCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	terminalResp := &http.Response{
		StatusCode: http.StatusOK,
		Body: &openAIStreamReadThenErrorCloser{
			reader: strings.NewReader(strings.Join([]string{
				"event: response.completed",
				`data: {"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}`,
				"",
			}, "\n")),
			err: io.ErrUnexpectedEOF,
		},
		Header: http.Header{},
	}
	_, err := svc.Output.ReadStreamObservation(terminalCtx.Request.Context(), terminalResp, terminalCtx, provider, time.Now(), "model", "model", "")
	require.NoError(t, err)

	for range 2 {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body: &openAIStreamReadThenErrorCloser{
				reader: strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"),
				err:    context.Canceled,
			},
			Header: http.Header{},
		}
		_, err = svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, provider, time.Now(), "model", "model", "")
		require.Error(t, err)
	}

	compatible, reason := selectionDiagnosticForStreamTest(t, svc, provider)
	require.True(t, compatible)
	require.Empty(t, reason)
}

func TestOpenAIStreamingResponseFailedBeforeOutputReturnsFailover(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.in_progress",
			`data: {"type":"response.in_progress","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","error":{"message":"An error occurred while processing your request."}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-failed"}},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameProvider)
	require.Contains(t, string(failoverErr.ResponseBody), "An error occurred while processing your request")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestOpenAIStreamingResponseFailedCapacityBeforeOutputReturnsFailover(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.failed",
			`data: {"type":"response.failed","response":{"error":{"message":"The selected model is at capacity. Please try again later."}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-capacity"}},
	}

	// Transient capacity failures get bounded same-provider retries even when pool mode does not list 502.
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Name:     "pool-provider",
			Credentials: map[string]any{
				"pool_mode": true,
			},
		},
	}
	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, provider, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameProvider)
	require.Contains(t, string(failoverErr.ResponseBody), "selected model is at capacity")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestOpenAIStreamingResponseFailedBeforeOutputServerOverloadedCodeReturnsFailover(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","error":{"code":"server_is_overloaded","message":"Please retry later."}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-overloaded-failed"}},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "Please retry later")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestOpenAIStreamingResponseFailedBeforeOutputRateLimitUsesPoolRetryPolicy(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"type":"invalid_request_error","code":"rate_limit_exceeded","message":"Concurrency limit exceeded for provider, please retry later"}}}`,
			"",
		}, "\n"))),
		Header: http.Header{
			"X-Request-Id": []string{"rid-rate-limit-failed"},
			"Retry-After":  []string{"1"},
		},
	}
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Name:     "pool-provider",
			Credentials: map[string]any{
				"pool_mode":                    true,
				"pool_mode_retry_count":        float64(1),
				"pool_mode_retry_status_codes": []any{float64(http.StatusTooManyRequests)},
			},
		},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, provider, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameProvider)
	require.Equal(t, "1", http.Header(failoverErr.ResponseHeaders).Get("Retry-After"))
	require.Equal(t, "rate_limit_error", gjson.GetBytes(failoverErr.ResponseBody, "error.type").String())
	require.Contains(t, string(failoverErr.ResponseBody), "Concurrency limit exceeded")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())

	opsValue, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	opsEvents, ok := opsValue.([]*ops.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.NotEmpty(t, opsEvents)
	require.Equal(t, http.StatusTooManyRequests, opsEvents[len(opsEvents)-1].UpstreamStatusCode)
}

// TestOpenAIStreamingResponseFailedRateLimitDoesNotBlockProviderScheduling checks OAuth same-provider retries
// before provider-wide rate limiting.
// HTTP 200 x-codex-* headers must not remove scheduling eligibility during that retry window.
func TestOpenAIStreamingResponseFailedRateLimitDoesNotBlockProviderScheduling(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"rate_limit_exceeded","message":"Concurrency limit exceeded for provider, please retry later"}}}`,
			"",
		}, "\n"))),
		Header: http.Header{
			"X-Codex-Primary-Used-Percent":        []string{"12"},
			"X-Codex-Primary-Reset-After-Seconds": []string{"604800"},
			"Retry-After":                         []string{"1"},
		},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 11, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Name: "oauth-provider"}}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, provider, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameProvider)
	require.False(t, failoverErr.SameProviderRetryDeadline.IsZero())
	require.False(t, httpFixtureRuntimeBlocked(svc, provider))
}

func TestOpenAIStreamingResponseFailedAfterOutputSanitizesVerboseResponseForClient(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	longInstructions := strings.Repeat("You are GPT-5.1 running in the Codex CLI. ", 20)
	failedPayload := fmt.Sprintf(
		`{"type":"response.failed","response":{"id":"resp_failed","object":"response","created_at":1782446336,"status":"failed","instructions":%q,"output":[{"type":"message","content":[{"type":"output_text","text":"large"}]}],"usage":{"input_tokens":123,"output_tokens":0},"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}}`,
		longInstructions,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_failed"}}`,
			"",
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","delta":"partial"}`,
			"",
			"event: response.failed",
			"data: " + failedPayload,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-failed-after-output"}},
	}

	result, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "流已向客户端输出后不得重放请求")
	require.NotNil(t, result)
	require.Equal(t, 123, result.Usage.InputTokens)

	body := rec.Body.String()
	require.Contains(t, body, "event: response.failed")
	require.Contains(t, body, "context_length_exceeded")
	require.Contains(t, body, `"type":"invalid_request_error"`)
	require.Contains(t, body, "Your input exceeds the context window")
	require.NotContains(t, body, "You are GPT-5.1 running in the Codex CLI")
	require.NotContains(t, body, `"instructions"`)
	require.NotContains(t, body, `"output"`)
	require.NotContains(t, body, `"usage"`)
}

func TestOpenAIStreamingContextWindowResponseFailedBeforeOutputPassesThrough(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","error":{"type":"upstream_error","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","code":null}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-context-window-failed"}},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.True(t, c.Writer.Written())
	require.Contains(t, rec.Body.String(), "response.failed")
	require.Contains(t, rec.Body.String(), `"type":"upstream_error"`)
	require.Contains(t, rec.Body.String(), "Your input exceeds the context window")
}

func TestOpenAIStreamingContextWindowResponseFailedBeforeOutputAppliesPassthroughRule(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	rule := gatewaytestkit.NonFailoverRule(http.StatusBadRequest, "context_length_exceeded", http.StatusBadRequest, "")
	rule.Platforms = []string{capability.PlatformOpenAI}
	rule.PassthroughBody = true
	rule.CustomMessage = nil
	ruleSvc := gatewaytestkit.ErrorRules([]*errorpolicy.ErrorPassthroughRule{rule})
	BindErrorPassthroughService(c, ruleSvc)

	upstreamMessage := "Your input exceeds the context window of this model. Please adjust your input and try again."
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"` + upstreamMessage + `"}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-context-window-passthrough-rule"}},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.True(t, IsResponseCommitted(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	body := rec.Body.String()
	require.Equal(t, "upstream_error", gjson.Get(body, "error.type").String())
	require.Equal(t, upstreamMessage, gjson.Get(body, "error.message").String())
	require.NotContains(t, body, "response.failed")
	require.NotContains(t, body, "Upstream request failed")
	// A matched passthrough rule still records an Ops upstream error event.
	opsVal, opsRecorded := c.Get(OpsUpstreamErrorsKey)
	require.True(t, opsRecorded, "passthrough hit should record an ops upstream error event")
	opsEvents, _ := opsVal.([]*ops.OpsUpstreamErrorEvent)
	require.NotEmpty(t, opsEvents)
}

func TestOpenAIStreamingPreambleOnlyMissingTerminalReturnsFailover(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.in_progress",
			`data: {"type":"response.in_progress","response":{"id":"resp_1"}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-missing-terminal"}},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestOpenAIStreamingPreambleKeepaliveUsesDownstreamIdle(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 1, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"))
		for range 6 {
			time.Sleep(250 * time.Millisecond)
			_, _ = pw.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_1\"}}\n\n"))
		}
		_, _ = pw.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n"))
	}()

	result, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), ":\n\n")
	require.Contains(t, rec.Body.String(), "response.completed")
}

func TestOpenAIStreamingNormalizesTerminalOutputFromDeltas(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_sdk_parse"}}`,
			"",
			`data: {"type":"response.output_text.delta","delta":"pon"}`,
			"",
			`data: {"type":"response.output_text.delta","delta":"g"}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp_sdk_parse","status":"completed","output":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-sdk-parse"}},
	}

	result, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")
	require.NoError(t, err)
	require.NotNil(t, result)

	terminalType, terminalPayload, ok := protocolopenai.ExtractOpenAISSETerminalEvent(rec.Body.String())
	require.True(t, ok)
	require.Equal(t, "response.completed", terminalType)
	output := gjson.GetBytes(terminalPayload, "response.output")
	require.True(t, output.IsArray())
	require.Len(t, output.Array(), 1)
	require.Equal(t, "pong", gjson.GetBytes(terminalPayload, "response.output.0.content.0.text").String())
}

func TestOpenAIStreamingNormalizesTerminalOutputToEmptyArray(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":{"id":"resp_empty","status":"completed","output":null,"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n" + "")),
		Header:     http.Header{"X-Request-Id": []string{"rid-empty-output"}},
	}

	result, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")
	require.NoError(t, err)
	require.NotNil(t, result)

	terminalType, terminalPayload, ok := protocolopenai.ExtractOpenAISSETerminalEvent(rec.Body.String())
	require.True(t, ok)
	require.Equal(t, "response.completed", terminalType)
	output := gjson.GetBytes(terminalPayload, "response.output")
	require.True(t, output.IsArray())
	require.Len(t, output.Array(), 0)
}

func TestOpenAIStreamingPolicyResponseFailedBeforeOutputPassesThrough(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","error":{"type":"safety_error","message":"This request has been flagged for potentially high-risk cyber activity."}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-policy-failed"}},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.True(t, c.Writer.Written())
	require.Contains(t, rec.Body.String(), "response.failed")
	require.Contains(t, rec.Body.String(), "high-risk cyber activity")
}

func TestOpenAIStreamingPolicyResponseFailedCarriesHTTPStatusWarning(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","error":{"type":"safety_error","message":"This request has been flagged for potentially high-risk cyber activity."}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-policy-failed"}},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")

	require.Error(t, err)
	warning, ok := forwardcore.WarningFromError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusOK, warning.StatusCode)
	require.Contains(t, warning.Message, "high-risk cyber activity")
	require.Contains(t, string(warning.ResponseBody), "response.failed")
}

func TestOpenAIStreamingCybersecurityRiskResponseFailedCarriesHTTPStatusWarning(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"error":{"message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request."}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-cybersecurity-risk"}},
	}

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}, time.Now(), "model", "model", "")

	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	warning, ok := forwardcore.WarningFromError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusOK, warning.StatusCode)
	require.Contains(t, warning.Message, "cybersecurity risk")
	require.Contains(t, string(warning.ResponseBody), "response.failed")
}

func TestOpenAIStreamingClientDisconnectDrainsUpstreamUsage(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Writer = &httptestkit.FailingWriter{ResponseWriter: c.Writer, FailAfter: 0}

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{}}\n\n"))
		_, _ = pw.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":1}}}}\n\n"))
	}()

	result, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", "")
	_ = pr.Close()
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result == nil || result.Usage == nil {
		t.Fatalf("expected usage result")
	}
	if result.Usage.InputTokens != 3 || result.Usage.OutputTokens != 5 || result.Usage.CacheReadInputTokens != 1 {
		t.Fatalf("unexpected usage: %+v", *result.Usage)
	}
	if strings.Contains(rec.Body.String(), "event: error") || strings.Contains(rec.Body.String(), "write_failed") {
		t.Fatalf("expected no injected SSE error event, got %q", rec.Body.String())
	}
}

func TestOpenAIStreamingMissingTerminalEventReturnsIncompleteError(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\",\"output_index\":0}\n\n"))
	}()

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", "")
	_ = pr.Close()
	if err == nil || !strings.Contains(err.Error(), "missing terminal event") {
		t.Fatalf("expected missing terminal event error, got %v", err)
	}
}

func TestOpenAIStreamingTooLong(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: 64 * 1024}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		// A line larger than MaxLineSize triggers ErrTooLong.
		payload := "data: " + strings.Repeat("a", 128*1024) + "\n"
		_, _ = pw.Write([]byte(payload))
	}()

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 2}}, time.Now(), "model", "model", "")
	_ = pr.Close()

	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("expected ErrTooLong, got %v", err)
	}
	if !strings.Contains(rec.Body.String(), "\"type\":\"error\"") || !strings.Contains(rec.Body.String(), "response_too_large") {
		t.Fatalf("expected OpenAI-compatible error SSE event, got %q", rec.Body.String())
	}
}

func TestOpenAINonStreamingContentTypePassThrough(t *testing.T) {
	cfg := &responsesFixtureOptions{Headers: egress.ResponseHeaderOptions{Enabled: false}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	body := []byte(`{"usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/vnd.test+json"}},
	}

	_, err := svc.Output.NonStream(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation}}, "model", "model")
	if err != nil {
		t.Fatalf("handleNonStreamingResponse error: %v", err)
	}

	if !strings.Contains(rec.Header().Get("Content-Type"), "application/vnd.test+json") {
		t.Fatalf("expected Content-Type passthrough, got %q", rec.Header().Get("Content-Type"))
	}
}

func TestOpenAINonStreamingContentTypeDefault(t *testing.T) {
	cfg := &responsesFixtureOptions{Headers: egress.ResponseHeaderOptions{Enabled: false}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	body := []byte(`{"usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{},
	}

	_, err := svc.Output.NonStream(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation}}, "model", "model")
	if err != nil {
		t.Fatalf("handleNonStreamingResponse error: %v", err)
	}

	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected default Content-Type, got %q", rec.Header().Get("Content-Type"))
	}
}

func TestOpenAIStreamingHeadersOverride(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}, Headers: egress.ResponseHeaderOptions{Enabled: false}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header: http.Header{
			"Cache-Control": []string{"upstream"},
			"X-Request-Id":  []string{"req-123"},
			"Content-Type":  []string{"application/custom"},
		},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{}}\n\n"))
	}()

	_, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", "")
	_ = pr.Close()
	if err != nil {
		t.Fatalf("handleStreamingResponse error: %v", err)
	}

	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("expected Cache-Control override, got %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected Content-Type override, got %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("X-Request-Id") != "req-123" {
		t.Fatalf("expected X-Request-Id passthrough, got %q", rec.Header().Get("X-Request-Id"))
	}
}

func TestOpenAIStreamingReuseScannerBufferAndStillWorks(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{StreamDataIntervalTimeout: 0, StreamKeepaliveInterval: 0, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":3}}}}\n\n"))
	}()

	result, err := svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "model", "model", "")
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Usage)
	require.Equal(t, 1, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, 3, result.Usage.CacheReadInputTokens)
}

func TestExtractOpenAISSEDataLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantData string
		wantOK   bool
	}{
		{name: "标准格式", line: `data: {"type":"x"}`, wantData: `{"type":"x"}`, wantOK: true},
		{name: "无空格格式", line: `data:{"type":"x"}`, wantData: `{"type":"x"}`, wantOK: true},
		{name: "纯空数据", line: `data:   `, wantData: ``, wantOK: true},
		{name: "非 data 行", line: `event: message`, wantData: ``, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := protocolopenai.ExtractSSEDataLine(tt.line)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantData, got)
		})
	}
}

func TestExtractOpenAIUsageFromJSONBytes_AcceptsResponseAndChatUsageShapes(t *testing.T) {
	usage, ok := protocolopenai.ExtractOpenAIUsageFromJSONBytes([]byte(`{"id":"resp_1","usage":{"input_tokens":9,"output_tokens":5,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":4}}}`))
	require.True(t, ok)
	require.Equal(t, 9, usage.InputTokens)
	require.Equal(t, 5, usage.OutputTokens)
	require.Equal(t, 2, usage.CacheReadInputTokens)
	require.Equal(t, 4, usage.CacheCreationInputTokens)

	usage, ok = protocolopenai.ExtractOpenAIUsageFromJSONBytes([]byte(`{"type":"response.completed","response":{"usage":{"prompt_tokens":13,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":4,"cache_creation_tokens":3,"image_tokens":2},"completion_tokens_details":{"image_tokens":1}}}}`))
	require.True(t, ok)
	require.Equal(t, 13, usage.InputTokens)
	require.Equal(t, 2, usage.ImageInputTokens)
	require.Equal(t, 7, usage.OutputTokens)
	require.Equal(t, 4, usage.CacheReadInputTokens)
	require.Equal(t, 3, usage.CacheCreationInputTokens)
	require.Equal(t, 1, usage.ImageOutputTokens)

	usage, ok = protocolopenai.ExtractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":11,"output_tokens":2,"cache_write_input_tokens":6}}`))
	require.True(t, ok)
	require.Equal(t, 6, usage.CacheCreationInputTokens)

	usage, ok = protocolopenai.ExtractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":20,"output_tokens":2,"cache_creation_input_tokens":19,"input_tokens_details":{"cache_write_tokens":7}}}`))
	require.True(t, ok)
	require.Equal(t, 7, usage.CacheCreationInputTokens, "官方嵌套字段应优先于兼容顶层别名")

	usage, ok = protocolopenai.ExtractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":20,"output_tokens":2,"cache_creation_input_tokens":19,"input_tokens_details":{"cache_write_tokens":0}}}`))
	require.True(t, ok)
	require.Zero(t, usage.CacheCreationInputTokens, "官方嵌套字段显式为零时仍应优先于兼容顶层别名")

	usage, ok = protocolopenai.ExtractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":20,"output_tokens":2,"cache_read_input_tokens":19,"input_tokens_details":{"cached_tokens":0}}}`))
	require.True(t, ok)
	require.Zero(t, usage.CacheReadInputTokens, "官方嵌套缓存读取字段显式为零时仍应优先于兼容顶层别名")

	// xAI may report reasoning_tokens separately from visible output_tokens; treat them as separate only
	// when the arithmetic agrees. Standard OpenAI output tokens already include reasoning.
	usage, ok = protocolopenai.ExtractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":10000,"output_tokens":500,"total_tokens":10800,"output_tokens_details":{"reasoning_tokens":300}}}`))
	require.True(t, ok)
	require.Equal(t, 800, usage.OutputTokens)
	usage, ok = protocolopenai.ExtractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":10000,"output_tokens":500,"total_tokens":10500,"output_tokens_details":{"reasoning_tokens":300}}}`))
	require.True(t, ok)
	require.Equal(t, 500, usage.OutputTokens)
}

func TestExtractOpenAIUsageFromJSONBytes_IncludesGrokReasoningTokens(t *testing.T) {
	usage, ok := protocolopenai.ExtractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,"completion_tokens_details":{"reasoning_tokens":94}}}`))
	require.True(t, ok)
	require.Equal(t, 103, usage.OutputTokens, "Grok Chat usage bills visible completion plus reasoning tokens")

	usage, ok = protocolopenai.ExtractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":32,"output_tokens":103,"total_tokens":135,"output_tokens_details":{"reasoning_tokens":94}}}`))
	require.True(t, ok)
	require.Equal(t, 103, usage.OutputTokens, "Responses output_tokens already includes reasoning when total confirms it")

	usage, ok = protocolopenai.ExtractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"input_tokens":32,"output_tokens":9,"total_tokens":135,"output_tokens_details":{"reasoning_tokens":94}}}`))
	require.True(t, ok)
	require.Equal(t, 103, usage.OutputTokens, "Responses detail-only shape is normalized when total exposes the full output")
}

func TestExtractCodexFinalResponse_SampleReplay(t *testing.T) {
	body := strings.Join([]string{
		`event: message`,
		`data: {"type":"response.in_progress","response":{"id":"resp_1"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-4o","usage":{"input_tokens":11,"output_tokens":22,"input_tokens_details":{"cached_tokens":3}}}}`,
		`data: [DONE]`,
	}, "\n")

	finalResp, ok := protocolopenai.ExtractCodexFinalResponse(body)
	require.True(t, ok)
	require.Contains(t, string(finalResp), `"id":"resp_1"`)
	require.Contains(t, string(finalResp), `"input_tokens":11`)
}

func TestHandleNonStreamingResponse_APIKeyFallsBackToSSEBodyWhenContentTypeIsWrong(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"hel"}`,
			`data: {"type":"response.output_text.delta","delta":"lo"}`,
			`data: {"type":"response.completed","response":{"id":"resp_api_key_sse","object":"response","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
			`data: [DONE]`,
		}, "\n"))),
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Type: capability.ProviderTypeAPIKey}}

	result, err := svc.Output.NonStream(context.Background(), resp, c, provider, "gpt-5.4", "gpt-5.4")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.NotContains(t, rec.Body.String(), "data:")
	require.Equal(t, "resp_api_key_sse", gjson.Get(rec.Body.String(), "id").String())
	require.Equal(t, "hello", gjson.Get(rec.Body.String(), "output.0.content.0.text").String())
}

func TestHandleNonStreamingResponse_OAuthJSONBodyWithDataEventTextKeepsJSONUsage(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}})
	// Ordinary data:/event: text inside Compact JSON must not cause SSE detection.
	jsonBody := `{"id":"resp_oauth_compact","object":"response","model":"gpt-5.4","status":"completed",` +
		`"output":[{"type":"message","content":[{"type":"output_text",` +
		`"text":"processing data: 1,2,3 then event: click finished"}]}],` +
		`"usage":{"input_tokens":11,"output_tokens":22,"total_tokens":33}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(jsonBody)),
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 146, Type: capability.ProviderTypeOAuth}}

	result, err := svc.Output.NonStream(context.Background(), resp, c, provider, "gpt-5.4", "gpt-5.4")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 22, result.Usage.OutputTokens)
	// Return the JSON response with upstream usage.
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, "resp_oauth_compact", gjson.Get(rec.Body.String(), "id").String())
	require.Equal(t, int64(33), gjson.Get(rec.Body.String(), "usage.total_tokens").Int())
	require.Contains(t, rec.Body.String(), "processing data: 1,2,3 then event: click finished")
}

func TestOpenAICompatSSEFrameParserResetsEventTypeAtFrameBoundary(t *testing.T) {
	var parser protocolopenai.OpenAICompatSSEFrameParser

	frame, ok := parser.AddLine("event: response.created")
	require.False(t, ok)
	require.Empty(t, frame)

	frame, ok = parser.AddLine(`data: {"response":{"id":"resp_1"}}`)
	require.False(t, ok)
	require.Empty(t, frame)

	frame, ok = parser.AddLine("")
	require.True(t, ok)
	require.Equal(t, "response.created", frame.EventType)
	require.JSONEq(t, `{"response":{"id":"resp_1"}}`, frame.Data)

	frame, ok = parser.AddLine(`data: {"delta":"ok"}`)
	require.False(t, ok)
	require.Empty(t, frame.EventType)

	frame, ok = parser.AddLine("")
	require.True(t, ok)
	require.Empty(t, frame.EventType)
	require.JSONEq(t, `{"delta":"ok"}`, frame.Data)
}

func TestOpenAIGatewayServiceHandleResponsesImageOutputs_NonStreaming(t *testing.T) {
	svc := newOpenAIImageGenerationControlTestService(&auxiliaryHTTPRecorder{})
	c, _ := newOpenAIImageGenerationControlTestContext(true, "unit-test-agent/1.0")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_image_json",
			"model":"gpt-5.4",
			"output":[{"id":"ig_json_1","type":"image_generation_call","result":"final-image"}],
			"usage":{"input_tokens":7,"output_tokens":3,"output_tokens_details":{"image_tokens":2}}
		}`)),
	}

	result, err := svc.Output.NonStream(context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Type: capability.ProviderTypeAPIKey}}, "gpt-5.4", "gpt-5.4")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.NotNil(t, result.Usage)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, 2, result.Usage.ImageOutputTokens)
}

func TestOpenAIGatewayServiceHandleResponsesImageOutputs_Streaming(t *testing.T) {
	svc := newOpenAIImageGenerationControlTestService(&auxiliaryHTTPRecorder{})
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "unit-test-agent/1.0")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_stream_1\",\"type\":\"image_generation_call\",\"status\":\"generating\",\"result\":\"final-image\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_image_stream\",\"model\":\"gpt-5.5\",\"output\":[{\"id\":\"ig_stream_1\",\"type\":\"image_generation_call\",\"status\":\"generating\",\"result\":\"final-image\"}],\"usage\":{\"input_tokens\":11,\"output_tokens\":5,\"output_tokens_details\":{\"image_tokens\":4}}}}\n\n",
		)),
	}

	result, err := svc.Output.ReadStreamObservation(context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}, time.Now(), "gpt-5.5", "gpt-5.5", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount)
	require.NotNil(t, result.Usage)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Equal(t, 4, result.Usage.ImageOutputTokens)
	require.NotContains(t, recorder.Body.String(), `"status":"generating"`)
	require.Equal(t, 2, strings.Count(recorder.Body.String(), `"status":"completed"`))
}

func TestNormalizeCompletedImageGenerationStatus(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		want        string
		wantChanged bool
	}{
		{
			name:        "output item done with result",
			input:       `{"type":"response.output_item.done","item":{"type":"image_generation_call","status":"generating","result":"image-data"}}`,
			want:        `{"type":"response.output_item.done","item":{"type":"image_generation_call","status":"completed","result":"image-data"}}`,
			wantChanged: true,
		},
		{
			name:        "terminal response only changes completed image result",
			input:       `{"type":"response.completed","response":{"output":[{"type":"image_generation_call","status":"in_progress","result":"image-data"},{"type":"image_generation_call","status":"failed","result":"partial-data"}]}}`,
			want:        `{"type":"response.completed","response":{"output":[{"type":"image_generation_call","status":"completed","result":"image-data"},{"type":"image_generation_call","status":"failed","result":"partial-data"}]}}`,
			wantChanged: true,
		},
		{
			name:        "done item without result",
			input:       `{"type":"response.output_item.done","item":{"type":"image_generation_call","status":"generating"}}`,
			want:        `{"type":"response.output_item.done","item":{"type":"image_generation_call","status":"generating"}}`,
			wantChanged: false,
		},
		{
			name:        "non-final image event",
			input:       `{"type":"response.output_item.added","item":{"type":"image_generation_call","status":"generating","result":"image-data"}}`,
			want:        `{"type":"response.output_item.added","item":{"type":"image_generation_call","status":"generating","result":"image-data"}}`,
			wantChanged: false,
		},
		{
			name:        "done preserves base64 result",
			input:       `{"type":"response.done","response":{"output":[{"type":"image_generation_call","status":"generating","result":"iVBORw0KGgoAAAANSUhEUg/+=="}]}}`,
			want:        `{"type":"response.done","response":{"output":[{"type":"image_generation_call","status":"completed","result":"iVBORw0KGgoAAAANSUhEUg/+=="}]}}`,
			wantChanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := protocolopenai.NormalizeCompletedImageGenerationStatus([]byte(tt.input))

			require.Equal(t, tt.wantChanged, changed)
			require.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestOpenAIGatewayService_NativeOAuth_NamespaceNonStreamingResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	SetOpenAIResponsesNamespaceNames(c, map[string]bridge.ResponsesNamespaceName{
		"collaboration__spawn_agent": {Namespace: "collaboration", Name: "spawn_agent"},
	})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_1","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`)),
	}

	result, err := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}}).Output.NonStream(
		context.Background(), resp, c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Type: capability.ProviderTypeOAuth}}, "gpt-5.5", "gpt-5.5",
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotContains(t, rec.Body.String(), "collaboration__spawn_agent")
	require.Contains(t, rec.Body.String(), `"name":"spawn_agent"`)
	require.Contains(t, rec.Body.String(), `"namespace":"collaboration"`)
}

// TestResponsesLifecycleCompatibility covers bare terminals, legacy done events, repeated usage, and truncated
// streams.
func TestResponsesLifecycleCompatibility(t *testing.T) {
	const delta = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello!\"}\n\n"
	const completed = `{"object":"response","id":"resp_compat","model":"upstream-model","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello!","annotations":[]}]}],"usage":{"input_tokens":7,"output_tokens":9},"vendor":{"opaque":"keep"}}`
	frame := func(eventType, payload string) string {
		if eventType == "" {
			return "data: " + payload + "\n\n"
		}
		return "event: " + eventType + "\ndata: " + payload + "\n\n"
	}
	tests := []struct {
		name, body string
		types      []string
		wantError  bool
		output     int
	}{
		{
			name: "bare lifecycle followed by duplicate done",
			body: frame("response.created", `{"object":"response","id":"resp_compat","status":"in_progress","output":[]}`) +
				frame("response.in_progress", `{"object":"response","id":"resp_compat","status":"in_progress","output":[]}`) + delta +
				frame("response.completed", strings.Replace(completed, `"output_tokens":9`, `"output_tokens":3`, 1)) +
				frame("response.done", `{"type":"response.done","response":`+completed+`}`),
			types:  []string{"response.created", "response.in_progress", "response.output_text.delta", "response.completed"},
			output: 9,
		},
		{
			name:  "done only",
			body:  frame("response.done", `{"type":"response.done","response":`+completed+`}`),
			types: []string{"response.completed"}, output: 9,
		},
		{
			name:  "data only done",
			body:  delta + frame("", `{"type":"response.done","response":`+completed+`}`),
			types: []string{"response.output_text.delta", "response.completed"}, output: 9,
		},
		{
			name:  "standard completed",
			body:  delta + frame("response.completed", `{"type":"response.completed","sequence_number":10,"response":`+completed+`}`),
			types: []string{"response.output_text.delta", "response.completed"}, output: 9,
		},
		{
			name:  "failed done after output",
			body:  delta + frame("response.done", `{"type":"response.done","response":{"id":"resp_compat","status":"failed","error":{"code":"server_error","message":"upstream failed"},"usage":{"input_tokens":7,"output_tokens":9}}}`),
			types: []string{"response.output_text.delta", "response.failed"}, wantError: true, output: 9,
		},
		{
			name:  "incomplete done",
			body:  delta + frame("response.done", `{"type":"response.done","response":{"id":"resp_compat","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":7,"output_tokens":9}}}`),
			types: []string{"response.output_text.delta", "response.incomplete"}, output: 9,
		},
		{
			name:  "error without status",
			body:  delta + frame("response.done", `{"type":"response.done","response":{"id":"resp_compat","error":{"code":"server_error","message":"upstream failed"},"usage":{"input_tokens":7,"output_tokens":9}}}`),
			types: []string{"response.output_text.delta", "response.failed"}, wantError: true, output: 9,
		},
		{
			name:  "incomplete without status",
			body:  delta + frame("response.done", `{"type":"response.done","response":{"id":"resp_compat","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":7,"output_tokens":9}}}`),
			types: []string{"response.output_text.delta", "response.incomplete"}, output: 9,
		},
		{
			name: "truncated stream stays failed",
			body: delta, types: []string{"response.output_text.delta"}, wantError: true,
		},
	}
	for _, mode := range []string{"standard", "async", "passthrough"} {
		for _, tt := range tests {
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				options := &wsFixtureOptions{Output: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
				if mode == "async" {
					options.Output.StreamDataIntervalTimeout = 30
				}
				svc := newWSFixture(wsFixtureInputs{options: options, corrector: responseupstream.NewCodexToolCorrector()})
				target := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tt.body))}
				var result *responseupstream.StreamingResult
				var err error
				if mode == "passthrough" {
					result, err = responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, target), time.Now(), "client-model", "upstream-model")
				} else {
					result, err = svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, target, time.Now(), "client-model", "upstream-model", "")
				}
				if tt.wantError {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.NotNil(t, result)
				require.Equal(t, tt.output, result.Usage.OutputTokens)
				assertOpenAISSEFrames(t, recorder.Body.String(), tt.types)
				// Detect client completion from the delivered JSON type rather than the server event-line fallback.
				protocolopenai.ForEachOpenAISSEFrame(recorder.Body.String(), func(_ string, data []byte) {
					if gjson.GetBytes(data, "type").String() == "response.completed" {
						require.Equal(t, "completed", gjson.GetBytes(data, "response.status").String())
						require.Equal(t, "client-model", gjson.GetBytes(data, "response.model").String())
						require.Equal(t, "keep", gjson.GetBytes(data, "response.vendor.opaque").String())
						require.True(t, gjson.GetBytes(data, "sequence_number").Exists())
					}
				})
			})
		}
	}
}

// TestResponsesLifecycleErrorThenFailedDone checks that a preceding error does not suppress a normalized
// failed terminal.
func TestResponsesLifecycleErrorThenFailedDone(t *testing.T) {
	for _, mode := range []string{"standard", "async", "passthrough"} {
		for _, header := range []string{"", "event: response.done\n"} {
			t.Run(mode+"/"+header, func(t *testing.T) {
				body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
					"event: error\ndata: {\"type\":\"error\",\"error\":{\"code\":\"server_error\",\"message\":\"failed\"}}\n\n" +
					header + "data: {\"type\":\"response.done\",\"response\":{\"id\":\"resp_review\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"failed\"},\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				options := &wsFixtureOptions{Output: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
				if mode == "async" {
					options.Output.StreamDataIntervalTimeout = 30
				}
				svc := newWSFixture(wsFixtureInputs{options: options, corrector: responseupstream.NewCodexToolCorrector()})
				target := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
				var result *responseupstream.StreamingResult
				var err error
				if mode == "passthrough" {
					result, err = responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, target), time.Now(), "model", "model")
				} else {
					result, err = svc.Output.ReadStreamObservation(c.Request.Context(), resp, c, target, time.Now(), "model", "model", "")
				}
				require.ErrorContains(t, err, "failed")
				require.NotNil(t, result)
				require.Equal(t, 1, result.Usage.OutputTokens)
				assertOpenAISSEFrames(t, recorder.Body.String(), []string{"response.output_text.delta", "response.failed"})
			})
		}
	}
}

// compileHTTPFixtureHeaders compiles header policy; nil selects defaults.
func compileHTTPFixtureHeaders(options *responsesFixtureOptions) *egress.CompiledHeaderFilter {
	if options == nil {
		return nil
	}
	return egress.CompileHeaderFilter(options.Headers)
}

func TestOpenAIStreamingRepairsConcatenatedJSONDocumentsInSingleDataLine(t *testing.T) {
	testOpenAIStreamingRepairsConcatenatedJSONDocuments(t, false, 0)
}

func TestOpenAIStreamingAsyncScannerRepairsConcatenatedJSONDocumentsInSingleDataLine(t *testing.T) {
	testOpenAIStreamingRepairsConcatenatedJSONDocuments(t, false, 30)
}

func TestSplitOpenAIConcatenatedJSONDocumentsRejectsPayloadOverRepairLimit(t *testing.T) {
	first := `{"type":"response.in_progress","padding":"` + strings.Repeat("x", 16*1024*1024) + `"}`
	second := `{"type":"response.completed"}`
	payload := first + second

	documents, repaired := protocolopenai.SplitConcatenatedJSONDocuments([]byte(payload))
	require.False(t, repaired)
	require.Nil(t, documents)

	line := "data: " + payload
	scanner := bufio.NewScanner(strings.NewReader(line))
	scanner.Buffer(make([]byte, 1024), len(line)+1)
	documentScanner := protocolopenai.NewSSEJSONDocumentScanner(scanner)
	require.True(t, documentScanner.Scan())
	require.Equal(t, line, documentScanner.Text())
	require.False(t, documentScanner.Scan())
	require.NoError(t, documentScanner.Err())
}

func TestOpenAIVisibleOutputClassification(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		eventType string
		want      bool
	}{
		{name: "keepalive", data: `{"type":"keepalive"}`, want: false},
		{name: "created", data: `{"type":"response.created"}`, want: false},
		{name: "empty output item", data: `{"type":"response.output_item.added","item":{"id":"item_test","type":"reasoning","summary":[]}}`, want: false},
		{name: "empty delta", data: `{"type":"response.output_text.delta","delta":""}`, want: false},
		{name: "text delta", data: `{"type":"response.output_text.delta","delta":"test output"}`, want: true},
		{name: "tool arguments", data: `{"type":"response.function_call_arguments.delta","delta":"{}"}`, want: true},
		{name: "partial image", data: `{"type":"response.image_generation_call.partial_image","partial_image_b64":"dGVzdA=="}`, want: true},
		{name: "completed image item", data: `{"type":"response.output_item.done","item":{"id":"item_test","type":"image_generation_call","result":"dGVzdA=="}}`, want: true},
		{name: "empty completed", data: `{"type":"response.completed","response":{"id":"resp_test","output":[]}}`, want: false},
		{name: "completed with output usage only", data: `{"type":"response.completed","response":{"id":"resp_test","usage":{"input_tokens":1,"output_tokens":2}}}`, want: false},
		{name: "completed with text", data: `{"type":"response.completed","response":{"id":"resp_test","output":[{"type":"message","content":[{"type":"output_text","text":"test output"}]}]}}`, want: true},
		{name: "done marker", data: `[DONE]`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, protocolopenai.StreamDataStartsVisibleOutput(tt.data, tt.eventType))
		})
	}
}

func TestOpenAIResponsesTTFTStartsAtVisibleOutput(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			result := runSyntheticVisibleTTFTStream(t, passthrough, 120*time.Millisecond, 0,
				`{"type":"response.output_text.delta","delta":"test output"}`)
			require.NotNil(t, result.FirstTokenMs)
			require.GreaterOrEqual(t, *result.FirstTokenMs, 100)
		})
	}
}

func TestOpenAIResponsesTTFTStartsAtCompletedImage(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			result := runSyntheticVisibleTTFTStream(t, passthrough, 120*time.Millisecond, 0,
				`{"type":"response.output_item.done","item":{"id":"item_test","type":"image_generation_call","result":"dGVzdA=="}}`)
			require.NotNil(t, result.FirstTokenMs)
			require.GreaterOrEqual(t, *result.FirstTokenMs, 100)
		})
	}
}

func TestOpenAINativeMetadataDoesNotDisarmFirstOutputTimeout(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{readers: newHTTPReadersFixture(&gatewaytestkit.FastPolicySettingsRepo{Values: map[string]string{gateway.SettingKeyOpenAITTFTMode: gateway.OpenAITTFTModeVisible}}, nil), options: &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize, OpenAIFirstOutputTimeoutSeconds: 1}}})
	reader, writer := io.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer func() { _ = writer.Close() }()
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\"}}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"item_test\",\"type\":\"reasoning\",\"summary\":[]}}\n\n")
		time.Sleep(1200 * time.Millisecond)
	}()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Name: "provider_test", Platform: capability.PlatformOpenAI}}

	_, err := svc.Output.ReadStreamObservation(context.Background(), resp, c, provider, time.Now(), "test-model", "test-model", "")
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.Empty(t, recorder.Body.String())
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("synthetic upstream writer did not exit")
	}
}

func runSyntheticVisibleTTFTStream(t *testing.T, passthrough bool, visibleDelay time.Duration, timeoutSeconds int, visibleEvent string) *responseupstream.StreamingResult {
	t.Helper()

	svc := newResponsesFixture(responsesFixtureInputs{readers: newHTTPReadersFixture(&gatewaytestkit.FastPolicySettingsRepo{Values: map[string]string{gateway.SettingKeyOpenAITTFTMode: gateway.OpenAITTFTModeVisible}}, nil), options: &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize, OpenAIFirstOutputTimeoutSeconds: timeoutSeconds}}})
	reader, writer := io.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer func() { _ = writer.Close() }()
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\"}}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"item_test\",\"type\":\"reasoning\",\"summary\":[]}}\n\n")
		time.Sleep(visibleDelay)
		_, _ = io.WriteString(writer, "data: "+visibleEvent+"\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Name: "provider_test", Platform: capability.PlatformOpenAI}}
	started := time.Now()

	var result *responseupstream.StreamingResult
	var err error
	if passthrough {
		var passthroughResult *responseupstream.StreamingResult
		passthroughResult, err = responseupstream.ReadPassthroughStreaming(context.Background(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(context.Background(), c, provider), started, "test-model", "test-model")
		if passthroughResult != nil {
			result = &responseupstream.StreamingResult{FirstTokenMs: passthroughResult.FirstTokenMs}
		}
	} else {
		result, err = svc.Output.ReadStreamObservation(context.Background(), resp, c, provider, started, "test-model", "test-model", "")
	}
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, recorder.Body.String(), `"type":"response.output_item.added"`)
	require.Contains(t, recorder.Body.String(), visibleEvent)
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("synthetic upstream writer did not exit")
	}
	return result
}

func (s streamSelectionDiagnosticSource) GetProvider(context.Context, int64) (*gatewayprovider.ExecutionProvider, error) {
	return &s.value, nil
}

func (s streamSelectionDiagnosticSource) GetGroup(context.Context, int64) (*routing.Group, error) {
	return &s.group, nil
}

func (s streamSelectionDiagnosticSource) ListProvidersForSchedulerScoreFilter(context.Context, string, string, string, string, int64, string) ([]gatewayprovider.ExecutionProvider, error) {
	return []gatewayprovider.ExecutionProvider{s.value}, nil
}

func (s streamSelectionDiagnosticSource) ListSchedulableProvidersForAdvancedSchedulerScore(context.Context, *int64, string) ([]gatewayprovider.ExecutionProvider, error) {
	return []gatewayprovider.ExecutionProvider{s.value}, nil
}

// selectionDiagnosticForStreamTest observes the shared proxy circuit breaker through its diagnostic interface.
func selectionDiagnosticForStreamTest(t *testing.T, source *OpenAIResponsesExecutor, value *gatewayprovider.ExecutionProvider) (bool, string) {
	t.Helper()
	copy := *value
	copy.Record.Status = "active"
	copy.Record.Schedulable = true
	copy.Record.GroupIDs = []int64{1}
	projection := streamSelectionDiagnosticSource{value: copy, group: routing.Group{ID: 1, Status: "active", Hydrated: true, SchedulerType: routing.GroupSchedulerTypeAdvanced}}
	parameters := scheduler.NewParameters(scheduler.NewSettingsRuntime(scheduler.Diagnostics{}), nil, scheduler.DefaultParameters())
	choices := selectionadapter.NewCompatible(selectionadapter.CompatibleDependencies{Responses: source.Lineage.Store, RuntimeBlocks: source.Output.Health.Runtime, ModelTransient: source.Output.Health.ModelTransient, ProxyCircuit: source.Output.ProxyCircuit}, selectionadapter.DefaultOptions())
	diagnostic := selectionadapter.NewDiagnostics(projection, selectionadapter.Shared{Parameters: parameters}, nil, choices)
	result, err := diagnostic.GetDetail(context.Background(), value.Record.ID, policy.AdvancedSchedulerScoreDiagnosticRequest{GroupID: 1})
	require.NoError(t, err)
	require.NotNil(t, result.Detail)
	return result.Detail.Eligible, strings.Join(result.Detail.HardFilterReasons, ",")
}
