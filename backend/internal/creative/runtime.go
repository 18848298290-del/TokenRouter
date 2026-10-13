package creative

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Creative workers share one runtime context but have separate stop channels
// for pool resizing. Scaling down signals the newest active handles to drain
// after their current task, while global shutdown cancels the shared context
// and prevents restart. Draining handles remain tracked until their queue loop
// exits, although the status active count excludes handles already signaled to
// stop.
//
// The runtime mutex coordinates startup, desired count, handle publication,
// and shutdown state. A wait group covers worker handles and recovery loops,
// and done closes after all registered work exits. The first StopContext waits
// with its caller deadline and publishes a cached result through stopDone;
// later callers wait for that publication or their own context. A timed-out
// first stop can therefore report unfinished work while the goroutines
// continue unwinding.

// DefaultCreativeWorkerCount supplies the target task pool size when startup
// settings or stored desired counts do not provide a positive worker count.
const DefaultCreativeWorkerCount = 128

// CreativeWorkerStatus is the pool usage snapshot displayed by the administration API.
type CreativeWorkerStatus struct {
	Running     bool `json:"running"`      // runtime started and unstopped flag
	WorkerCount int  `json:"worker_count"` // workers not signaled to drain
	BusyWorkers int  `json:"busy_workers"` // active queue Process calls
}

// creativeWorkerHandle identifies one queue loop and its independently
// signaled drain lifetime.
type creativeWorkerHandle struct {
	id uint64 // monotonically assigned worker identifier
	// stop closes when this handle is selected to drain; stopping records that
	// request before the close, keeping it out of active worker counts.
	stop     chan struct{} // queue loop drain signal
	stopping atomic.Bool   // worker drain requested flag
}

// CreativeWorkerRuntime owns task workers, queue recovery loops, and configured
// reconcilers.
type CreativeWorkerRuntime struct {
	worker  RuntimeWorker  // shared queue processing worker
	options RuntimeOptions // stable runtime startup callbacks

	// mu protects stopped, stopDone, stopErr, cancel, ctx, done, and wg during
	// lifecycle publication. The same mutex protects workers, nextWorkerID, and
	// desiredWorkerCount while handles are started, drained, or removed.
	mu      sync.Mutex // runtime lifecycle and pool mutex
	stopped bool       // irreversible runtime stop marker
	// stopDone and stopErr publish the first shutdown result. Writes hold mu;
	// later StopContext readers wait for this channel close before reading the
	// cached error outside the mutex.
	stopDone chan struct{}      // first stop result publication signal
	stopErr  error              // cached first shutdown result
	cancel   context.CancelFunc // shared runtime context cancellation
	ctx      context.Context    // shared worker and recovery context
	// all registered goroutines finished signal
	done chan struct{}
	wg   *sync.WaitGroup // worker and recovery completion counter
	// tracked running and draining handles
	workers            map[uint64]*creativeWorkerHandle
	nextWorkerID       uint64 // latest assigned worker identifier
	desiredWorkerCount int    // configured future active pool size
}

// RuntimeWorker supplies task execution, queue recovery, and busy-worker counts
// to the runtime.
type RuntimeWorker interface {
	// RunUntilStopped processes queued work under ctx until cancellation or the
	// supplied drain signal. A drain permits current work to finish and returns a
	// reservation acquired after that signal to ready.
	RunUntilStopped(context.Context, <-chan struct{})
	// RunDelayedMover polls due delayed work until the runtime context is
	// cancelled. The runtime owns goroutine completion and waits for this method
	// before publishing its all-work-finished signal.
	RunDelayedMover(context.Context)
	// RunStaleActiveRecovery polls abandoned active work until the runtime
	// context is cancelled. The runtime waits for this recovery loop along with
	// worker and other reconciler goroutines during shutdown.
	RunStaleActiveRecovery(context.Context)
	// BusyCount returns the shared worker current count of queue-driven Process
	// calls.
	BusyCount() int
}

// RuntimeOptions supplies the enable flag, worker-count reader, and optional
// outbox/transient recovery loops.
type RuntimeOptions struct {
	Enabled bool // queue runtime startup enable flag
	// optional startup worker count reader
	// WorkerCount is read during startup under a separate two-second context.
	// Nonpositive results use the stored desired count before the runtime default.
	// The callback observes cancellation itself to bound startup read time.
	WorkerCount func(context.Context) int
	// Outbox runs recovery under the shared runtime context when configured.
	// A nil callback completes its registered goroutine immediately; configured
	// loops return after that context is cancelled.
	Outbox func(context.Context) // optional outbox recovery loop
	// Transient runs terminal byte cleanup under the shared runtime context.
	// A nil callback completes immediately; configured loops return on cancellation
	// so runtime shutdown can join their registered work.
	Transient func(context.Context) // optional terminal byte cleanup loop
}

// NewCreativeWorkerRuntime retains a worker and stable startup options with
// the default desired count. It starts no goroutines until Start, and callers
// can set a positive desired count beforehand.
func NewCreativeWorkerRuntime(worker RuntimeWorker, options RuntimeOptions) *CreativeWorkerRuntime {
	return &CreativeWorkerRuntime{worker: worker, options: options, desiredWorkerCount: DefaultCreativeWorkerCount}
}

// Start publishes the shared context, completion signals, and desired pool
// before starting worker handles and recovery loops. Nil workers, disabled
// options, prior startup, and an already stopped runtime return directly.
//
// Worker count is read before the lifecycle mutex is acquired, and the lock
// then prevents duplicate startup or concurrent stop publication. Four
// recovery goroutines are registered even when optional callbacks are absent,
// while the handle reconciler adds the desired workers. A separate waiter
// closes done only after the registered wait group reaches completion; a
// stopped runtime cannot restart.
func (r *CreativeWorkerRuntime) Start() {
	if r == nil || r.worker == nil || !r.options.Enabled {
		return
	}

	desired := r.desiredWorkerCountValue()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil || r.stopped {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.ctx = ctx
	r.done = make(chan struct{})
	r.wg = &sync.WaitGroup{}
	r.workers = make(map[uint64]*creativeWorkerHandle)
	r.desiredWorkerCount = desired
	// Register recovery loops before launching them so the done waiter cannot
	// publish completion while startup is still adding the initial worker pool.
	// Their shared context ends with global runtime shutdown.
	r.wg.Add(4)
	go func() {
		defer r.wg.Done()
		r.worker.RunDelayedMover(ctx)
	}()
	go func() {
		defer r.wg.Done()
		r.worker.RunStaleActiveRecovery(ctx)
	}()
	go func() {
		defer r.wg.Done()
		if r.options.Outbox != nil {
			r.options.Outbox(ctx)
		}
	}()
	go func() {
		defer r.wg.Done()
		if r.options.Transient != nil {
			r.options.Transient(ctx)
		}
	}()
	r.reconcileWorkersLocked()
	wg := r.wg
	done := r.done
	// Capture this startup wait group and completion channel for the waiter.
	// Close done after registered workers and recovery loops finish so shutdown
	// waits on completed work rather than just cancellation publication.
	go func() {
		wg.Wait()
		close(done)
	}()
}

// desiredWorkerCountValue reads an optional configured count under a
// two-second context, then the stored desired count under mu. Only positive
// results are used; otherwise DefaultCreativeWorkerCount supplies the startup
// target.
func (r *CreativeWorkerRuntime) desiredWorkerCountValue() int {
	if r != nil && r.options.WorkerCount != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		count := r.options.WorkerCount(ctx)
		cancel()
		if count > 0 {
			return count
		}
	}

	if r != nil {
		r.mu.Lock()
		desired := r.desiredWorkerCount
		r.mu.Unlock()
		if desired > 0 {
			return desired
		}
	}
	return DefaultCreativeWorkerCount
}

// SetWorkerCount changes the desired pool size and reconciles a live runtime
// under mu. Nil receivers and nonpositive counts return directly, and stopped
// runtimes retain their completed lifecycle state.
//
// Scale-up registers and starts handles immediately; scale-down closes
// selected handle stop channels after setting their stopping markers. Those
// loops drain current processing rather than cancelling the shared context. A
// pre-start change updates the target for later startup, and a draining handle
// is removed only when its queue loop finishes.
func (r *CreativeWorkerRuntime) SetWorkerCount(count int) {
	if r == nil || count <= 0 {
		return
	}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.desiredWorkerCount = count
	if r.cancel != nil && r.ctx != nil && r.ctx.Err() == nil {
		r.reconcileWorkersLocked()
	}
	r.mu.Unlock()
}

// WorkerCount returns the desired pool size under the lifecycle mutex,
// including before startup. Nil runtimes return zero, and a nonpositive stored
// target falls back to the default worker count.
func (r *CreativeWorkerRuntime) WorkerCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.desiredWorkerCount <= 0 {
		return DefaultCreativeWorkerCount
	}
	return r.desiredWorkerCount
}

// Status reports live runtime state, non-draining handle count, and the shared
// worker busy count. Stopped or unconfigured runtimes return zero status; busy
// draining workers can remain counted until their Process call exits.
func (r *CreativeWorkerRuntime) Status() CreativeWorkerStatus {
	if r == nil || r.worker == nil {
		return CreativeWorkerStatus{}
	}
	r.mu.Lock()
	running := r.cancel != nil && !r.stopped
	workerCount := 0
	if running {
		workerCount = r.activeWorkerCountLocked()
	}
	r.mu.Unlock()
	if !running {
		return CreativeWorkerStatus{Running: false}
	}
	return CreativeWorkerStatus{
		Running:     true,
		WorkerCount: workerCount,
		BusyWorkers: r.worker.BusyCount(),
	}
}

// reconcileWorkersLocked adjusts the live handle set toward its positive
// desired count while the caller holds mu. It returns when startup state is
// unavailable or the shared context is cancelled.
//
// Scale-up registers each handle and wait-group contribution before launching
// its loop. Scale-down visits identifiers newest first, marking a handle
// stopping before closing its channel so subsequent counts exclude it. The
// handles remain stored while draining, allowing runWorkerHandle to remove
// each completed loop and reconcile any remaining gap under the same mutex.
func (r *CreativeWorkerRuntime) reconcileWorkersLocked() {
	if r == nil || r.cancel == nil || r.ctx == nil || r.ctx.Err() != nil || r.wg == nil {
		return
	}
	desired := r.desiredWorkerCount
	if desired <= 0 {
		desired = DefaultCreativeWorkerCount
	}
	activeCount := r.activeWorkerCountLocked()
	for activeCount < desired {
		r.nextWorkerID++
		handle := &creativeWorkerHandle{id: r.nextWorkerID, stop: make(chan struct{})}
		r.workers[handle.id] = handle
		r.wg.Add(1)
		go r.runWorkerHandle(r.ctx, handle)
		activeCount++
	}
	if activeCount <= desired {
		return
	}
	// Choose newest active handles first for drain. Mark each before closing its
	// stop channel so another reconciliation counts that pending exit only once
	// and leaves earlier handles available for new reservations.
	toStop := activeCount - desired
	for id := r.nextWorkerID; toStop > 0 && id > 0; id-- {
		handle, ok := r.workers[id]
		if !ok || handle.stopping.Load() {
			continue
		}
		handle.stopping.Store(true)
		close(handle.stop)
		toStop--
	}
}

// activeWorkerCountLocked counts tracked handles whose drain marker has not
// been set. Callers hold mu while reading the map; drained but unfinished
// loops are excluded from the returned active target count.
func (r *CreativeWorkerRuntime) activeWorkerCountLocked() int {
	active := 0
	for _, handle := range r.workers {
		if handle != nil && !handle.stopping.Load() {
			active++
		}
	}
	return active
}

// runWorkerHandle runs one queue loop and removes its handle after that loop
// returns. Its wait-group contribution ends only after removal and any live
// pool reconciliation completes.
//
// Removal and reconciliation share mu so concurrent resizing sees a consistent
// handle map. A live context can start replacement handles when desired
// capacity is still unmet; cancellation prevents replacement during shutdown.
// The worker receives the per-handle stop channel for draining and the shared
// runtime context for global cancellation.
func (r *CreativeWorkerRuntime) runWorkerHandle(ctx context.Context, handle *creativeWorkerHandle) {
	defer r.wg.Done()
	r.worker.RunUntilStopped(ctx, handle.stop)
	r.mu.Lock()
	delete(r.workers, handle.id)
	if r.cancel != nil && r.ctx != nil && r.ctx.Err() == nil {
		r.reconcileWorkersLocked()
	}
	r.mu.Unlock()
}

// Stop cancels runtime work and waits using a background context with no
// deadline. It discards the StopContext result; callers requiring bounded
// shutdown use StopContext with their own timeout.
func (r *CreativeWorkerRuntime) Stop() { _ = r.StopContext(context.Background()) }

// StopContext permanently stops the runtime and waits for registered work
// within the caller context. Nil receivers succeed, while the first caller
// owns cancellation and first-stop result publication.
//
// The stopped marker prevents further startup and resizing before the shared
// context is cancelled outside mu. A deadline can leave goroutines unwinding
// and becomes the cached first stop error. stopDone closes after that result
// is stored. Later callers wait for publication or their own cancellation, so
// their deadline can return independently of the cached first result.
func (r *CreativeWorkerRuntime) StopContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.stopped {
		done := r.stopDone
		r.mu.Unlock()
		select {
		case <-done:
			return r.stopErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Publish irreversible stop state under mu before cancelling outside it.
	// Concurrent callers then wait on stopDone rather than starting another
	// cancellation-and-result publisher for the same runtime.
	r.stopped = true
	r.stopDone = make(chan struct{})
	cancel := r.cancel
	done := r.done
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	var err error
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			err = fmt.Errorf("creative workers remain unfinished: %w", ctx.Err())
		}
	}
	r.mu.Lock()
	r.stopErr = err
	close(r.stopDone)
	r.mu.Unlock()
	return err
}

// Running reports whether startup installed cancellation and the runtime has
// not been stopped. Nil runtimes report false, and the lifecycle mutex
// protects the read against concurrent start or stop.
func (r *CreativeWorkerRuntime) Running() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancel != nil && !r.stopped
}
