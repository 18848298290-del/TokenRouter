package ws

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// A settings refresh can begin before an administrator saves and publishes a
// newer override object. Returning storage bytes from that earlier read must
// not replace the locally published settings used by new connections and turns.
//
// Runtime copies each effective Parameters value behind an atomic pointer while
// a mutex serializes generations, apply callbacks, and shutdown state. Refresh
// reads storage outside that mutex and checks its captured generation before
// publication. Publish advances the generation after validation and before
// applying, so even an apply failure invalidates older refreshes. The snapshot
// changes only after the callback succeeds; callback side effects are owned by
// that callback and are not rolled back here. Stop closes publication before
// canceling and joining the refresh worker.

// SettingSource supplies stored override values to the WebSocket runtime
// refresh worker.
type SettingSource interface {
	// GetMultiple reads requested settings under the supplied context and omits
	// missing keys. Implementations honor cancellation for refresh shutdown;
	// Runtime supplies a two-second deadline and resolves an omitted SettingKey
	// against its deployment defaults.
	GetMultiple(context.Context, []string) (map[string]string, error)
}

// Runtime publishes effective WebSocket parameters and coordinates
// generation-checked storage refresh work.
type Runtime struct {
	defaults Parameters // deployment parameter baseline value
	// current points to a copied effective value. Snapshot returns that value by
	// copy, so callers cannot mutate the published parameters through the returned
	// result.
	current atomic.Pointer[Parameters] // immutable effective parameters pointer
	source  SettingSource              // optional stored settings reader
	// mu protects generation, apply, cancel, done, and closed. Apply callbacks run
	// while this lock is held; storage reads and failure reporting execute outside
	// it.
	mu sync.Mutex // publication and lifecycle lock
	// generation advances before each accepted publication attempt, including
	// callback failure. Refresh discards results whose captured generation differs
	// from this later attempt.
	generation uint64 // latest publication attempt generation
	// apply runs before current is replaced and must not reenter mutex-taking
	// Runtime methods. An error retains the current pointer; callback side effects
	// have no rollback here.
	apply  func(Parameters) error // optional parameter application callback
	report func(error)            // refresh failure observation callback
	// cancel and done are initialized together by Start. The worker uses its own
	// Background-derived context; Stop cancels it and waits with the caller's stop
	// budget.
	cancel context.CancelFunc // refresh worker cancellation function
	done   chan struct{}      // refresh worker completion channel
	// closed is permanent after Stop. Publish then fails, while Refresh and Start
	// return without scheduling further publication or creating another worker.
	closed bool // publication permanently stopped flag
}

// NewRuntime retains defaults, optional source, and failure reporter without
// starting refresh work. The caller supplies usable deployment defaults because
// construction publishes them directly; later stored overrides are validated
// before replacing them.
func NewRuntime(defaults Parameters, source SettingSource, report func(error)) *Runtime {
	r := &Runtime{defaults: defaults, source: source, report: report}
	r.current.Store(&defaults)
	return r
}

// Snapshot returns a value copy of the atomically published parameters. A nil
// Runtime returns built-in defaults; nonnil instances must be constructed by
// NewRuntime so the initial pointer is available.
func (r *Runtime) Snapshot() Parameters {
	if r == nil {
		return DefaultParameters()
	}
	return *r.current.Load()
}

// Defaults returns the deployment parameter baseline used to resolve stored
// overrides.
func (r *Runtime) Defaults() Parameters { return r.defaults }

// SetApply replaces the pool-application callback under the publication mutex.
// The callback runs inside that lock before snapshot replacement and must not
// call mutex-taking Runtime methods; Snapshot remains an atomic read.
func (r *Runtime) SetApply(apply func(Parameters) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apply = apply
}

// publishLocked applies value and replaces the current pointer after
// application succeeds. The caller holds mu; a stopped runtime or callback
// error returns before replacing the effective snapshot, without rolling back
// callback side effects.
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

// Publish validates stored overrides and attempts to publish their effective
// parameters after persistence. Invalid raw returns before changing publication
// state.
//
// With mu held, the generation advances before the apply callback and atomic
// snapshot store. This makes older storage reads stale even when applying the
// saved value fails. Callback success replaces current, callback failure
// retains its earlier value, and Stop rejects publication. Callers report
// persisted-but-unapplied settings using this error; Runtime has no database
// rollback or callback compensation step.
func (r *Runtime) Publish(raw string) error {
	value, err := ResolveParameters(r.defaults, raw)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Advance before applying persisted settings so every older storage read is
	// discarded even if the application callback fails to replace the snapshot.
	r.generation++
	return r.publishLocked(value)
}

// Refresh reads storage and publishes a changed effective value when its
// generation is still current. A missing source, stopped runtime, or stale read
// returns nil, and storage or validation failures retain the current snapshot.
//
// The caller provides a nonnil context, given a two-second deadline for the
// source read. The source implements read cancellation. Reads run outside mu
// so local Publish can progress; publication reacquires mu
// and compares the captured generation before applying. An unchanged value
// needs no callback. A changed value advances generation before applying, so
// callback failure also invalidates older concurrent reads while leaving the
// effective pointer available.
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
	// Give storage a two-second context deadline. The source must honor its
	// cancellation for this synchronous read to finish within that request budget.
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
	// Discard a result read before a newer publish attempt or shutdown, retaining
	// the locally chosen snapshot even when storage returned successfully.
	if r.closed || r.generation != generation {
		return nil
	}
	if value == r.Snapshot() {
		return nil
	}
	r.generation++
	return r.publishLocked(value)
}

// Start performs an immediate refresh and creates one independent periodic
// refresh worker. Repeated starts and starts after Stop return nil without
// creating another worker.
//
// The supplied nonnil context bounds only the initial refresh. The worker uses
// a Background-derived cancellation context and a five-second ticker, with
// failures passed to report when present. Stop owns its cancellation and
// completion wait. Initial refresh failure is reported while the previously
// published defaults or snapshot remain available, and Start still returns nil
// after scheduling the worker.
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.closed || r.cancel != nil {
		r.mu.Unlock()
		return nil
	}
	// Give the refresh worker independent lifetime from the initial read context;
	// Stop owns its cancellation after publication has been closed.
	workerCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	r.mu.Unlock()
	if err := r.Refresh(ctx); err != nil && r.report != nil {
		r.report(err)
	}
	// Observe periodic read failures through report while retaining the last
	// effective pointer; the worker exits only after its own cancellation is
	// signaled.
	go func() {
		defer close(r.done)
		// Poll shared settings at the multi-instance refresh interval.
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

// Stop permanently closes publication, cancels the worker, and waits for its
// completion. A runtime that never started returns nil after closing
// publication.
//
// The caller supplies a nonnil context for the join budget. Cancellation is
// issued outside mu so the worker can finish a refresh or observe its stop
// signal; an expired join budget returns ctx.Err while publication remains
// closed. Subsequent Start cannot recreate the worker, and Publish rejects
// further local settings changes.
func (r *Runtime) Stop(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if cancel == nil {
		return nil
	}
	// Publication is already closed; cancel the independent worker and join it
	// outside the mutex so a finishing refresh can release its resources.
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
