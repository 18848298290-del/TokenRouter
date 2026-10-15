package search

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// lifecycle.go tracks search operations across Manager generations and waits
// for their completion during shutdown.
// Replacing a search Manager leaves requests from the previous configuration
// generation active, including quota release after a failed supplier call. A
// shared WorkGroup counts these operations across generations so shutdown waits
// for the cleanup that still uses quota storage.
//
// Admission and completion change under one mutex. Stop closes admission
// permanently, and either Stop or the final registered completion closes the
// shared notification channel. Completion is idempotent because callers may
// invoke it through more than one cleanup path. A timed-out waiter leaves the
// group stopped and can wait again with another context.

// WorkGroup counts admitted search and configuration operations until shutdown
// drains them.
type WorkGroup struct {
	// mu protects active, stopped, and the decision to close done. Begin and Stop
	// share this lock so registration cannot enter after the stop decision.
	mu      sync.Mutex    // admission and completion lock
	active  int           // unfinished registered operation count
	stopped bool          // permanently closed admission flag
	done    chan struct{} // stopped and drained notification
}

// NewWorkGroup constructs an accepting group with an open drain-notification
// channel.
func NewWorkGroup() *WorkGroup { return &WorkGroup{done: make(chan struct{})} }

// Begin registers an operation and returns its completion function, or an error
// after stop admission has closed. Use a group constructed by NewWorkGroup, and
// invoke completion after the operation and any quota cleanup have returned.
//
// Registration and the stop decision use the same mutex, so accepted work
// contributes to Stop's drain condition. The returned function uses sync.Once
// to decrement once even when completion paths overlap; the final completion
// closes done after the group has stopped.
func (g *WorkGroup) Begin() (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return nil, errors.New("websearch: runtime stopped")
	}
	// Account for this admitted operation before releasing mu so Stop cannot
	// observe a drained group while its completion remains pending.
	g.active++
	var once sync.Once
	return func() {
		// Count cleanup completion once so overlapping caller cleanup paths cannot
		// underflow active or close the stopped group's notification twice.
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.active--
			if g.stopped && g.active == 0 {
				close(g.done)
			}
		})
	}, nil
}

// Stop permanently rejects new operations and waits for registered operations
// to complete. Each caller supplies a nonnil waiting context; a timeout reports
// a sampled unfinished count and leaves admission closed for later waits.
//
// The first stop decision closes done immediately when no operations remain.
// Otherwise the final completion performs that close under the same mutex.
// Waiting occurs after releasing the mutex, allowing completions to drain the
// group. When done and cancellation are both ready, either select result can be
// returned.
func (g *WorkGroup) Stop(ctx context.Context) error {
	g.mu.Lock()
	// Close admission under the same lock as Begin so accepted work is counted
	// before Stop decides whether done can close.
	if !g.stopped {
		g.stopped = true
		if g.active == 0 {
			close(g.done)
		}
	}
	done := g.done
	g.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		n := g.active
		g.mu.Unlock()
		return fmt.Errorf("websearch: %d unfinished operations: %w", n, ctx.Err())
	}
}
