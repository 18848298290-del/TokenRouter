package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// runtime.go tracks scheduler workers and request operations through startup,
// admission, and bounded shutdown.
// Scheduler workers and request operations share a shutdown context and
// admission lock. Start registers each worker before launching it, while Enter
// registers request work before returning its context. Stop closes that
// admission gate before beginning its wait, so later operations cannot extend
// the shutdown workload.
//
// Periodic tasks run sequentially within one worker, and request operation
// completion remains the caller responsibility. Stop cancels the shared
// context and waits within its caller budget, retaining the first shutdown
// result for repeated calls. A callback that ignores cancellation can remain
// active after a timeout; task names are reported from the active-count
// snapshot, and the runtime cannot be restarted after stopping.

// ErrRuntimeStopped identifies request operations rejected after the scheduler
// runtime closes admission permanently.
var ErrRuntimeStopped = errors.New("scheduler runtime stopped")

// RuntimeTask describes one-shot or periodic work launched by Start.
type RuntimeTask struct {
	Name string // shutdown diagnostic task name
	// Interval and Immediate control the first execution. A positive
	// Interval creates periodic ticks and Immediate requests one initial
	// run; a nonpositive Interval runs once regardless of Immediate.
	Interval  time.Duration // periodic callback interval
	Immediate bool          // run before first tick
	// Run receives the shared runtime context and executes serially for
	// this task. Nil callbacks are skipped; callbacks need to return for
	// shutdown waiting to complete.
	Run func(context.Context) // worker operation callback
}

// WorkerRuntime coordinates worker startup, cancellation, and shutdown waiting. Its zero value is ready to use.
type WorkerRuntime struct {
	// mu protects ctx, cancel, started, stopped, and active. Start and
	// Enter register wg work under this same lock before Stop closes
	// admission and starts waiting. stopOnce publishes stopErr after the
	// first stop execution.
	mu      sync.Mutex         // protects runtime admission state
	ctx     context.Context    // shared shutdown cancellation context
	cancel  context.CancelFunc // shared context cancellation function
	started bool               // worker startup has occurred
	stopped bool               // new admission is closed
	active  map[string]int     // registered counts by task name
	wg      sync.WaitGroup     // tracks registered work completion
	// stopOnce serializes the first shutdown and publishes stopErr to
	// later callers. A timeout result remains retained after work
	// eventually finishes, so subsequent stops return that result.
	stopOnce sync.Once // serializes first stop execution
	stopErr  error     // retained first shutdown result
}

// initializeLocked creates the shared cancellation context and active-count
// map on first use. The caller holds mu; later calls retain the same context,
// including its canceled state after stopping.
func (r *WorkerRuntime) initializeLocked() {
	if r.ctx == nil {
		r.ctx, r.cancel = context.WithCancel(context.Background())
		r.active = make(map[string]int)
	}
}

// Context returns the shared runtime context, initializing it before Start if
// needed. Stop cancels this context permanently; consumers can use it to
// observe shutdown even when workers have not started.
func (r *WorkerRuntime) Context() context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initializeLocked()
	return r.ctx
}

// Start launches the supplied non-nil tasks on the first accepted call.
// Repeated starts and starts after Stop do nothing.
//
// Each task is registered under the admission mutex before its goroutine
// launches, so shutdown waiting includes the worker even when execution has
// not begun. Duplicate task names share an active count for diagnostics. A
// nonpositive interval produces one execution, while periodic workers use Run
// and Immediate according to RuntimeTask and remain tracked until their worker
// returns.
func (r *WorkerRuntime) Start(tasks ...RuntimeTask) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initializeLocked()
	if r.started || r.stopped {
		return
	}
	r.started = true
	for _, task := range tasks {
		if task.Run == nil {
			continue
		}
		// Register before launching while the admission lock is held.
		// Stop can then close the gate and wait without racing a new
		// zero-to-positive wait-group transition.
		r.wg.Add(1)
		r.active[task.Name]++
		go r.run(task)
	}
}

// run executes one registered task until its one-shot work finishes or runtime
// cancellation is observed. It owns active-count and wait-group completion for
// that worker.
//
// The ticker is created before optional immediate execution, so a slow first
// run can leave a tick ready for the next execution. Periodic callbacks
// execute sequentially, and cancellation is rechecked after receiving a tick.
// A callback already running must observe its context or otherwise return; the
// loop cannot interrupt that callback itself.
func (r *WorkerRuntime) run(task RuntimeTask) {
	// Return diagnostic registration before signaling wait-group
	// completion. Shutdown can report the remaining task names while other
	// workers are still active.
	defer func() {
		r.mu.Lock()
		r.active[task.Name]--
		r.mu.Unlock()
		r.wg.Done()
	}()
	ctx := r.Context()
	if ctx.Err() != nil {
		return
	}
	// Create periodic timing before immediate execution. A slow first
	// callback can leave a tick ready, and the worker still runs
	// subsequent callbacks sequentially.
	var ticks <-chan time.Time
	if task.Interval > 0 {
		ticker := time.NewTicker(task.Interval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	if task.Immediate || task.Interval <= 0 {
		task.Run(ctx)
	}
	if task.Interval <= 0 {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if ctx.Err() != nil {
				return
			}
			task.Run(ctx)
		}
	}
}

// StopContext permanently closes admission, cancels shared work, and waits
// using the supplied non-nil shutdown context. The first call determines the
// retained result; later calls wait for and return that same result.
//
// Admission closes under the mutex used by Start and Enter before waiting
// for registered work. If the shutdown context ends first, active task names
// are sampled and sorted, with an error retained when names remain. Work can
// continue after that timeout until its callbacks return; a later StopContext
// does not replace the first result with a fresh wait.
func (r *WorkerRuntime) StopContext(ctx context.Context) error {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.initializeLocked()
		// Close admission before signaling cancellation. An operation
		// awakened by shutdown then sees the stopped gate before it can
		// register replacement work during draining.
		r.stopped = true
		r.cancel()
		r.mu.Unlock()
		// Admission has closed before starting the wait. The waiter
		// closes done when registered work finishes, even if the
		// caller budget ends earlier.
		done := make(chan struct{})
		go func() { r.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			r.mu.Lock()
			var pending []string
			for name, count := range r.active {
				if count > 0 {
					pending = append(pending, name)
				}
			}
			r.mu.Unlock()
			if len(pending) > 0 {
				sort.Strings(pending)
				r.stopErr = fmt.Errorf("scheduler stop unfinished [%s]: %w", strings.Join(pending, ", "), ctx.Err())
			}
		}
	})
	return r.stopErr
}

// Enter registers an operation and returns its cancellation context and
// idempotent completion callback. Nil parent uses a background context, and
// stopped runtimes return ErrRuntimeStopped.
//
// Registration joins the wait group under the admission lock before shutdown
// can begin waiting. The operation context follows parent cancellation and is
// also canceled through the shared runtime context. Call done after releasing
// any acquired resources, because done ends shutdown tracking as well as
// detaching the cancellation callback. Forgetting it leaves the named
// operation active even after its context has been canceled.
func (r *WorkerRuntime) Enter(parent context.Context, name string) (context.Context, func(), error) {
	// Count the operation before exposing its context. If shutdown starts
	// while the cancellation link is being installed, its wait still
	// includes this caller-owned completion.
	r.mu.Lock()
	r.initializeLocked()
	if r.stopped {
		r.mu.Unlock()
		return nil, nil, ErrRuntimeStopped
	}
	r.wg.Add(1)
	r.active[name]++
	runtimeContext := r.ctx
	r.mu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	// Link runtime shutdown to this operation without changing the parent
	// context. The returned done callback detaches this link before ending
	// registration, even after cancellation has fired.
	stop := context.AfterFunc(runtimeContext, cancel)
	var once sync.Once
	done := func() {
		once.Do(func() {
			stop()
			cancel()
			r.mu.Lock()
			r.active[name]--
			r.mu.Unlock()
			r.wg.Done()
		})
	}
	return ctx, done, nil
}
