package qoder

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/requestcontext"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// contextTestClient inspects cancellation after inference starts.
type contextTestClient struct{ check func(context.Context) error }

type modelTestClient struct{}

type modelTestSink struct{ bytes.Buffer }

func (c contextTestClient) StreamRequestContext(ctx context.Context, _ *SessionContext, _ string, _ []byte, _ map[string]string) (*http.Response, error) {
	return nil, c.check(ctx)
}

// TestStreamExecutionInternalAbort checks internal abort propagation after client disconnect for all three protocols.
func TestStreamExecutionInternalAbort(t *testing.T) {
	for _, wire := range []protocol.ProtocolID{protocol.ProtocolOpenAIChatCompletions, protocol.ProtocolOpenAIResponses, protocol.ProtocolAnthropicMessages} {
		t.Run(string(wire), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client, cancelClient := context.WithCancel(context.Background())
				request, abort := requestcontext.WithAbort(client)
				defer abort()
				called := false
				target := &Target{
					ProviderID: 1,
					Session:    func(context.Context) (*SessionContext, error) { return &SessionContext{}, nil },
					Client: func() (StreamClient, error) {
						return contextTestClient{check: func(ctx context.Context) error {
							called = true
							cancelClient()
							synctest.Wait()
							require.NoError(t, ctx.Err())
							abort()
							synctest.Wait()
							require.ErrorIs(t, ctx.Err(), context.Canceled)
							return ctx.Err()
						}}, nil
					},
				}
				_, err := NewExecutor(ExecuteOptions{}).Execute(request, upstream.AttemptInput{
					Target: target, Protocol: wire, Stream: true,
					Body: []byte(`{"model":"auto","input":"hi","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`),
				}, &modelTestSink{})
				require.True(t, called)
				require.ErrorIs(t, err, context.Canceled)
			})
		})
	}
}

func (modelTestClient) StreamRequestContext(context.Context, *SessionContext, string, []byte, map[string]string) (*http.Response, error) {
	body, _ := json.Marshal(QoderSSEWrapper{Body: `{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`})
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n\ndata: [DONE]\n\n"))}, nil
}

func (*modelTestSink) Begin(upstream.OutputHead) error { return nil }

func (s *modelTestSink) Emit(e upstream.OutputEvent) error { _, err := s.Write(e.Data); return err }

// TestQoderDoesNotReportSynthesizedModel keeps client response aliases separate from upstream model declarations.
func TestQoderDoesNotReportSynthesizedModel(t *testing.T) {
	for _, wire := range []protocol.ProtocolID{protocol.ProtocolOpenAIResponses, protocol.ProtocolOpenAIChatCompletions, protocol.ProtocolAnthropicMessages} {
		for _, stream := range []bool{false, true} {
			t.Run(string(wire)+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
				executor := NewExecutor(ExecuteOptions{})
				target := &Target{ProviderID: 1, Session: func(context.Context) (*SessionContext, error) { return &SessionContext{}, nil }, Client: func() (StreamClient, error) { return modelTestClient{}, nil }}
				sink := &modelTestSink{}
				result, err := executor.Execute(context.Background(), upstream.AttemptInput{Target: target, Protocol: wire, Body: []byte(`{"model":"auto","input":"hi","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`), ResponseModel: "client-alias", Stream: stream}, sink)
				require.NoError(t, err)
				require.Contains(t, sink.String(), "client-alias")
				require.Empty(t, result.UpstreamResponseModel)
			})
		}
	}
}
