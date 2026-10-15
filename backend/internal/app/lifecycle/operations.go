package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// operations.go tracks admitted operations and seals admission while shutdown
// waits for callers to release their slots. Operation admission and shutdown
// share a count of calls that have not released their slots. The idle channel
// starts closed, becomes a new open channel for the first admitted call, and
// closes again when the last call releases. A stop seals admission before
// capturing that channel, preventing a new operation from replacing the group
// being drained.
//
// The first stop records its result after the captured group becomes idle or
// its context expires. Repeated stops observe that stored result after its
// completion signal, although each waiting caller may cancel its own wait.
// Operation releases remain valid after a timeout, and each release is
// idempotent so duplicate cleanup cannot decrement the active count twice.

// Operations admits counted work and supplies a shared shutdown result for that work.
// Construct it with NewOperations so the initial empty group has an idle signal.
type Operations struct {
	// mu protects active, idle, and closed. Enter opens idle for a new active
	// group; the final release closes it, and closed prevents a later group
	// from replacing the idle signal captured by shutdown.
	mu     sync.Mutex    // operation admission lock
	active int           // unreleased operation count
	idle   chan struct{} // current group completion
	closed bool          // new admission sealed
	// stopDone publishes stopErr from the first stop after its drain attempt
	// finishes. Later callers read that result after observing the completion signal.
	stopDone chan struct{} // first stop completion
	stopErr  error         // first stop result
	name     string        // operation diagnostic label
}

// NewOperations constructs an empty operation barrier named for diagnostics.
// Its initial idle channel is closed, so stopping before any admission can
// finish immediately; callers release every subsequently admitted operation.
func NewOperations(name string) *Operations {
	idle := make(chan struct{})
	close(idle)
	return &Operations{name: name, idle: idle}
}

// Enter admits one operation and returns its idempotent release function.
// Calling after shutdown seals admission returns a stopped error. The caller
// invokes release when all work covered by that operation has finished.
//
// The first call in an empty group replaces its closed idle signal before
// incrementing active under the mutex. Releases decrement that same count
// under the mutex and close idle at zero. A per-operation Once prevents normal
// completion and cleanup from both returning the same slot.
func (o *Operations) Enter() (func(), error) {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil, errors.New(o.name + " is stopped")
	}
	if o.active == 0 {
		// The prior empty group already has a closed signal. Replace it before
		// admitting work so shutdown cannot mistake this new operation for an idle group.
		o.idle = make(chan struct{})
	}
	o.active++
	o.mu.Unlock()
	var once sync.Once
	// Normal completion and deferred cleanup may both release this admission.
	// Once protects the decrement and the final idle close from duplicate releases.
	return func() {
		once.Do(func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.active--
			if o.active == 0 {
				close(o.idle)
			}
		})
	}, nil
}

// StopContext seals admission and drains the group captured by the first stop.
// Its context can end that drain with an error containing the operation count
// observed at timeout. Later operation releases do not replace the stored result.
//
// Repeated calls wait for the first result through stopDone, or return their
// own context error while waiting. The first call captures idle only after
// setting closed under the admission mutex, so a later Enter cannot start a
// new group outside the drain. Stop does not cancel work already admitted.
func (o *Operations) StopContext(ctx context.Context) error {
	o.mu.Lock()
	if o.closed {
		// Another caller owns the drain and its result. Wait for its publication
		// while allowing this caller's context to end only this wait.
		done := o.stopDone
		o.mu.Unlock()
		select {
		case <-done:
			return o.stopErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Seal admission before taking the group's signal. Future Enter calls
	// cannot replace idle while this stop waits for the admitted operations.
	o.closed = true
	o.stopDone = make(chan struct{})
	done, idle := o.stopDone, o.idle
	o.mu.Unlock()
	var err error
	select {
	case <-idle:
	default:
		select {
		case <-idle:
		case <-ctx.Done():
			o.mu.Lock()
			count := o.active
			o.mu.Unlock()
			err = fmt.Errorf("%s: %d operations unfinished: %w", o.name, count, ctx.Err())
		}
	}
	o.mu.Lock()
	// Publish the stored result before closing stopDone. Its receivers can then
	// read the first stop error even after later releases make the group idle.
	o.stopErr = err
	close(done)
	o.mu.Unlock()
	return err
}
