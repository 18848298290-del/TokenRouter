package anthropic

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

// Messages attempts can observe zero-token usage before semantic content, and
// a slow synchronous sink can hold execution inside its first output call.
// The local server fixtures exercise standard and passthrough responses while
// retaining those observations separately from payload delivery and response
// cleanup.
//
// The sink closes entered before waiting on resume, letting the test confirm
// that Execute remains active while output is blocked. A done signal orders
// result and event inspection after that wait is released and execution
// returns. Close counters verify selected-response cleanup, while the
// cancellation fixture uses a separate handler-entry signal to interrupt an
// active nonstream request and check its activity release.

// executionSink records owned payload copies and can block first output or
// simulate a client write failure for attempt tests.
type executionSink struct {
	// body and events collect successful synchronous Emit calls after the
	// optional wait. Tests read them after execution completes, and retained
	// event payloads are copied before the caller can reuse its bytes.
	body   bytes.Buffer           // collected successful output bytes
	events []upstream.OutputEvent // owned emitted event records
	// entered and resume coordinate the first nonempty Emit call. entered
	// closes before waiting on resume, allowing the test to inspect an attempt
	// while output is blocked.
	entered chan struct{} // first payload arrival signal
	resume  chan struct{} // first payload release signal
	// once claims the first nonempty payload wait when entered is configured.
	// Later events bypass that coordination gate and use the same synchronous
	// output recording path.
	once atomic.Bool
	fail bool // configured output failure switch
}

// countedResponse wraps an HTTP body with a close-invocation counter used by
// attempt cleanup assertions.
type countedResponse struct {
	io.ReadCloser // wrapped upstream response reader
	// closed points to the test's shared atomic counter. Close increments it on
	// every invocation before delegating, allowing assertions to count cleanup
	// across the execution goroutine.
	closed *atomic.Int32
}

// Begin accepts response metadata without adding data to the test buffer.
func (s *executionSink) Begin(upstream.OutputHead) error { return nil }

// Emit records a synchronous output event or returns the configured disconnect
// error. The first nonempty payload can signal entered and wait on resume,
// allowing the test to inspect execution while client output remains blocked.
//
// The atomic gate prevents flush or later payload events from starting another
// wait. Recorded data is cloned because OutputSink callers can reuse their byte
// slices after return. Successful writes append both the owned event and
// payload bytes; configured failure occurs before either is retained. Tests
// wait for execution completion before reading the mutable collected output.
func (s *executionSink) Emit(event upstream.OutputEvent) error {
	// Signal the first payload before blocking Emit so the test can observe
	// progress while Execute is still inside synchronous client output. resume
	// releases that single wait.
	if len(event.Data) > 0 && s.entered != nil && s.once.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.resume
	}
	if s.fail {
		return errors.New("client disconnected")
	}
	// OutputSink data is borrowed through the call. Copy it before retention so
	// later caller buffer reuse cannot rewrite the events inspected after
	// attempt completion.
	event.Data = bytes.Clone(event.Data)
	s.events = append(s.events, event)
	_, _ = s.body.Write(event.Data)
	return nil
}

// Close increments the fixture's response-close counter before closing its
// embedded reader. Each invocation is counted, and the reader's close error is
// returned; tests use the counter to detect repeated response cleanup.
func (r *countedResponse) Close() error { r.closed.Add(1); return r.ReadCloser.Close() }

// executionTarget binds one local server exchange to the selected stream and
// passthrough settings. Successful response handles receive a counted body
// wrapper. Request contexts remain usable after the no-op release, and tests
// provide the server and close counter for the attempt's lifetime.
func executionTarget(server *httptest.Server, stream, passthrough bool, closed *atomic.Int32) *Target {
	return &Target{ProviderID: 7, Model: "claude-test", Passthrough: passthrough, Exchange: ExchangeOptions{
		Stream: stream, MaxAttempts: 1, MaxElapsed: time.Second,
		Context: func(ctx context.Context, _ bool) (context.Context, context.CancelFunc) { return ctx, func() {} },
		Build: func(ctx context.Context, body []byte) (*http.Request, []byte, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(body))
			return req, body, err
		},
		Do: func(req *http.Request) (*http.Response, error) {
			res, err := server.Client().Do(req)
			if res != nil {
				res.Body = &countedResponse{ReadCloser: res.Body, closed: closed}
			}
			return res, err
		},
		TransportError: func(_ context.Context, err error, _ string) error { return err },
	}, Response: ResponseOptions{ReadBody: io.ReadAll, InvalidJSON: func(_ context.Context, _ *http.Response, _ []byte, err error) error { return err }}}
}

// executeInput creates a Messages attempt containing the fixed fixture request.
func executeInput(target *Target, stream bool) upstream.AttemptInput {
	return upstream.AttemptInput{Target: target, Protocol: protocol.ProtocolAnthropicMessages, ResponseModel: "claude-test", Body: []byte(`{"model":"claude-test"}`), Stream: stream}
}

// TestExecuteNonStreamObservedZero supplies semantic buffered responses with
// explicit zero usage in both modes. It checks usage presence, model identity,
// absent text TTFT, unchanged payload bytes, sink classification, and one body
// close.
func TestExecuteNonStreamObservedZero(t *testing.T) {
	for _, pass := range []bool{false, true} {
		t.Run(fmt.Sprint(pass), func(t *testing.T) {
			payload := `{"id":"msg","type":"message","model":"claude-runtime","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":0,"output_tokens":0},"future":{"keep":true}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("x-request-id", "local-request")
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()
			var closed atomic.Int32
			sink := &executionSink{}
			result, err := (Executor{}).Execute(context.Background(), executeInput(executionTarget(server, false, pass, &closed), false), sink)
			require.NoError(t, err)
			require.True(t, result.HasUsage)
			require.True(t, result.Served)
			require.True(t, result.Served)
			require.Nil(t, result.Timings.TTFTMs())
			require.Equal(t, "local-request", result.RequestID)
			require.Equal(t, "claude-runtime", result.UpstreamResponseModel)
			require.Equal(t, payload, sink.body.String())
			require.EqualValues(t, 1, closed.Load())
			require.True(t, sink.events[0].Semantic)
		})
	}
}

// TestExecuteStreamingProgressAndPartialFailure blocks the first sink payload
// in both response modes. It checks progressive synchronous delivery, retained
// usage and semantic observations after partial errors, and one close after
// execution is released.
func TestExecuteStreamingProgressAndPartialFailure(t *testing.T) {
	for _, pass := range []bool{false, true} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("pass=%v/partial=%v", pass, partial), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-runtime\",\"usage\":{\"input_tokens\":0}}}\n\n")
					_ = http.NewResponseController(w).Flush()
					_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"visible\"}}\n\n")
					if partial {
						_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"failed\"}}\n\n")
					} else {
						_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					}
				}))
				defer server.Close()
				var closed atomic.Int32
				sink := &executionSink{entered: make(chan struct{}), resume: make(chan struct{})}
				// Execute runs separately while the sink blocks its first
				// payload. Closing done publishes its result after return, and
				// the test checks entered before releasing the simulated slow
				// client.
				done := make(chan struct{})
				var result upstream.AttemptResult
				var err error
				go func() {
					result, err = (Executor{}).Execute(context.Background(), executeInput(executionTarget(server, true, pass, &closed), true), sink)
					close(done)
				}()
				// Bound first-output arrival so a stalled exchange fails the
				// fixture instead of leaving its sink coordination waiting
				// indefinitely.
				select {
				case <-sink.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("首段未渐进到达")
				}
				select {
				case <-done:
					t.Fatal("慢客户端尚未释放时执行已返回")
				default:
				}
				// The first payload has arrived while Execute is still blocked.
				// Release the sink, then wait for done before reading collected
				// bytes, observations, and response-close counters.
				close(sink.resume)
				// Bound completion after releasing output so missing teardown
				// fails visibly instead of hanging the local test process.
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("执行未收尾")
				}
				if partial && !pass {
					require.Error(t, err)
				} else if !partial {
					require.NoError(t, err)
				}
				require.Equal(t, "claude-runtime", result.UpstreamResponseModel)
				// Partial error cases are checked alongside normal completion
				// after the first sink wait is released, so previously observed
				// usage and semantic content stay available to these result
				// assertions.
				require.True(t, result.HasUsage)
				require.True(t, result.Served)
				require.Nil(t, result.Timings.TTFTMs())
				require.True(t, result.Served)
				require.Contains(t, sink.body.String(), "visible")
				require.EqualValues(t, 1, closed.Load())
				sawSemantic := false
				for _, event := range sink.events {
					if event.Semantic {
						sawSemantic = true
					}
					if strings.Contains(string(event.Data), "message_start") {
						require.False(t, event.Semantic)
					}
				}
				require.True(t, sawSemantic)
			})
		}
	}
}

// TestExecuteNonStreamCancellationClosesAttempt cancels a request waiting at
// the local upstream before response delivery. It checks the cancellation error
// and result flag, then verifies the activity entry's release runs once.
func TestExecuteNonStreamCancellationClosesAttempt(t *testing.T) {
	// The upstream entry signal separates transport arrival from cancellation.
	// stopServer is a cleanup escape for the blocked handler and closes before
	// server shutdown waits for it.
	entered := make(chan struct{})
	stopServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		select {
		case <-r.Context().Done():
		case <-stopServer:
		}
	}))
	defer server.Close()
	defer close(stopServer)
	var closed, release atomic.Int32
	target := executionTarget(server, false, false, &closed)
	target.Enter = func() (func(), error) { return func() { release.Add(1) }, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The execution goroutine publishes its cancellation flag before sending
	// the error. Receiving done orders the assertion reads after that result
	// and the deferred activity release.
	done := make(chan error, 1)
	var cancelled bool
	go func() {
		result, err := (Executor{}).Execute(ctx, executeInput(target, false), &executionSink{})
		cancelled = result.Cancelled
		done <- err
	}()
	// Wait for transport arrival before cancellation; the timeout bounds
	// fixture failure when the local request never reaches its handler.
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("请求未进入本地上游")
	}
	// Cancel after the handler confirms the request reached the upstream,
	// testing interruption of an active nonstream exchange rather than
	// rejection of an already-cancelled input.
	cancel()
	// Bound cancellation completion so an exchange that ignores interruption
	// cannot leave the test waiting indefinitely for its activity release.
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		require.True(t, cancelled)
	case <-time.After(3 * time.Second):
		t.Fatal("取消未返回")
	}
	require.EqualValues(t, 1, release.Load())
}
