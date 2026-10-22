package timingwheel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/zeromicro/go-zero/core/collection"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// wheel.go schedules named callbacks and coordinates replacement,
// cancellation, and shutdown.
// Named callbacks can be registered before the timing wheel starts and can
// outlive cancellation once execution has begun. Each registration gets a
// distinct task pointer; dispatch and rearming compare that pointer with the
// current name entry, preventing an old callback from rearming over a
// replacement that reused its name. Recurring delay starts again after
// callback completion.
//
// A single mutex protects admission, identity checks, and terminal shutdown
// state. Execution registers both per-task and wheel-wide wait counts before
// unlocking, while cancellation removes the task before waiting and shutdown
// rejects future admission before waiting for active callbacks. Context
// cancellation bounds shutdown waiting, leaving admitted callback work
// running until its own function returns.

// newTimingWheel selects the underlying constructor; serial failure and timing
// fixtures replace it and restore it before another test uses this dependency.
var newTimingWheel = collection.NewTimingWheel

// task identifies one named callback registration and its admitted execution count.
type task struct {
	name      string        // registered timer lookup name
	delay     time.Duration // delay after registration or completion
	recurring bool          // rearm after callback completion
	// fn runs outside the wheel mutex and may continue after cancellation.
	// Callers provide any cancellation needed by the callback's own work.
	fn func()
	// wg counts callbacks admitted for this task under Wheel.mu before a
	// cancellation removes its identity; CancelAndWait waits after that removal.
	wg sync.WaitGroup
}

// Wheel schedules named callbacks after Start. Task identity prevents canceled
// callbacks from rearming over a replacement with the same name.
type Wheel struct {
	// mu coordinates tw, tasks, stopped, wg admission, and stopDone publication.
	// Execution registers its counts before unlocking; terminal shutdown closes
	// admission before starting the shared completion wait.
	mu       sync.Mutex              // lifecycle and admission mutex
	tw       *collection.TimingWheel // started timer dispatch dependency
	tasks    map[string]*task        // current registration by name
	stopped  bool                    // terminal admission rejection flag
	wg       sync.WaitGroup          // wheel-wide admitted callback count
	stopDone chan struct{}           // shared shutdown completion signal
}

// New constructs an unstarted wheel with an empty named task map.
func New() *Wheel { return &Wheel{tasks: make(map[string]*task)} }

// Start creates a one-second, 3600-slot timing wheel and arms queued task
// registrations. Repeated calls after successful start reuse it; after
// shutdown, Start returns an error. Timer construction failure leaves the
// wheel available for another Start attempt.
func (w *Wheel) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return fmt.Errorf("timing wheel already stopped")
	}
	if w.tw != nil {
		return nil
	}
	tw, err := newTimingWheel(time.Second, 3600, func(_ any, value any) {
		if fn, ok := value.(func()); ok {
			fn()
		}
	})
	if err != nil {
		return fmt.Errorf("failed to create timing wheel: %w", err)
	}
	w.tw = tw
	for _, t := range w.tasks {
		w.arm(t)
	}
	logging.LegacyPrintf("service.timing_wheel", "%s", "[TimingWheel] Started")
	return nil
}

// Schedule registers a one-shot fn under name, replacing the current
// registration with that name. Before Start it queues the task; after
// shutdown it logs rejection. Replacement does not wait for any already
// active old callback.
func (w *Wheel) Schedule(name string, delay time.Duration, fn func()) {
	w.schedule(name, delay, fn, false)
}

// ScheduleRecurring registers fn to run again after each completed callback
// and delay. It replaces the current named registration, queues before Start,
// and logs rejection after shutdown; already active replaced work can still
// finish.
func (w *Wheel) ScheduleRecurring(name string, delay time.Duration, fn func()) {
	w.schedule(name, delay, fn, true)
}

// schedule replaces the named task registration under the wheel mutex and
// arms it when started. Each replacement receives a distinct task pointer, so
// previously dispatched or active callbacks cannot remove or rearm the
// replacement.
//
// A stopped wheel logs rejection and keeps terminal state. Timer errors are
// logged while the task remains registered; invalid delays are delegated to
// the underlying wheel's SetTimer validation. Replacing a task does not stop
// an already running function, and callers use cancellation or shutdown
// waiting when they need completion.
func (w *Wheel) schedule(name string, delay time.Duration, fn func(), recurring bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		logging.LegacyPrintf("service.timing_wheel", "[TimingWheel] SetTimer failed for %q: stopped", name)
		return
	}
	// Publish a fresh registration pointer before arming its timer, so a
	// previously dispatched callback fails the identity check even when it shares this name.
	t := &task{name: name, delay: delay, fn: fn, recurring: recurring}
	w.tasks[name] = t
	if w.tw != nil {
		w.arm(t)
	}
}

// arm submits t for asynchronous dispatch through the started underlying
// wheel. The caller holds w.mu and supplies w.tw; SetTimer errors are logged
// while the task registration remains available to its owner.
func (w *Wheel) arm(t *task) {
	// Failed timer submission leaves the named registration in tasks. Report
	// the failure here because the public scheduling calls return no admission error.
	if err := w.tw.SetTimer(t.name, func() { w.execute(t) }, t.delay); err != nil {
		logging.LegacyPrintf("service.timing_wheel", "[TimingWheel] SetTimer failed for %q: %v", t.name, err)
	}
}

// execute admits t only if it is still the current named registration and the
// wheel is running. It registers both wait counts under the mutex before
// invoking fn outside that mutex; nil functions simply complete.
//
// After execution, a second identity and shutdown check decides whether to
// rearm recurring work or remove the one-shot registration. Cancellation or
// replacement during fn therefore prevents this old task from changing a
// successor's timer. Deferred wait-count releases still run when fn panics,
// but normal rearming and removal are skipped.
func (w *Wheel) execute(t *task) {
	w.mu.Lock()
	if w.stopped || w.tasks[t.name] != t {
		w.mu.Unlock()
		return
	}
	// Register active work before releasing admission's lock so cancellation
	// and shutdown cannot begin waiting before this accepted callback is counted.
	w.wg.Add(1)
	t.wg.Add(1)
	w.mu.Unlock()
	defer w.wg.Done()
	defer t.wg.Done()
	if t.fn != nil {
		t.fn()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.tasks[t.name] != t {
		return
	}
	// The callback can cancel or replace its registration while running. The
	// preceding identity check keeps rearming or deletion tied to this same task.
	if t.recurring {
		w.arm(t)
	} else {
		delete(w.tasks, t.name)
	}
}

// cancel removes name's current registration under the wheel mutex and
// attempts to remove its timer before returning the task pointer. A
// dispatched callback must pass execute's identity check, while already
// admitted work retains its wait count.
//
// Removal errors are discarded because deleting the task identity prevents a
// later dispatch from entering normal execution. A returned task identifies
// the registration present at cancellation, allowing CancelAndWait to wait
// for that task; replacements scheduled afterward have separate identity and
// execution counts.
func (w *Wheel) cancel(name string) *task {
	w.mu.Lock()
	defer w.mu.Unlock()
	t := w.tasks[name]
	// Invalidate identity before timer removal; an already dispatched callback
	// then fails execute's admission check even if RemoveTimer cannot remove it.
	delete(w.tasks, name)
	if w.tw != nil && !w.stopped {
		_ = w.tw.RemoveTimer(name)
	}
	return t
}

// Cancel removes the current registration for name and prevents it from
// rearming. Already admitted callbacks may still finish; callers needing
// their completion use CancelAndWait rather than assuming removal stops
// active work.
func (w *Wheel) Cancel(name string) { w.cancel(name) }

// CancelAndWait removes name's current registration and waits for callbacks
// already admitted for that task. Calling it from the same callback deadlocks
// on its own count; a later replacement with the same name is a separate
// registration.
func (w *Wheel) CancelAndWait(name string) {
	if t := w.cancel(name); t != nil {
		t.wg.Wait()
	}
}

// Stop performs terminal shutdown and waits for active callbacks using
// background context. It can be repeated, but an active callback calling Stop
// cannot complete while shutdown waits for that callback.
func (w *Wheel) Stop() { _ = w.Shutdown(context.Background()) }

// Shutdown permanently rejects scheduling and waits for admitted callbacks,
// returning ctx's error if its wait budget ends. Pass a non-nil context. The
// first call stops the underlying wheel, clears queued task identities, and
// starts one wait goroutine after admission closes under the mutex.
//
// Later calls share the same completion channel and can wait again after a
// timeout. Cancellation affects waiting rather than callback execution;
// callbacks retain responsibility for their own cancellation and return.
// Calling from an admitted callback waits on its own outstanding count.
func (w *Wheel) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	if w.stopDone == nil {
		// Close admission before starting Wait, so no accepted execution can add
		// a new wait count after shutdown has begun sharing its completion signal.
		w.stopped = true
		w.stopDone = make(chan struct{})
		if w.tw != nil {
			w.tw.Stop()
		}
		clear(w.tasks)
		go func() { w.wg.Wait(); close(w.stopDone) }()
	}
	done := w.stopDone
	w.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
