package grok

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Realtime registration spans a successful handshake and relay, while failed
// handshakes must release it immediately. A local TLS WebSocket and
// channel-backed client exercise JSON text forwarding and audio observation,
// then concurrent session closes inspect one connection close and one activity
// release.

// testWSFrames adapts a local WebSocket and counts its Close invocations.
type testWSFrames struct {
	// conn and closed let session tests observe the underlying connection's
	// cleanup. The adapter increments closed on every Close call, while
	// RealtimeSession supplies the idempotent ownership rule.
	conn   *websocket.Conn // local WebSocket connection
	closed *atomic.Int64   // connection-close invocation counter
}

// testDownFrames supplies cancellable channel-backed client frames for relay
// assertions.
type testDownFrames struct {
	// input and output exchange shared payload slices with the relay. The owning
	// test controls channel lifetime and retains payload bytes unchanged until its
	// corresponding receive or assertion.
	input  chan []byte // incoming client payloads
	output chan []byte // recorded upstream payloads
}

// ReadFrame reads one complete message from the fixture WebSocket using ctx. It
// converts the library message kind to FrameKind and returns the same bytes and
// error so relay assertions observe the concrete transport result.
func (f testWSFrames) ReadFrame(ctx context.Context) (upstream.FrameKind, []byte, error) {
	kind, data, err := f.conn.Read(ctx)
	return upstream.FrameKind(kind), data, err
}

// WriteFrame sends data through the fixture WebSocket using the supplied frame
// kind and ctx. It converts FrameKind to the library message type and returns
// the transport error for the relay's terminal-result selection.
func (f testWSFrames) WriteFrame(ctx context.Context, kind upstream.FrameKind, data []byte) error {
	return f.conn.Write(ctx, websocket.MessageType(kind), data)
}

// Close counts this invocation and calls CloseNow on the fixture WebSocket.
// Repeated calls each increment the counter, so session tests can detect
// duplicate cleanup attempts even when the underlying library already closed the
// connection.
func (f testWSFrames) Close() error { f.closed.Add(1); return f.conn.CloseNow() }

// ReadFrame waits for an input payload or context cancellation and labels
// payloads binary. Ready input competes with cancellation in select, and
// returned bytes remain shared with the channel sender for test-owned lifetime.
func (f testDownFrames) ReadFrame(ctx context.Context) (upstream.FrameKind, []byte, error) {
	select {
	case data := <-f.input:
		return upstream.FrameBinary, data, nil
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

// WriteFrame records data through output or returns context cancellation,
// ignoring the supplied kind. The recorded payload shares its byte storage with
// the caller, and channel readiness can win a concurrent cancellation selection.
func (f testDownFrames) WriteFrame(ctx context.Context, _ upstream.FrameKind, data []byte) error {
	select {
	case f.output <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close returns nil and leaves channel ownership with the test.
func (f testDownFrames) Close() error { return nil }

// TestRealtimeNativeLocalWebSocket exchanges binary client JSON and an upstream
// audio delta through a local TLS WebSocket. It asserts text forwarding, default
// model/authentication/headers, both payloads, observed audio after
// cancellation, and one close/release across eight concurrent session closes.
func TestRealtimeNativeLocalWebSocket(t *testing.T) {
	incoming := make(chan []byte, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer fixture-token", r.Header.Get("Authorization"))
		assert.Equal(t, "grok-voice-latest", r.URL.Query().Get("model"))
		assert.Equal(t, "fixture", r.Header.Get("X-Test"))
		conn, err := websocket.Accept(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		// Bound the fixture's frame operations so missing relay traffic terminates
		// with a timeout instead of leaving the HTTP handler waiting indefinitely.
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		kind, data, err := conn.Read(ctx)
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, websocket.MessageText, kind)
		incoming <- data
		assert.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.audio.delta","delta":"YQ=="}`)))
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	var entered, released, closed atomic.Int64
	// One test deadline covers dialing and both payload observations, allowing a
	// failed forwarding path to unblock the waiting assertions and relay.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := DialRealtime(ctx, RealtimeDialOptions{
		BaseURL:      server.URL + "/v1/realtime",
		Token:        "fixture-token",
		ApplyHeaders: func(h http.Header) { h.Set("X-Test", "fixture") },
		Enter:        func() (func(), error) { entered.Add(1); return func() { released.Add(1) }, nil },
		Dial: func(ctx context.Context, target string, headers http.Header) (upstream.FrameConn, int, error) {
			conn, resp, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: headers})
			status := 0
			if resp != nil {
				status = resp.StatusCode
			}
			if err != nil {
				return nil, status, err
			}
			return testWSFrames{conn, &closed}, status, nil
		},
	})
	require.NoError(t, err)
	require.True(t, session.Ready())
	require.EqualValues(t, 1, entered.Load())
	require.Zero(t, released.Load())
	down := testDownFrames{make(chan []byte, 1), make(chan []byte, 1)}
	down.input <- []byte(`{"type":"input_audio_buffer.append","audio":"Yg=="}`)
	// result carries the relay's audio observation and terminal error to the
	// waiting test.
	type result struct {
		// audio and err carry one RelayRealtime return to the waiting test. The
		// buffered done channel publishes both before assertions inspect their
		// corresponding audio and termination outcomes.
		audio bool  // recognized audio observed
		err   error // relay termination error
	}
	done := make(chan result, 1)
	// Run relay independently so the test can observe both forwarded payloads
	// before cancelling its context and collecting the terminal audio evidence.
	go func() { audio, err := RelayRealtime(ctx, down, session); done <- result{audio, err} }()
	select {
	case payload := <-incoming:
		require.JSONEq(t, `{"type":"input_audio_buffer.append","audio":"Yg=="}`, string(payload))
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case payload := <-down.output:
		require.JSONEq(t, `{"type":"response.audio.delta","delta":"YQ=="}`, string(payload))
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	got := <-done
	require.True(t, got.audio)
	require.Error(t, got.err)
	var wg sync.WaitGroup
	// Competing session Close calls exercise shared cleanup ownership; the atomic
	// counters reveal duplicate connection closure or registration release.
	for range 8 {
		wg.Go(func() { ; _ = session.Close() })
	}
	wg.Wait()
	require.EqualValues(t, 1, closed.Load())
	require.EqualValues(t, 1, released.Load())
}

// TestRealtimeNativeDialFailureAndProbeError injects an HTTP 403 dial failure
// with an activity release callback. DialRealtime must retain status and error
// identity while releasing activity; ProbeRealtime must expose the original
// error and release its own registration.
func TestRealtimeNativeDialFailureAndProbeError(t *testing.T) {
	sentinel := errors.New("fixture unavailable")
	var released atomic.Int64
	options := RealtimeDialOptions{
		BaseURL: "https://fixture.invalid/v1/realtime",
		Enter:   func() (func(), error) { return func() { released.Add(1) }, nil },
		Dial:    func(context.Context, string, http.Header) (upstream.FrameConn, int, error) { return nil, 403, sentinel },
	}
	_, err := DialRealtime(context.Background(), options)
	var failure *RealtimeDialError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, 403, failure.StatusCode)
	require.ErrorIs(t, err, sentinel)
	require.EqualValues(t, 1, released.Load())
	err = ProbeRealtime(context.Background(), options)
	require.Same(t, sentinel, err)
	require.EqualValues(t, 2, released.Load())
}
