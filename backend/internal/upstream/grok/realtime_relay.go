package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Realtime Voice audio can arrive from either the client or the upstream
// connection, and metering needs evidence even when the connection ends with an
// error. Each relay direction records recognized audio before attempting
// delivery, so a write failure still leaves the observed audio flag available to
// the caller.
//
// The directions progress independently and publish their first terminal error
// into a two-entry channel. The first received error ends the relay wait, after
// which deferred cancellation signals both directions. The return samples the
// shared atomic flag after that receive; it does not wait for the other
// goroutine or close either connection. Callers close their connections, and
// concrete FrameConn implementations determine whether cancellation releases
// blocked frame operations.

// RelayRealtime forwards Voice events in both directions and returns observed
// audio with the first terminal error. Client text and binary payloads must be
// valid JSON and are forwarded as text; upstream payloads also use text
// delivery. Recognition occurs before writes, so the flag describes observation
// rather than delivery.
//
// Two goroutines each publish one terminal result to a channel with capacity
// two. The first result wins and deferred cancellation signals the other
// direction. Callers retain connection-closing responsibility and provide frame
// operations that respond to cancellation; the relay does not join the
// goroutines.
func RelayRealtime(ctx context.Context, client, conn upstream.FrameConn) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Each direction sends one terminal error. Two buffered entries let the later
	// sender finish after the caller accepts the first result.
	errCh := make(chan error, 2)
	var audioObserved atomic.Bool

	// The upstream direction records audio before delivery so a disconnected client
	// still leaves evidence for session metering when this goroutine publishes its
	// error.
	go func() {
		for {
			_, msg, readErr := conn.ReadFrame(ctx)
			if readErr != nil {
				errCh <- readErr
				return
			}
			if GrokRealtimeEventHasAudio(msg) {
				audioObserved.Store(true)
			}
			if writeErr := client.WriteFrame(ctx, upstream.FrameText, msg); writeErr != nil {
				errCh <- writeErr
				return
			}
		}
	}()

	// Client events arrive as text or binary, but xAI receives JSON text frames.
	// Record audio before validating or writing, then publish any terminal failure.
	go func() {
		for {
			kind, msg, readErr := client.ReadFrame(ctx)
			if readErr != nil {
				errCh <- readErr
				return
			}
			if kind != upstream.FrameText && kind != upstream.FrameBinary {
				continue
			}
			if GrokRealtimeEventHasAudio(msg) {
				audioObserved.Store(true)
			}
			var raw json.RawMessage
			if unmarshalErr := json.Unmarshal(msg, &raw); unmarshalErr != nil {
				errCh <- fmt.Errorf("invalid realtime event: %w", unmarshalErr)
				return
			}
			if writeErr := conn.WriteFrame(ctx, upstream.FrameText, raw); writeErr != nil {
				errCh <- writeErr
				return
			}
		}
	}()

	// Load the audio flag after the winning terminal result arrives; deferred
	// cancellation then signals any still-running frame operation.
	return AwaitGrokRealtimeAudioObserved(errCh, &audioObserved)
}
