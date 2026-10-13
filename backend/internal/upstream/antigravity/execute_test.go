package antigravity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// One Antigravity upstream stream feeds four client representations with
// streamed and buffered output. Fixtures record body/lifecycle release and raw
// usage observations, including a failing client sink and an upstream read
// failure after partial usage payloads.

// executionSink records synchronous events and output bytes for Antigravity
// execution fixtures.
type executionSink struct {
	body strings.Builder // successfully written fixture output bytes
	// events records supplied values without cloning their data; fixtures inspect
	// retained slices after Execute completes and keep source storage stable.
	events []upstream.OutputEvent
	// fail rejects semantic output after retaining its event, allowing fixtures
	// to model a client disconnect during useful content.
	fail bool
}

// executionBody counts response-body closes before delegating cleanup to its
// wrapped reader.
type executionBody struct {
	io.ReadCloser // wrapped fixture response body reader
	// closes is the nonnil atomic counter shared with the assertion. Each Close
	// updates it before delegating to the wrapped body.
	closes *atomic.Int32
}

// failedAfterPayload replaces underlying EOF with a read failure after
// delivering available fixture bytes.
type failedAfterPayload struct{ io.Reader } // wrapped payload reader

// Begin accepts the fixture output head without recording any header state.
func (s *executionSink) Begin(upstream.OutputHead) error { return nil }

// Emit retains the supplied event and writes bytes unless semantic output is
// configured to fail. Event data stays shared; callers inspect recorded data
// after synchronous execution and keep its storage stable.
func (s *executionSink) Emit(e upstream.OutputEvent) error {
	s.events = append(s.events, e)
	if s.fail && e.Semantic {
		return io.ErrClosedPipe
	}
	_, _ = s.body.Write(e.Data)
	return nil
}

// Close increments the shared close counter before closing the wrapped body.
// The counter records every invocation, including repeated cleanup, while the
// delegated closer determines the returned error.
func (b *executionBody) Close() error { b.closes.Add(1); return b.ReadCloser.Close() }

// executionResponseOptions builds identity tool restoration and deterministic
// error/failover callbacks for execution tests. The fixture uses callback
// errors to expose adapter outcomes without mutating provider health or
// external state.
func executionResponseOptions() ResponseOptions {
	return ResponseOptions{ReverseTools: func(b []byte) []byte { return b }, ClaudeError: func(status int, kind, message string) error { return fmt.Errorf("%d %s %s", status, kind, message) }, CompatError: func(status int, kind, message string) error { return fmt.Errorf("%d %s %s", status, kind, message) }, MapCollectionError: func(err error) error { return err }, Failover: func(body []byte) error { return fmt.Errorf("failover: %s", body) }, IsFailover: func(err error) bool { return strings.HasPrefix(err.Error(), "failover:") }, MarkCommitted: func() {}}
}

// TestExecuteProtocolOutputsAndRelease runs all four client representations in
// streamed and buffered modes. Each attempt must retain output,
// request/model/usage observations, semantic timing, and one body close and
// lifecycle release.
func TestExecuteProtocolOutputsAndRelease(t *testing.T) {
	cases := []struct {
		name string              // human-readable client representation name
		mode ResponseMode        // selected output adapter mode
		wire protocol.ProtocolID // admitted client protocol identity
	}{{"claude", ModeClaudeResponse, protocol.ProtocolAnthropicMessages}, {"gemini", ModeGeminiResponse, protocol.ProtocolGeminiGenerateContent}, {"chat", ModeChatResponse, protocol.ProtocolOpenAIChatCompletions}, {"responses", ModeResponsesResponse, protocol.ProtocolOpenAIResponses}}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("x-request-id", "fixture-request")
					_, _ = io.WriteString(w, "data: {\"response\":{\"modelVersion\":\"gemini-runtime\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":1,\"totalTokenCount\":4}}}\n\n")
				}))
				defer server.Close()
				var closes, releases atomic.Int32
				sink := &executionSink{}
				target := &Target{ProviderID: 12, Model: "gemini-fixture", Mode: tc.mode, IncludeUsage: true, Response: executionResponseOptions(), Enter: func() (func(), error) { return func() { releases.Add(1) }, nil }, Exchange: func(ctx context.Context) (*http.Response, error) {
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
					if err != nil {
						return nil, err
					}
					resp, err := server.Client().Do(req)
					if resp != nil {
						resp.Body = &executionBody{ReadCloser: resp.Body, closes: &closes}
					}
					return resp, err
				}}
				result, err := (Executor{}).Execute(context.Background(), upstream.AttemptInput{Protocol: tc.wire, ResponseModel: "public-model", Stream: stream, Target: target}, sink)
				require.NoError(t, err)
				require.Contains(t, sink.body.String(), "hello")
				require.Equal(t, "fixture-request", result.RequestID)
				require.Equal(t, "gemini-runtime", result.UpstreamResponseModel)
				require.Equal(t, 3, result.Usage.InputTokens)
				require.Equal(t, 1, result.Usage.OutputTokens)
				require.True(t, result.HasUsage)
				require.True(t, result.Served)
				require.NotNil(t, result.FirstSemanticOutput)
				require.EqualValues(t, 1, closes.Load())
				require.EqualValues(t, 1, releases.Load())
				{
					var semantic bool
					for _, event := range sink.events {
						semantic = semantic || event.Semantic
					}
					require.True(t, semantic, "实际输出必须报告语义内容")
				}
			})
		}
	}
}

// TestExecuteDisconnectedStreamStillObservesTailUsage fails the sink when
// semantic stream output arrives before a later usage event. Execution must
// report client disconnect while retaining tail token observations and
// releasing the owned body and lifecycle entry through their close counters.
func TestExecuteDisconnectedStreamStillObservesTailUsage(t *testing.T) {
	body := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]}}]}}\n\ndata: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":9,\"candidatesTokenCount\":4}}}\n\n"
	var closes, releases atomic.Int32
	target := &Target{Mode: ModeClaudeResponse, Response: executionResponseOptions(), Enter: func() (func(), error) { return func() { releases.Add(1) }, nil }, Exchange: func(context.Context) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: &executionBody{ReadCloser: io.NopCloser(strings.NewReader(body)), closes: &closes}}, nil
	}}
	result, err := (Executor{}).Execute(context.Background(), upstream.AttemptInput{Protocol: protocol.ProtocolAnthropicMessages, ResponseModel: "fixture", Stream: true, Target: target}, &executionSink{fail: true})
	require.NoError(t, err)
	require.True(t, result.ClientDisconnect)
	require.True(t, result.HasUsage)
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.EqualValues(t, 1, closes.Load())
	require.EqualValues(t, 1, releases.Load())
}

// TestExecuteFailedBeforeResponseReleasesOwnedBody returns a policy failure
// before response adaptation. Execution must retain unserved/no-usage status
// and close the owned body and lifecycle registration once.
func TestExecuteFailedBeforeResponseReleasesOwnedBody(t *testing.T) {
	var closes, releases atomic.Int32
	want := errors.New("policy rejected")
	target := &Target{Response: executionResponseOptions(), Enter: func() (func(), error) { return func() { releases.Add(1) }, nil }, Exchange: func(context.Context) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: &executionBody{ReadCloser: io.NopCloser(strings.NewReader("denied")), closes: &closes}}, nil
	}, BeforeResponse: func(context.Context, *http.Response) (bool, error) { return true, want }}
	result, err := (Executor{}).Execute(context.Background(), upstream.AttemptInput{Protocol: protocol.ProtocolAnthropicMessages, Target: target}, &executionSink{})
	require.ErrorIs(t, err, want)
	require.False(t, result.Served)
	require.False(t, result.HasUsage)
	require.EqualValues(t, 1, closes.Load())
	require.EqualValues(t, 1, releases.Load())
}

// Read delegates payload delivery and replaces EOF with io.ErrUnexpectedEOF.
// Available bytes and other errors pass through, allowing the executor fixture
// to observe usage before an upstream stream failure.
func (r failedAfterPayload) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

// TestExecutePartialUsageSurvivesReadFailure splits input/output usage across
// payloads before an unexpected EOF. The failed attempt must retain both
// observed token counts and report served content and usage.
func TestExecutePartialUsageSurvivesReadFailure(t *testing.T) {
	body := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]}}],\"usageMetadata\":{\"promptTokenCount\":9}}}\n\ndata: {\"response\":{\"usageMetadata\":{\"candidatesTokenCount\":4}}}\n\n"
	target := &Target{Mode: ModeClaudeResponse, Response: executionResponseOptions(), Exchange: func(context.Context) (*http.Response, error) {
		// Replace only EOF after fixture payload delivery so the failure follows
		// separately observed usage fields rather than preventing their initial
		// reads.
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(failedAfterPayload{Reader: strings.NewReader(body)})}, nil
	}}
	result, err := (Executor{}).Execute(context.Background(), upstream.AttemptInput{Protocol: protocol.ProtocolAnthropicMessages, Stream: true, ResponseModel: "fixture", Target: target}, &executionSink{})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.True(t, result.Served)
	require.True(t, result.HasUsage)
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
}
