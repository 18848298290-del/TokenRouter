package ws

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// client_read.go coordinates WebSocket client reads with control cancellation
// and idle timeout closure. Control cancellation sends a close frame while the
// reader can still process its handshake. Canceling the read context first
// would let the transport end before that frame can be delivered.
//
// ReadClientMessageWithTimeoutStart starts one background reader and selects
// its result against control cancellation and a separately armed idle timer. On
// a control event it sends the chosen close status, closes the transport, and
// joins the reader before returning a ClientCloseError. Timeout-start
// notifications can reset the idle budget only when the supplied activity
// predicate permits it; active upstream turns can therefore keep the client
// reader alive without counting their generation time as inter-turn idleness.

// ClientConn permits close-frame delivery while an outstanding frame read
// remains active.
type ClientConn interface {
	// Read waits for one client frame using the supplied context. Implementations
	// must finish an outstanding read when CloseNow terminates the transport,
	// because the control path joins that reader before returning.
	Read(context.Context) (int, []byte, error)
	// Close sends the selected WebSocket status and reason while the reader
	// remains active. The control path ignores its error and follows with
	// CloseNow, so handshake failure cannot skip transport cleanup.
	Close(int, string) error
	// CloseNow terminates the transport and releases an outstanding reader.
	// ReadClientMessageWithTimeoutStart calls it after attempting a close frame,
	// then waits for Read to finish before returning to its caller.
	CloseNow() error
}

// ClientCloseError retains the gateway-selected close instruction and its
// cancellation cause.
type ClientCloseError struct {
	Status int    // WebSocket close status code
	Reason string // trimmed client close reason
	Cause  error  // underlying close control cause
}

// clientReadResult carries one completed background read to the control loop.
type clientReadResult struct {
	messageType int    // received WebSocket message type
	payload     []byte // received client frame bytes
	err         error  // underlying frame read error
}

// NewClientCloseError stores the chosen status and cause with a trimmed reason.
// It constructs a close instruction for caller presentation; transport closure
// is performed separately by the read controller or HTTP adapter.
func NewClientCloseError(status int, reason string, cause error) *ClientCloseError {
	return &ClientCloseError{Status: status, Reason: strings.TrimSpace(reason), Cause: cause}
}

// Error formats the chosen close instruction and optional cause for
// diagnostics. A nil receiver returns empty; an absent cause leaves the status
// and trimmed reason in the message.
func (e *ClientCloseError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return fmt.Sprintf("openai ws client close: %d %s", e.Status, strings.TrimSpace(e.Reason))
	}
	return fmt.Sprintf("openai ws client close: %d %s: %v", e.Status, strings.TrimSpace(e.Reason), e.Cause)
}

// Unwrap exposes the close control cause to errors.Is and errors.As. A nil
// receiver or absent Cause returns nil; status and reason remain available for
// the caller's close presentation.
func (e *ClientCloseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ReadClientMessage waits for a client frame with immediate timeout arming when
// timeout is positive. It delegates control-event close delivery and reader
// joining to ReadClientMessageWithTimeoutStart; nonpositive timeouts leave
// cancellation as the control mechanism.
func ReadClientMessage(
	controlCtx context.Context,
	conn ClientConn,
	timeout time.Duration,
	timeoutStatus int,
	timeoutReason string,
) (int, []byte, error) {
	return ReadClientMessageWithTimeoutStart(
		controlCtx,
		conn,
		timeout,
		timeoutStatus,
		timeoutReason,
		nil,
		nil,
	)
}

// ReadClientMessageWithTimeoutStart waits for one frame while independently
// controlling cancellation and idle timeout. A nil connection returns an error,
// nil controlCtx uses Background, and a nonpositive timeout disables the timer.
//
// A positive timer starts immediately when timeoutActive is absent or true, and
// timeoutStart notifications can restart it after a later activity transition.
// The predicate controls arming and rearming; returning false leaves any timer
// already armed in place. The background Read uses an uncancelled context so
// control events can attempt
// a close frame before CloseNow ends the transport. Cancellation selects 1013
// for a lost ingress lease and 1001 otherwise; timeout uses the supplied close
// instruction. Each control path joins the reader, requiring the connection's
// CloseNow to unblock it.
func ReadClientMessageWithTimeoutStart(
	controlCtx context.Context,
	conn ClientConn,
	timeout time.Duration,
	timeoutStatus int,
	timeoutReason string,
	timeoutStart <-chan struct{},
	timeoutActive func() bool,
) (int, []byte, error) {
	if conn == nil {
		return 0, nil, errors.New("openai websocket client connection is nil")
	}
	if controlCtx == nil {
		controlCtx = context.Background()
	}

	// Keep Read independent of control cancellation so it can service the close
	// handshake; buffer its single result while the controller sends closure.
	readDone := make(chan clientReadResult, 1)
	go func() {
		messageType, payload, err := conn.Read(context.Background())
		readDone <- clientReadResult{messageType: messageType, payload: payload, err: err}
	}()

	var timer *time.Timer
	var timeoutCh <-chan time.Time
	startTimeout := func() {
		if timeout <= 0 || (timeoutActive != nil && !timeoutActive()) {
			return
		}
		if timer == nil {
			timer = time.NewTimer(timeout)
		} else {
			// Drain an already fired timer before resetting the idle budget, so its
			// earlier expiry cannot close a newly idle interval.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		}
		timeoutCh = timer.C
	}
	if timeoutActive == nil || timeoutActive() {
		startTimeout()
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	// Attempt close delivery while Read is still active, then terminate the
	// transport and join that read before returning its control cause.
	closeAndJoin := func(status int, reason string, cause error) (int, []byte, error) {
		_ = conn.Close(status, reason)
		_ = conn.CloseNow()
		<-readDone
		return 0, nil, NewClientCloseError(status, reason, cause)
	}

	for {
		select {
		case result := <-readDone:
			return result.messageType, result.payload, result.err
		case <-timeoutStart:
			startTimeout()
		case <-timeoutCh:
			return closeAndJoin(timeoutStatus, timeoutReason, context.DeadlineExceeded)
		case <-controlCtx.Done():
			cause := context.Cause(controlCtx)
			if errors.Is(cause, scheduler.ErrOpenAIWSIngressLeaseLost) {
				return closeAndJoin(
					1013,
					"websocket ingress capacity lease lost; please reconnect",
					cause,
				)
			}
			return closeAndJoin(1001, "websocket request canceled", cause)
		}
	}
}
