package provider

import (
	"context"
	"fmt"
	"sync"
)

// A provider operation can stop waiting before the shared work it started has
// finished. Each accepted caller or shared task registers its own cancellation
// function, and completion removes that registration, so shutdown can wait for
// both lifetimes even when they end at different times. Admission and the
// permanent stop flag use the same mutex.
//
// The first stop closes admission, cancels the registered contexts, and waits on
// the idle signal for the current group of operations. Its success or timeout is
// retained for later stops. A deadline reports unfinished work; the
// still-registered operations complete through their original finish functions,
// and subsequent callers cannot turn that recorded timeout into a successful
// drain.

// operationActivity records cancellable provider work and one permanent shutdown
// result.
type operationActivity struct {
	// mu protects stopped, next, active, idle, stopDone, and stopErr. Admission,
	// completion, and the first stop change this state together, while cancellation
	// functions are invoked after the admission lock has been released.
	mu      sync.Mutex                    // operation lifecycle lock
	stopped bool                          // permanent admission barrier
	next    uint64                        // next operation identifier
	active  map[uint64]context.CancelFunc // registered operation cancellation functions
	// idle is replaced when the active set becomes nonempty and closed when its
	// final finish function runs. A stop captures that group's completion signal.
	idle chan struct{}
	// stopDone publishes stopErr after the first stop finishes its wait. Later
	// callers wait on that same signal before reading the retained result.
	stopDone chan struct{} // first stop result publication signal
	stopErr  error         // first shutdown wait result
}

// begin admits an operation and returns its cancellable context and finish
// function. The parent is nonnil; an already cancelled parent returns its error
// before admission, and closed admission returns stoppedError. The caller
// invokes finish exactly once when its work ends.
//
// Registration and the idle-channel transition happen under the stop mutex, so
// shutdown either rejects this operation or includes its cancellation and
// completion in the captured active group. The finish function cancels the
// derived context and removes its registration; only the final registered
// operation closes the group signal.
func (state *operationActivity) begin(parent context.Context, stoppedError error) (context.Context, func(), error) {
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	// A newly nonempty active set needs its own open idle signal. Register it with
	// this cancellation function under the stop lock so an earlier group's closed
	// channel cannot make shutdown mistake this work for already finished.
	state.mu.Lock()
	if state.stopped {
		state.mu.Unlock()
		return nil, nil, stoppedError
	}
	if len(state.active) == 0 {
		state.idle = make(chan struct{})
	}
	if state.active == nil {
		state.active = make(map[uint64]context.CancelFunc)
	}
	state.next++
	id := state.next
	ctx, cancel := context.WithCancel(parent)
	state.active[id] = cancel
	state.mu.Unlock()
	return ctx, func() {
		// Completion removes this registration before signalling idle. Closing after
		// the last removal lets shutdown wait for actual work completion rather than
		// cancellation delivery.
		cancel()
		state.mu.Lock()
		delete(state.active, id)
		if len(state.active) == 0 {
			close(state.idle)
		}
		state.mu.Unlock()
	}, nil
}

// stop closes admission, cancels registered operations, and retains the first
// shutdown result. A nil state succeeds. Callers provide a nonnil waiting
// context; expiry while work remains returns a labelled error wrapping that
// context error.
//
// The first caller captures the active group after setting stopped under the
// admission mutex. Cancellation runs outside the mutex, allowing each finish
// function to remove its registration. An already idle group succeeds even with
// an expired budget. Later callers wait for the first stop publication or their
// own cancellation, then return the retained result once available; late work
// completion does not revise it.
func (state *operationActivity) stop(ctx context.Context, label string) error {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	// Prefer an already published result over this caller's expired wait context.
	// Pending publication uses this caller's budget and leaves the first stop in
	// charge of cancelling and waiting for the captured work.
	if state.stopped {
		done := state.stopDone
		state.mu.Unlock()
		select {
		case <-done:
			return state.stopErr
		default:
		}
		select {
		case <-done:
			return state.stopErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Close admission before copying cancellation functions and the idle signal.
	// The snapshot then covers every accepted operation still active when this
	// first stop begins.
	state.stopped = true
	state.stopDone = make(chan struct{})
	done := state.stopDone
	idle := state.idle
	cancels := make([]context.CancelFunc, 0, len(state.active))
	for _, cancel := range state.active {
		cancels = append(cancels, cancel)
	}
	state.mu.Unlock()
	// Run cancellation after releasing the state mutex so finish functions can
	// acquire it and close the captured idle signal while shutdown waits.
	for _, cancel := range cancels {
		cancel()
	}
	var err error
	// Prefer an already closed idle signal before consulting the wait budget.
	// Completed work can report a successful drain even when that budget has
	// expired.
	if idle != nil {
		select {
		case <-idle:
		default:
			select {
			case <-idle:
			case <-ctx.Done():
				err = fmt.Errorf("%s work remains unfinished: %w", label, ctx.Err())
			}
		}
	}
	state.mu.Lock()
	// Publish the first wait result before closing stopDone. Later callers read
	// that result after the signal, including a timeout retained after eventual
	// completion.
	state.stopErr = err
	close(done)
	state.mu.Unlock()
	return err
}
