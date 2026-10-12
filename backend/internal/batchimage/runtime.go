package batchimage

import (
	"context"
	"fmt"
	"sync"
)

type Runtime struct {
	mu       sync.Mutex
	enabled  bool
	name     string
	loops    []func(context.Context)
	cancel   context.CancelFunc
	done     chan struct{}
	stopped  bool
	stopDone chan struct{}
	stopErr  error
}

func NewRuntime(name string, enabled bool, loops ...func(context.Context)) *Runtime {
	return &Runtime{name: name, enabled: enabled, loops: loops}
}

// NewWorkerRuntime prepares queue, delayed-job, stale-job, and funding recovery loops for Start.
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
					_, _ = recovery.ReleaseStaleUnsubmittedOnce(ctx)
					SleepOrDone(ctx, interval)
				}
			})
		}
	}
	return NewRuntime("batch image worker", enabled && worker != nil, loops...)
}

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
	for _, loop := range r.loops {
		if loop != nil {
			wg.Add(1)
			go func(run func(context.Context)) { defer wg.Done(); run(ctx) }(loop)
		}
	}
	go func() { wg.Wait(); close(done) }()
}

func (r *Runtime) Running() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancel != nil && !r.stopped
}
func (r *Runtime) Stop() { _ = r.StopContext(context.Background()) }

// StopContext cancels work and shares the first stop result; a deadline reports unfinished loops.
func (r *Runtime) StopContext(ctx context.Context) error {
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
			err = fmt.Errorf("%s work remains unfinished: %w", r.name, ctx.Err())
		}
	}
	r.mu.Lock()
	r.stopErr = err
	close(r.stopDone)
	r.mu.Unlock()
	return err
}
