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

type executionSink struct {
	body   strings.Builder
	events []upstream.OutputEvent
	fail   bool
}

type executionBody struct {
	io.ReadCloser
	closes *atomic.Int32
}

// failedAfterPayload simulates a connection failure after the payload is read.
type failedAfterPayload struct{ io.Reader }

func (s *executionSink) Begin(upstream.OutputHead) error { return nil }
func (s *executionSink) Emit(e upstream.OutputEvent) error {
	s.events = append(s.events, e)
	if s.fail && e.Semantic {
		return io.ErrClosedPipe
	}
	_, _ = s.body.Write(e.Data)
	return nil
}

func (b *executionBody) Close() error { b.closes.Add(1); return b.ReadCloser.Close() }
func executionResponseOptions() ResponseOptions {
	return ResponseOptions{ReverseTools: func(b []byte) []byte { return b }, ClaudeError: func(status int, kind, message string) error { return fmt.Errorf("%d %s %s", status, kind, message) }, CompatError: func(status int, kind, message string) error { return fmt.Errorf("%d %s %s", status, kind, message) }, MapCollectionError: func(err error) error { return err }, Failover: func(body []byte) error { return fmt.Errorf("failover: %s", body) }, IsFailover: func(err error) bool { return strings.HasPrefix(err.Error(), "failover:") }, MarkCommitted: func() {}}
}

func TestExecuteProtocolOutputsAndRelease(t *testing.T) {
	cases := []struct {
		name string
		mode ResponseMode
		wire protocol.ProtocolID
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

func (r failedAfterPayload) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func TestExecutePartialUsageSurvivesReadFailure(t *testing.T) {
	body := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]}}],\"usageMetadata\":{\"promptTokenCount\":9}}}\n\ndata: {\"response\":{\"usageMetadata\":{\"candidatesTokenCount\":4}}}\n\n"
	target := &Target{Mode: ModeClaudeResponse, Response: executionResponseOptions(), Exchange: func(context.Context) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(failedAfterPayload{Reader: strings.NewReader(body)})}, nil
	}}
	result, err := (Executor{}).Execute(context.Background(), upstream.AttemptInput{Protocol: protocol.ProtocolAnthropicMessages, Stream: true, ResponseModel: "fixture", Target: target}, &executionSink{})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.True(t, result.Served)
	require.True(t, result.HasUsage)
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
}
