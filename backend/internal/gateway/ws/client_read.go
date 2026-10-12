package ws

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// ClientConn supports sending a close frame before joining the active reader.
type ClientConn interface {
	Read(context.Context) (int, []byte, error)
	Close(int, string) error
	CloseNow() error
}

// ClientCloseError records the close status, reason, and underlying cause.
type ClientCloseError struct {
	Status int
	Reason string
	Cause  error
}

type clientReadResult struct {
	messageType int
	payload     []byte
	err         error
}

func NewClientCloseError(status int, reason string, cause error) *ClientCloseError {
	return &ClientCloseError{Status: status, Reason: strings.TrimSpace(reason), Cause: cause}
}

func (e *ClientCloseError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return fmt.Sprintf("openai ws client close: %d %s", e.Status, strings.TrimSpace(e.Reason))
	}
	return fmt.Sprintf("openai ws client close: %d %s: %v", e.Status, strings.TrimSpace(e.Reason), e.Cause)
}

func (e *ClientCloseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ReadClientMessage keeps one reader alive while a control event sends the close frame,
// then closes the transport and waits for the reader to exit.
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

// ReadClientMessageWithTimeoutStart can start the timer after a state transition, such as turn completion.
// A positive timeout starts immediately when timeoutActive is nil or returns true.
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
