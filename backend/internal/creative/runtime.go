package creative

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultCreativeWorkerCount is the default task worker pool size.
const DefaultCreativeWorkerCount = 128

// CreativeWorkerStatus is the pool usage snapshot displayed by the administration API.
type CreativeWorkerStatus struct {
	Running     bool `json:"running"`
	WorkerCount int  `json:"worker_count"`
	BusyWorkers int  `json:"busy_workers"`
}

type creativeWorkerHandle struct {
	id       uint64
	stop     chan struct{}
	stopping atomic.Bool
}

// CreativeWorkerRuntime owns task workers, queue recovery loops, and configured reconcilers.
type CreativeWorkerRuntime struct {
	worker  RuntimeWorker
	options RuntimeOptions

	mu                 sync.Mutex
	stopped            bool
	stopDone           chan struct{}
	stopErr            error
	cancel             context.CancelFunc
	ctx                context.Context
	done               chan struct{}
	wg                 *sync.WaitGroup
	workers            map[uint64]*creativeWorkerHandle
	nextWorkerID       uint64
	desiredWorkerCount int
}

// RuntimeWorker supplies task execution, queue recovery, and busy-worker counts to the runtime.
type RuntimeWorker interface {
	RunUntilStopped(context.Context, <-chan struct{})
	RunDelayedMover(context.Context)
	RunStaleActiveRecovery(context.Context)
	BusyCount() int
}

// RuntimeOptions supplies the enable flag, worker-count reader, and optional outbox/transient recovery loops.
type RuntimeOptions struct {
	Enabled     bool
	WorkerCount func(context.Context) int
	Outbox      func(context.Context)
	Transient   func(context.Context)
}

func NewCreativeWorkerRuntime(worker RuntimeWorker, options RuntimeOptions) *CreativeWorkerRuntime {
	return &CreativeWorkerRuntime{worker: worker, options: options, desiredWorkerCount: DefaultCreativeWorkerCount}
}

// Start starts the worker pool and recovery loops once; a stopped runtime cannot restart.
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
	go func() {
		wg.Wait()
		close(done)
	}()
}

// desiredWorkerCountValue prefers a positive configured count, then the stored desired count, then the default.
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

// SetWorkerCount resizes the pool; scale-down drains selected workers after their current task.
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

// WorkerCount returns the desired pool size, including workers not yet started.
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

// Status returns pool usage, or a zero snapshot while the runtime is stopped.
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

// reconcileWorkersLocked adjusts active workers toward the desired count. The caller holds mu.
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
	// Drain the newest workers first so earlier workers remain available.
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

// activeWorkerCountLocked counts workers not signaled to drain. The caller holds mu.
func (r *CreativeWorkerRuntime) activeWorkerCountLocked() int {
	active := 0
	for _, handle := range r.workers {
		if handle != nil && !handle.stopping.Load() {
			active++
		}
	}
	return active
}

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

// Stop cancels the runtime and waits without a deadline; bounded shutdown uses StopContext.
func (r *CreativeWorkerRuntime) Stop() { _ = r.StopContext(context.Background()) }

// StopContext cancels the runtime and caches the first stop result; a timeout may leave work in flight.
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

// Running reports whether the runtime has started and has not been stopped.
func (r *CreativeWorkerRuntime) Running() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancel != nil && !r.stopped
}
