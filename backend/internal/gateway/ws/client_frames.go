package ws

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// client_frames.go wraps client frames with inbound policy, outbound
// restoration, and inter-turn activity tracking. Inter-turn idle timing starts
// after a terminal write completes, so time spent waiting for the current
// answer is excluded from that idle budget.
//
// clientFrameConn keeps a control context for reads and removes cancellation
// from each write while retaining its existing deadline. This lets a
// control-triggered close wait for a frame already being delivered.
// policyFrameConn runs synchronous filters around that transport; a blocked
// inbound frame emits its policy callback and returns a close instruction.
// TurnActivity shares an atomic waiting flag and a coalesced completion
// notification between the read controller and terminal delivery callbacks.

// clientFrameConn adapts client transport with idle control and response
// metadata restoration.
type clientFrameConn struct {
	// closeError and closedError supply adapter-specific failure forms. ReadFrame
	// and WriteFrame require a nonnil wrapper even when its inner connection is
	// missing.
	closeError  func(int, string, error) error // typed close error constructor
	closedError error                          // missing transport error value
	// normalizeCompleted is called for every downstream text frame and must be
	// supplied. Optional model and tool restorers run afterward on that normalized
	// payload.
	normalizeCompleted func([]byte) ([]byte, bool) // terminal event compatibility normalizer
	conn               ClientSocket                // accepted client socket transport
	controlCtx         context.Context             // client read control context
	// interTurnIdleTimeout supplies the read budget when idleTimeout is absent.
	// The callback can select updated settings for each next client read.
	interTurnIdleTimeout time.Duration        // static inter-turn idle budget
	idleTimeout          func() time.Duration // current idle budget reader
	// interTurnStarted and waitingForNextTurn share completion state with the
	// reader. Completion publishes the flag before a nonblocking notification
	// starts or resets its idle timer.
	interTurnStarted     chan struct{}       // coalesced turn completion notification
	waitingForNextTurn   atomic.Bool         // inter-turn idle activity flag
	restoreResponseModel func([]byte) []byte // client model restoration function
	restoreToolNames     func([]byte) []byte // client tool restoration function
}

// policyFrameConn synchronously filters frames around an existing bidirectional
// relay connection.
type policyFrameConn struct {
	// closeError is required when filter can return a block. ReadFrame and
	// WriteFrame require a nonnil wrapper even when inner is missing.
	closeError  func(int, string, error) error // policy close error constructor
	closedError error                          // missing inner connection error
	inner       FrameConn                      // wrapped synchronous frame connection
	// filter may replace inbound bytes, report a policy block, or return an error.
	// onBlock runs for a block before ReadFrame returns its close instruction.
	filter func(msgType int, payload []byte) ([]byte, *PolicyBlocked, error) // inbound policy evaluation callback
	// writeFilter optionally rewrites each outgoing payload before
	// inner.WriteFrame. A returned error prevents forwarding that frame to the
	// client.
	writeFilter func(msgType int, payload []byte) ([]byte, error) // outbound payload rewrite callback
	onBlock     func(blocked *PolicyBlocked)                      // inbound policy rejection callback
}

// TurnActivity publishes terminal completion and active-turn state to the idle
// reader.
type TurnActivity struct {
	// Waiting is required and shared with the read activity predicate.
	// MarkCompleted stores true before notifying Started; MarkStarted clears it
	// when admission succeeds.
	Waiting *atomic.Bool  // shared inter-turn waiting flag
	Started chan struct{} // completion timer start notification
}

// ReadFrame obtains one inner frame and synchronously applies the optional
// inbound filter. A nonnil wrapper is required; filter failures propagate, and
// policy blocks invoke onBlock before returning the configured close error.
func (c *policyFrameConn) ReadFrame(ctx context.Context) (int, []byte, error) {
	if c == nil || c.inner == nil {
		return TextFrame, nil, c.closedError
	}
	msgType, payload, err := c.inner.ReadFrame(ctx)
	if err != nil {
		return msgType, payload, err
	}
	if c.filter == nil {
		return msgType, payload, nil
	}
	updated, blocked, filterErr := c.filter(msgType, payload)
	if filterErr != nil {
		return msgType, payload, filterErr
	}
	if blocked != nil {
		if c.onBlock != nil {
			c.onBlock(blocked)
		}
		return msgType, nil, c.closeError(1008, blocked.Message, blocked)
	}
	return msgType, updated, nil
}

// WriteFrame applies the optional outbound filter before delegating the
// synchronous write. A nonnil wrapper is required; a filter error prevents
// delegation and a missing inner connection returns closedError.
func (c *policyFrameConn) WriteFrame(ctx context.Context, msgType int, payload []byte) error {
	if c == nil || c.inner == nil {
		return c.closedError
	}
	if c.writeFilter != nil {
		updated, err := c.writeFilter(msgType, payload)
		if err != nil {
			return err
		}
		payload = updated
	}
	return c.inner.WriteFrame(ctx, msgType, payload)
}

// Close delegates transport cleanup to the wrapped connection when present. A
// nil wrapper or missing inner connection returns nil; transport-close errors
// from an available inner connection are returned to the caller.
func (c *policyFrameConn) Close() error {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.inner.Close()
}

// ReadFrame waits under the stored control context and inter-turn idle budget.
// A nonnil wrapper is required; the activity flag delays timer arming until
// completion, and typed control-close results use the injected error
// constructor.
func (c *clientFrameConn) ReadFrame(ctx context.Context) (int, []byte, error) {
	if c == nil || c.conn == nil {
		return TextFrame, nil, c.closedError
	}
	controlCtx := ctx
	if c.controlCtx != nil {
		controlCtx = c.controlCtx
	}
	idleTimeout := c.interTurnIdleTimeout
	if c.idleTimeout != nil {
		idleTimeout = c.idleTimeout()
	}
	msgType, payload, err := ReadClientMessageWithTimeoutStart(
		controlCtx,
		c.conn,
		idleTimeout,
		1000,
		"websocket idle timeout",
		c.interTurnStarted,
		func() bool { return c.waitingForNextTurn.Load() },
	)
	if closeErr, ok := errors.AsType[*ClientCloseError](err); ok {
		err = c.closeError(closeErr.Status, closeErr.Reason, closeErr.Cause)
	}
	return msgType, payload, err
}

// markTurnStarted clears the waiting flag after a response.create reservation
// succeeds. A nil wrapper is ignored; the terminal delivery callback later sets
// that flag again to begin inter-turn idle timing.
func (c *clientFrameConn) markTurnStarted() {
	if c != nil {
		TurnActivity{Waiting: &c.waitingForNextTurn, Started: c.interTurnStarted}.MarkStarted()
	}
}

// markTurnCompleted publishes the waiting flag and timer-start notification
// after terminal delivery succeeds. A nil wrapper is ignored; the notification
// is coalesced so completion does not wait for the reader.
func (c *clientFrameConn) markTurnCompleted() {
	if c != nil {
		TurnActivity{Waiting: &c.waitingForNextTurn, Started: c.interTurnStarted}.MarkCompleted()
	}
}

// WriteFrame normalizes text terminals and restores client model and tool
// metadata before synchronous delivery. A nonnil wrapper and text normalizer
// are required, while a nil context uses Background.
//
// A context already canceled prevents output. Once processing begins, the write
// context removes cancellation while retaining any existing deadline, allowing
// the frame in progress to continue while the control path attempts closure.
// The transport's close handshake retains its own timeout and can still end
// that send. This separates lease-loss signaling from the WebSocket library's
// write-context cancellation, which closes the transport on error. The payload
// can remain shared when the supplied normalizer and restorers leave it
// unchanged.
func (c *clientFrameConn) WriteFrame(ctx context.Context, msgType int, payload []byte) error {
	if c == nil || c.conn == nil {
		return c.closedError
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if msgType == TextFrame {
		if normalized, changed := c.normalizeCompleted(payload); changed {
			payload = normalized
		}
		if c.restoreResponseModel != nil {
			payload = c.restoreResponseModel(payload)
		}
		if c.restoreToolNames != nil {
			payload = c.restoreToolNames(payload)
		}
	}
	// Keep the send's deadline while removing lease cancellation, so the read
	// controller can attempt its close handshake instead of directly aborting the
	// send through the write context.
	writeCtx := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		writeCtx, cancel = context.WithDeadline(writeCtx, deadline)
		defer cancel()
	}
	return c.conn.Write(writeCtx, msgType, payload)
}

// Close attempts a normal close handshake and then immediate transport closure.
// Nil wrappers and missing sockets return nil; both close errors are ignored
// because the relay cleanup path treats transport termination as best effort.
func (c *clientFrameConn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	// Attempt the normal handshake before forcing closure, ignoring both transport
	// errors so relay cleanup remains available after partial disconnection.
	_ = c.conn.Close(1000, "")
	_ = c.conn.CloseNow()
	return nil
}

// NewPolicyFrames wraps synchronous inbound and outbound filters around inner
// for relay adapters. Callers provide closeError when inbound filtering can
// block; the wrapper retains supplied connection and callback references for
// its lifetime.
func NewPolicyFrames(inner FrameConn, filter func(int, []byte) ([]byte, *PolicyBlocked, error), writeFilter func(int, []byte) ([]byte, error), onBlock func(*PolicyBlocked), closeError func(int, string, error) error, closedError error) FrameConn {
	return &policyFrameConn{inner: inner, filter: filter, writeFilter: writeFilter, onBlock: onBlock, closeError: closeError, closedError: closedError}
}

// MarkStarted clears the required atomic Waiting flag when a turn is accepted.
// The lifecycle caller serializes this change with terminal delivery so idle
// reads do not start counting active generation time.
func (a TurnActivity) MarkStarted() { a.Waiting.Store(false) }

// MarkCompleted publishes inter-turn waiting and sends a nonblocking completion
// notification. Waiting must be nonnil; a full or nil Started channel drops the
// notification while retaining the atomic activity state.
func (a TurnActivity) MarkCompleted() {
	// Publish idle state before notifying the reader, so its activity predicate
	// can arm the timer when it consumes this completion notification.
	a.Waiting.Store(true)
	select {
	case a.Started <- struct{}{}:
	default:
	}
}
