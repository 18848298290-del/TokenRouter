package provider

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Background credential maintenance starts with an immediate cycle and then runs
// cycles sequentially on a ticker. Concurrent StartContext calls compete for one
// start flag, and StopContext permanently closes startup even when the loop was
// never started. A cycle that ignores cancellation can keep work unfinished
// after the stop budget expires.
//
// The loop mutex protects start and stop ownership, while operationActivity
// links the running goroutine to parent cancellation and shutdown. Startup
// registers that activity before launching the goroutine, and its deferred
// completion closes the join only after the cycle returns and ticker stops.
// Shutdown retains its first completed result, including a timeout, so a later
// call does not turn incomplete drainage into reported success.

// RefreshLoop coordinates one periodic credential maintenance goroutine and its
// permanent shutdown state.
type RefreshLoop struct {
	// mu protects started and stopped so a concurrent stop or repeated start
	// cannot launch another loop. Both flags remain set once their event occurs.
	mu               sync.Mutex // start and stop synchronization
	started, stopped bool       // loop launch and shutdown flags
	// sequential maintenance cycle callback
	cycle    func(context.Context)
	activity operationActivity // registered loop cancellation and join
}

// NewRefreshLoop captures the maintenance callback and leaves startup to
// StartContext.
func NewRefreshLoop(cycle func(context.Context)) *RefreshLoop { return &RefreshLoop{cycle: cycle} }

// StartContext registers and launches the first loop, returning true when this
// call starts it. Nil receiver or callback, a prior start, or permanent stop
// returns false; a reached nonpositive interval returns an error.
//
// Nil parent selects context.Background, and an already canceled parent prevents
// registration. The mutex holds startup ownership until activity is registered
// and the goroutine launched. The callback runs immediately when the derived
// context is live, then serially on ticker events. Cancellation is rechecked
// before later cycles, and deferred activity completion waits for the actual
// callback return rather than assuming its context caused it to finish.
func (l *RefreshLoop) StartContext(parent context.Context, interval time.Duration) (bool, error) {
	if l == nil || l.cycle == nil {
		return false, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started || l.stopped {
		return false, nil
	}
	if interval <= 0 {
		return false, errors.New("token refresh interval must be positive")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, done, err := l.activity.begin(parent, ErrRefreshStopped)
	if err != nil {
		return false, err
	}
	// Mark startup after registration and before launch so concurrent callers
	// observe one owned loop whose actual completion remains tracked for
	// shutdown.
	l.started = true
	go func() {
		// Complete registration after ticker cleanup and the last callback return;
		// shutdown cannot report drainage merely because cancellation was delivered.
		defer done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		if ctx.Err() == nil {
			l.cycle(ctx)
		}
		// Recheck cancellation after ticker admission because select can choose a
		// ready tick when cancellation is also ready; that observation stops a
		// subsequent cycle from starting.
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				l.cycle(ctx)
			}
		}
	}()
	return true, nil
}

// StopContext prevents future startup, cancels the loop, and waits for its
// registered work within ctx. A nil receiver succeeds, while reached shutdown
// requires a nonnil context.
//
// The stopped flag is published under the loop mutex before operationActivity
// cancels and joins the goroutine. The first completed shutdown result remains
// fixed, including a timeout caused by unfinished cycle work. A concurrent
// caller waiting for that first result can exhaust its own context, and the loop
// remains permanently stopped even after the last callback later returns.
func (l *RefreshLoop) StopContext(ctx context.Context) error {
	if l == nil {
		return nil
	}
	// Close startup before canceling activity so a concurrent first start cannot
	// create work after shutdown has begun waiting for the existing registration.
	l.mu.Lock()
	l.stopped = true
	l.mu.Unlock()
	return l.activity.stop(ctx, "token refresh")
}
