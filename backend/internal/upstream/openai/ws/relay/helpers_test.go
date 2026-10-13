package openai_ws_v2

import (
	"context"
	"io"
	"sync"

	coderws "github.com/coder/websocket"
)

// passthroughTestFrame stores one raw message type and payload for relay
// fixtures.
type passthroughTestFrame struct {
	msgType coderws.MessageType // raw fixture frame type
	payload []byte              // raw fixture frame bytes
}

// passthroughTestFrameConn supplies channel reads and mutex-protected write
// capture for relay tests.
type passthroughTestFrameConn struct {
	// mu protects writes while readCh drives independent fixture reads. once
	// controls method closure; constructor auto-close is also tolerated by Close
	// so relay cleanup can run for either fixture EOF mode.
	mu     sync.Mutex                // captured write state mutex
	writes []passthroughTestFrame    // recorded output frame sequence
	readCh chan passthroughTestFrame // buffered input frame queue
	once   sync.Once                 // read-channel close invocation guard
}

// newPassthroughTestFrameConn copies supplied frame payloads into a buffered
// read queue. autoClose closes that queue after its fixtures, producing EOF
// after consumption; callers own connection cleanup for queues left open.
func newPassthroughTestFrameConn(frames []passthroughTestFrame, autoClose bool) *passthroughTestFrameConn {
	c := &passthroughTestFrameConn{
		readCh: make(chan passthroughTestFrame, len(frames)+1),
	}
	for _, frame := range frames {
		copied := passthroughTestFrame{msgType: frame.msgType, payload: append([]byte(nil), frame.payload...)}
		c.readCh <- copied
	}
	if autoClose {
		close(c.readCh)
	}
	return c
}

// ReadFrame returns the next fixture frame with a copied payload or a
// context/EOF error. Nil ctx selects Background, and cancellation races
// normally with a ready queued frame through the select.
func (c *passthroughTestFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return coderws.MessageText, nil, ctx.Err()
	case frame, ok := <-c.readCh:
		if !ok {
			return coderws.MessageText, nil, io.EOF
		}
		return frame.msgType, append([]byte(nil), frame.payload...), nil
	}
}

// WriteFrame checks context cancellation and records a copied payload under the
// fixture mutex. Nil ctx selects Background; callers inspect records through
// Writes, and read-queue closure does not prohibit this write capture.
func (c *passthroughTestFrameConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, passthroughTestFrame{msgType: msgType, payload: append([]byte(nil), payload...)})
	return nil
}

// Close closes the fixture read channel through sync.Once and tolerates
// constructor auto-close. Repeated calls return nil; closing stops pending
// reads with EOF while captured writes remain available for assertions.
func (c *passthroughTestFrameConn) Close() error {
	c.once.Do(func() {
		// autoClose fixtures already closed readCh during construction. Swallow that
		// close panic so relay cleanup retains the same nil result for both fixture modes.
		defer func() { _ = recover() }()
		close(c.readCh)
	})
	return nil
}

// Writes copies the recorded frame slice under its mutex. Payload bytes remain
// shared with fixture records, so callers treat returned payloads as immutable
// while they inspect the write sequence.
func (c *passthroughTestFrameConn) Writes() []passthroughTestFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]passthroughTestFrame, len(c.writes))
	copy(out, c.writes)
	return out
}
