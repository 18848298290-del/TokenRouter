package ws

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// SettingSource reads stored settings in batches, omitting missing keys from the returned map.
type SettingSource interface {
	GetMultiple(context.Context, []string) (map[string]string, error)
}

// Runtime owns effective settings and refresh work; publication generations prevent stale reads overwriting saved values.
type Runtime struct {
	defaults   Parameters
	current    atomic.Pointer[Parameters]
	source     SettingSource
	mu         sync.Mutex
	generation uint64
	apply      func(Parameters) error
	report     func(error)
	cancel     context.CancelFunc
	done       chan struct{}
	closed     bool
}

// NewRuntime initializes settings without starting a refresh worker.
func NewRuntime(defaults Parameters, source SettingSource, report func(error)) *Runtime {
	r := &Runtime{defaults: defaults, source: source, report: report}
	r.current.Store(&defaults)
	return r
}

// Snapshot returns effective parameters by value, or built-in defaults for a nil Runtime.
func (r *Runtime) Snapshot() Parameters {
	if r == nil {
		return DefaultParameters()
	}
	return *r.current.Load()
}

// Defaults returns deployment defaults for override merging and reset.
func (r *Runtime) Defaults() Parameters { return r.defaults }

// SetApply binds the pool-update callback during app assembly; the caller creates pools on demand.
func (r *Runtime) SetApply(apply func(Parameters) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apply = apply
}

func (r *Runtime) publishLocked(value Parameters) error {
	if r.closed {
		return errors.New("responses websocket settings are stopped")
	}
	if r.apply != nil {
		if err := r.apply(value); err != nil {
			return err
		}
	}
	r.current.Store(&value)
	return nil
}

// Publish applies validated stored overrides and invalidates older in-flight reads. Call it after persistence succeeds.
func (r *Runtime) Publish(raw string) error {
	value, err := ResolveParameters(r.defaults, raw)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	return r.publishLocked(value)
}

// Refresh reads storage outside the lock and checks the publication generation before applying the result.
func (r *Runtime) Refresh(ctx context.Context) error {
	if r.source == nil {
		return nil
	}
	r.mu.Lock()
	generation, closed := r.generation, r.closed
	r.mu.Unlock()
	if closed {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	stored, err := r.source.GetMultiple(ctx, []string{SettingKey})
	if err != nil {
		return err
	}
	value, err := ResolveParameters(r.defaults, stored[SettingKey])
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.generation != generation {
		return nil
	}
	if value == r.Snapshot() {
		return nil
	}
	r.generation++
	return r.publishLocked(value)
}

// Start reads shared settings immediately and every five seconds; read failures retain the current parameters.
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.closed || r.cancel != nil {
		r.mu.Unlock()
		return nil
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	r.mu.Unlock()
	if err := r.Refresh(ctx); err != nil && r.report != nil {
		r.report(err)
	}
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				if err := r.Refresh(workerCtx); err != nil && r.report != nil {
					r.report(err)
				}
			}
		}
	}()
	return nil
}

// Stop prevents further publication, cancels refresh, and waits for exit or ctx cancellation.
func (r *Runtime) Stop(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
