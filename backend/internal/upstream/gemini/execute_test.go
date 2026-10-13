package gemini

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// executeSink records synchronous test output and can block or fail its first
// payload.
type executeSink struct {
	// body and events are written by synchronous Emit calls. Tests inspect both
	// after Execute returns or the done channel joins its goroutine.
	body   bytes.Buffer           // captured successful output bytes
	events []upstream.OutputEvent // copied successful sink events
	// first, resume, and once coordinate the first data emission with the
	// assertion goroutine. The test closes resume after confirming Execute is
	// blocked by synchronous sink backpressure.
	first  chan struct{} // first payload arrival notification
	resume chan struct{} // first payload continuation gate
	once   atomic.Bool   // first arrival notification guard
	fail   bool          // forced payload failure flag
}

// Begin accepts response metadata while leaving this fixture focused on
// payload output.
func (s *executeSink) Begin(upstream.OutputHead) error { return nil }

// Emit optionally blocks the first nonempty payload, then records copied event
// data and body bytes. fail returns the fixture disconnect error after the
// gate; tests join Execute before reading accumulated output.
//
// The atomic guard emits one arrival notification even if the adapter produces
// several frames. Waiting happens inside synchronous sink delivery so the test
// can prove Execute has not completed before output is released. Retained data
// is cloned because converters can reuse buffers after a successful write.
func (s *executeSink) Emit(event upstream.OutputEvent) error {
	// Signal the first payload before waiting on its release gate so the
	// assertion can observe an active synchronous write and verify Execute
	// remains blocked.
	if len(event.Data) > 0 && s.first != nil && s.once.CompareAndSwap(false, true) {
		close(s.first)
		<-s.resume
	}
	if s.fail {
		return errors.New("downstream closed")
	}
	event.Data = bytes.Clone(event.Data)
	s.events = append(s.events, event)
	_, _ = s.body.Write(event.Data)
	return nil
}

// localTarget builds a one-attempt local target with conversion callbacks and
// response-close counting. mode selects the client adapter; the stream
// parameter is retained in the fixture signature and does not alter this
// target construction.
func localTarget(server *httptest.Server, mode ResponseMode, stream bool, closes *atomic.Int32) *Target {
	return &Target{ProviderID: 41, Model: "gemini-fixture", Mode: mode, Exchange: ExchangeOptions{MaxRetries: 1, RequestIDHeader: "x-request-id", Build: func(ctx context.Context) (*http.Request, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(`{"contents":[]}`))
		return req, "x-request-id", err
	}, Do: func(req *http.Request) (*http.Response, error) {
		res, err := server.Client().Do(req)
		if res != nil {
			res.Body = &executeBody{ReadCloser: res.Body, closes: closes}
		}
		return res, err
	}, CheckPolicy: func(_ context.Context, res *http.Response) (bool, *http.Response) { return false, res }, ShouldRetry: func(int) bool { return false }, ReadError: func(res *http.Response) []byte { body, _ := io.ReadAll(res.Body); return body }, Observe: func(ExchangeNotice) {}, SetError: func(int, string, string) {}, Sanitize: func(s string) string { return s }, Message: func(b []byte) string { return string(b) }, Detail: func([]byte) string { return "" }, BuildError: func(err error) error { return err }, FinalError: errors.New}, Response: ResponseOptions{ReadBody: io.ReadAll, WriteHeaders: func(dst, src http.Header) {
		for key, values := range src {
			dst[key] = append([]string(nil), values...)
		}
	}, ObserveImages: func([]byte) {}, ReverseTools: func(b []byte) []byte { return b }, ClaudeError: func(_ int, _ string, m string) error { return errors.New(m) }, ChatError: func(_ int, _ string, m string) error { return errors.New(m) }, GoogleError: func(_ int, m string) error { return errors.New(m) }, CompatError: func(_ OpenAICompatProtocol, _ int, _ string, m string) error { return errors.New(m) }}}
}

// nativeInput builds the client protocol and stream flag matching the target
// test mode. It selects Responses separately within OpenAI mode and supplies
// the fixed response model and generation body.
func nativeInput(target *Target, stream bool) upstream.AttemptInput {
	id := protocol.ProtocolAnthropicMessages
	if target.Mode == NativeResponse {
		id = protocol.ProtocolGeminiGenerateContent
	}
	if target.Mode == OpenAIResponse {
		id = protocol.ProtocolOpenAIChatCompletions
		if target.OpenAIProtocol == OpenAICompatResponses {
			id = protocol.ProtocolOpenAIResponses
		}
	}
	return upstream.AttemptInput{Target: target, Protocol: id, Stream: stream, ResponseModel: "gemini-fixture", Body: []byte(`{"contents":[]}`)}
}

// TestExecuteGeminiNonStreamObservedZero returns visible content with explicit
// zero usage across all four client representations. It checks usage presence,
// semantic output, observed model, request ID, lifecycle/body release, and
// retained native payload bytes.
func TestExecuteGeminiNonStreamObservedZero(t *testing.T) {
	for _, kind := range []string{"messages", "native", "chat", "responses"} {
		t.Run(kind, func(t *testing.T) {
			payload := `{"modelVersion":"gemini-runtime","candidates":[{"content":{"parts":[{"text":"visible"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0},"future":{"preserve":true}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("x-request-id", "gemini-request")
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()
			var closes, releases atomic.Int32
			mode := MessagesResponse
			switch kind {
			case "native":
				mode = NativeResponse
			case "chat", "responses":
				mode = OpenAIResponse
			}
			target := localTarget(server, mode, false, &closes)
			if kind == "responses" {
				target.OpenAIProtocol = OpenAICompatResponses
			}
			target.Enter = func() (func(), error) { return func() { releases.Add(1) }, nil }
			sink := &executeSink{}
			result, err := (Executor{}).Execute(context.Background(), nativeInput(target, false), sink)
			require.NoError(t, err)
			require.True(t, result.HasUsage)
			require.True(t, result.Served)
			require.NotNil(t, result.FirstSemanticOutput)
			require.Nil(t, result.FirstTokenMs)
			require.Zero(t, result.Usage.InputTokens)
			require.Equal(t, "gemini-request", result.RequestID)
			require.Equal(t, "gemini-runtime", result.UpstreamResponseModel)
			require.Contains(t, sink.body.String(), "visible")
			require.EqualValues(t, 1, closes.Load())
			require.EqualValues(t, 1, releases.Load())
			require.True(t, sink.events[len(sink.events)-1].Semantic)
			if kind == "native" {
				require.Equal(t, payload, sink.body.String())
			}
		})
	}
}

// TestExecuteGeminiStreamingProgressAndPartial blocks the first sink emission
// while a complete or truncated upstream stream is available. It checks
// synchronous backpressure, progressive output, retained observed usage/model
// on read failure, and final response closing.
func TestExecuteGeminiStreamingProgressAndPartial(t *testing.T) {
	for _, variant := range []int{0, 1, 2, 3} {
		mode := ResponseMode(variant)
		if variant == 3 {
			mode = OpenAIResponse
		}
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode=%d/partial=%v", variant, partial), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if partial {
						w.Header().Set("Content-Length", "10000")
					}
					_, _ = io.WriteString(w, "data: {\"usageMetadata\":{\"promptTokenCount\":0}}\n\n")
					_ = http.NewResponseController(w).Flush()
					_, _ = io.WriteString(w, "data: {\"modelVersion\":\"gemini-runtime\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"visible\"}]}}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1}}\n\n")
					if !partial {
						_, _ = io.WriteString(w, "data: {\"modelVersion\":\"gemini-runtime\",\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":3}}\n\ndata: [DONE]\n\n")
					}
				}))
				defer server.Close()
				var closes atomic.Int32
				target := localTarget(server, mode, true, &closes)
				if variant == 3 {
					target.OpenAIProtocol = OpenAICompatResponses
				}
				sink := &executeSink{first: make(chan struct{}), resume: make(chan struct{})}
				done := make(chan struct{})
				var result upstream.AttemptResult
				var runErr error
				// Run Execute separately while the sink blocks its first payload; result
				// and error are read after done closes to join the worker.
				go func() {
					result, runErr = (Executor{}).Execute(context.Background(), nativeInput(target, true), sink)
					close(done)
				}()
				select {
				case <-sink.first:
				case <-time.After(3 * time.Second):
					t.Fatal("未取得渐进输出")
				}
				select {
				case <-done:
					t.Fatal("同步背压未生效")
				default:
				}
				close(sink.resume)
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("流未结束")
				}
				if partial {
					require.Error(t, runErr)
					require.Equal(t, "gemini-runtime", result.UpstreamResponseModel)
					require.Equal(t, 1, result.Usage.OutputTokens)
				} else {
					require.NoError(t, runErr)
					require.Equal(t, "gemini-runtime", result.UpstreamResponseModel)
					require.Equal(t, 3, result.Usage.OutputTokens)
				}
				require.True(t, result.HasUsage)
				require.True(t, result.Served)
				require.NotNil(t, result.FirstTokenMs)
				require.NotNil(t, result.FirstSemanticOutput)
				require.Contains(t, sink.body.String(), "visible")
				require.EqualValues(t, 1, closes.Load())
				semantic := false
				for _, event := range sink.events {
					semantic = semantic || event.Semantic
					if strings.Contains(string(event.Data), "promptTokenCount\":0") {
						require.False(t, event.Semantic)
					}
				}
				require.True(t, semantic)
			})
		}
	}
}

// TestExecuteGeminiCountFallbackIsNotUsage exhausts a retryable native request
// with a configured token estimate. It checks the estimate is emitted
// separately while server-usage presence, semantic output, and billed counters
// remain unset.
func TestExecuteGeminiCountFallbackIsNotUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	var closes atomic.Int32
	target := localTarget(server, NativeResponse, false, &closes)
	target.Exchange.ShouldRetry = func(int) bool { return true }
	target.Exchange.CountFallback = true
	target.Exchange.EstimateCount = func() int { return 17 }
	sink := &executeSink{}
	result, err := (Executor{}).Execute(context.Background(), nativeInput(target, false), sink)
	require.NoError(t, err)
	require.NotNil(t, result.EstimatedTokenCount)
	require.Equal(t, 17, *result.EstimatedTokenCount)
	require.False(t, result.HasUsage)
	require.False(t, result.Served)
	require.Zero(t, result.Usage.InputTokens)
	require.Contains(t, sink.body.String(), `"totalTokens":17`)
	require.EqualValues(t, 1, closes.Load())
}

// TestExecuteGeminiCancelledRequestDoesNotReachServer starts with a cancelled
// context and one allowed attempt. It checks no server request occurs,
// cancellation is reported, and the registered lifecycle entry is released.
func TestExecuteGeminiCancelledRequestDoesNotReachServer(t *testing.T) {
	var calls, closes, releases atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	target := localTarget(server, NativeResponse, false, &closes)
	target.Enter = func() (func(), error) { return func() { releases.Add(1) }, nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := (Executor{}).Execute(ctx, nativeInput(target, false), &executeSink{})
	require.Error(t, err)
	require.True(t, result.Cancelled)
	require.Zero(t, calls.Load())
	require.EqualValues(t, 1, releases.Load())
}
