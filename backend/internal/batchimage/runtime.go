package batchimage

import (
	"context"
	"fmt"
	"sync"
)

// Background batch loops share one cancellation context and one completion
// signal for their launched work. Startup publishes those handles under the
// lifecycle mutex before callbacks begin, so a concurrent stop waits for the
// same invocation even when cancellation arrives before a callback runs.
//
// The first stop closes admission permanently and publishes its wait result to
// later stop callers. A caller deadline can publish an unfinished-work error
// while loops continue returning after cancellation; later calls reuse that
// stored result instead of starting a new wait over different work. Each later
// caller can still end its own wait through its context.

// Runtime owns one launch and irreversible shutdown of a configured set of
// background loops.
type Runtime struct {
	mu      sync.Mutex // runtime lifecycle state lock
	enabled bool       // construction time admission flag
	name    string     // unfinished work display name
	// loops is retained from construction and read during Start. Configure it
	// before startup; the runtime shares one context across nonnil callbacks and
	// waits for all of them.
	loops []func(context.Context) // configured background loop callbacks
	// cancel and done are installed together under mu during Start. StopContext
	// snapshots both handles, cancels the shared work and waits on the fixed
	// completion signal.
	cancel context.CancelFunc // shared running context cancellation
	done   chan struct{}      // all launched loops completion
	// stopped, stopDone and stopErr coordinate irreversible shutdown under mu.
	// The first stop publishes a result before closing stopDone, and later calls
	// reuse that result.
	stopped  bool          // permanent admission closure flag
	stopDone chan struct{} // first stop result publication
	stopErr  error         // first shutdown wait result
}

// NewRuntime retains the supplied enable flag, display name and loop
// callbacks for a later Start. The caller keeps the loop slice and callbacks
// stable while the runtime can start work.
func NewRuntime(name string, enabled bool, loops ...func(context.Context)) *Runtime {
	return &Runtime{name: name, enabled: enabled, loops: loops}
}

// NewWorkerRuntime prepares queue consumption, delayed movement and
// stale-active recovery for the supplied worker. Funding recovery is added
// when present; disabled or absent workers produce a runtime whose Start
// launches no loops.
func NewWorkerRuntime(worker *BatchImageWorker, recovery *BillingRecovery, enabled bool) *Runtime {
	var loops []func(context.Context)
	if worker != nil {
		loops = []func(context.Context){worker.Run, worker.RunDelayedMover, worker.RunStaleActiveRecovery}
		if recovery != nil {
			loops = append(loops, func(ctx context.Context) {
				interval := worker.Options().RecoveryInterval
				for {
					if ctx.Err() != nil {
						return
					}
					// Funding recovery records individual release failures and schedules
					// their retries; continue periodic scans so later stale submissions are
					// still considered.
					_, _ = recovery.ReleaseStaleUnsubmittedOnce(ctx)
					SleepOrDone(ctx, interval)
				}
			})
		}
	}
	return NewRuntime("batch image worker", enabled && worker != nil, loops...)
}

// Start launches each nonnil configured loop with one shared background
// cancellation context. Disabled, stopped and already started runtimes return
// immediately. Construction retains the loop slice, so callers finish
// configuring it before Start.
//
// The mutex publishes cancel and a fixed done channel before concurrent stop
// callers can observe them. A separate waiter closes done after every
// launched loop returns, including the empty-loop case, so shutdown can wait
// for the same work even when individual loop callbacks finish independently.
func (r *Runtime) Start() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled || r.stopped || r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	done := make(chan struct{})
	r.done = done
	var wg sync.WaitGroup
	// Publish lifecycle handles before launching callbacks so concurrent
	// StopContext calls cancel and wait for this same set of work.
	for _, loop := range r.loops {
		if loop != nil {
			wg.Add(1)
			go func(run func(context.Context)) { defer wg.Done(); run(ctx) }(loop)
		}
	}
	// The fixed signal closes after every registered callback returns, allowing
	// shutdown callers to wait for one complete runtime invocation.
	go func() { wg.Wait(); close(done) }()
}

// Running reports whether Start installed a cancellation handle and Stop has
// not closed admission. A nil runtime returns false; individual loops may
// already have returned while this admission state remains true.
func (r *Runtime) Running() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancel != nil && !r.stopped
}

// Stop cancels the runtime and waits using a background context. It discards
// the shared stop error, so callers that need a shutdown budget or an
// unfinished-work report use StopContext.
func (r *Runtime) Stop() { _ = r.StopContext(context.Background()) }

// StopContext permanently stops admission and publishes the first caller's
// cancellation and wait result. A nil runtime returns successfully, and
// stopping before Start prevents later startup. Concurrent callers wait on
// stopDone using their own contexts.
//
// The first caller cancels shared work and waits for loop completion. If its
// context wins that wait, the unfinished-work error is retained even as
// cancelled loops later return. After publication, repeated callers with
// active contexts receive that stored result; a cancelled caller can select
// its own context error when both completion and cancellation are ready.
func (r *Runtime) StopContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.stopped {
		done := r.stopDone
		r.mu.Unlock()
		// A repeated caller waits for the first published shutdown result. Its own
		// cancellation ends this wait without replacing that shared result.
		select {
		case <-done:
			return r.stopErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.stopped = true
	r.stopDone = make(chan struct{})
	cancel := r.cancel
	done := r.done
	r.mu.Unlock()
	// Stop snapshots the running handles under the mutex, then cancels outside
	// it so returning callbacks can finish before the shared result is
	// published.
	if cancel != nil {
		cancel()
	}
	var err error
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			err = fmt.Errorf("%s work remains unfinished: %w", r.name, ctx.Err())
		}
	}
	r.mu.Lock()
	// Store the first wait outcome before closing stopDone so repeated callers
	// observe the same published shutdown result.
	r.stopErr = err
	close(r.stopDone)
	r.mu.Unlock()
	return err
}
