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

// ErrRuntimeStopped rejects new operations on a stopped runtime.
var ErrRuntimeStopped = errors.New("scheduler runtime stopped")

// RuntimeTask describes one-shot or periodic work launched by Start.
type RuntimeTask struct {
	Name      string
	Interval  time.Duration
	Immediate bool
	Run       func(context.Context)
}

// WorkerRuntime coordinates worker startup, cancellation, and shutdown waiting. Its zero value is ready to use.
type WorkerRuntime struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	started  bool
	stopped  bool
	active   map[string]int
	wg       sync.WaitGroup
	stopOnce sync.Once
	stopErr  error
}

func (r *WorkerRuntime) initializeLocked() {
	if r.ctx == nil {
		r.ctx, r.cancel = context.WithCancel(context.Background())
		r.active = make(map[string]int)
	}
}

// Context returns the shared runtime context, even before Start. Stop cancels it permanently.
func (r *WorkerRuntime) Context() context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initializeLocked()
	return r.ctx
}

// Start launches tasks once and registers them before shutdown can begin waiting.
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
		r.wg.Add(1)
		r.active[task.Name]++
		go r.run(task)
	}
}

func (r *WorkerRuntime) run(task RuntimeTask) {
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
	// Start the ticker before the immediate run so an elapsed tick is ready after a slow first run.
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

// StopContext cancels work and saves its first result, reporting unfinished task names if the caller's context ends.
func (r *WorkerRuntime) StopContext(ctx context.Context) error {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.initializeLocked()
		r.stopped = true
		r.cancel()
		r.mu.Unlock()
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

// Enter registers an operation for shutdown waiting. The caller calls done after releasing any acquired resources.
func (r *WorkerRuntime) Enter(parent context.Context, name string) (context.Context, func(), error) {
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
