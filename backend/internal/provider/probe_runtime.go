package provider

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// probe_runtime.go shares on-demand probe results and reserves background
// tasks by key within a tracked shutdown lifetime. A Grok quota caller can
// abandon its wait while another caller still needs the same probe. Shared
// on-demand probes use the first caller's context values with a separate
// timeout, and both the waiter and the shared operation register with the
// shutdown tracker. Identical in-flight keys share the same callback result;
// each consumer supplies its own copying policy for mutable values.
//
// Background model discovery has a separate key set. It reserves a key and
// registers the task before launching its goroutine, preventing another
// background request from starting the same task during that lifetime.
// Shutdown cancels both kinds of registered work and waits within its caller's
// budget, retaining the first shutdown result even when a callback finishes
// after a timeout.

// ErrProbeStopped marks rejected admission after the runtime permanently closes
// its operation tracker to new provider probes and background tasks.
var ErrProbeStopped = errors.New("provider probe runtime stopped")

// ProbeRuntime owns cancellable shared probes and keyed background tasks. Its
// zero value is usable; retain one instance for consumers whose work belongs to
// the same shutdown lifetime.
type ProbeRuntime struct {
	// activity tracks on-demand waiters, shared callbacks, and scheduled callbacks
	// separately. Its permanent stop barrier supplies cancellation and completion
	// waiting for all three lifetimes.
	activity operationActivity
	// flights and background use independent key spaces. Run shares in-flight
	// results through flights, while Schedule reserves background keys until the
	// admitted callback returns.
	flights    singleflight.Group // keyed on-demand result sharing
	background sync.Map           // reserved background task keys
}

// Run returns a shared in-flight probe result or this waiter's cancellation
// error. Callers provide a nonnil context and callback; matching keys share the
// first callback and budget, so keys distinguish incompatible probe inputs.
// Returned mutable values remain shared until the consumer copies them.
//
// Each waiter registers independently, while the callback keeps the initiating
// context's values through WithoutCancel and receives its own budget plus
// runtime cancellation. Budget governs the callback context; cooperation
// determines actual completion after expiry. Cancelling one waiter leaves the
// shared operation registered. If shutdown prevents that operation from
// registering after its waiter was admitted, the shared result becomes
// context.Canceled. Completion and waiter cancellation can both be ready when
// the final select chooses its result.
func (r *ProbeRuntime) Run(ctx context.Context, key string, budget time.Duration, probe func(context.Context) (any, error)) (any, error) {
	waitCtx, finishWait, err := r.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer finishWait()
	// Register shared work separately from its waiters so an abandoned caller
	// cannot remove the task from the runtime's shutdown wait. The first caller
	// supplies its context values.
	result := r.flights.DoChan(key, func() (any, error) {
		shared, finish, err := r.activity.begin(context.WithoutCancel(ctx), ErrProbeStopped)
		// Shutdown can close admission between waiter registration and callback
		// startup. Convert that rejected shared registration to cancellation for the
		// waiter that was already accepted.
		if err == ErrProbeStopped {
			return nil, context.Canceled
		}
		if err != nil {
			return nil, err
		}
		defer finish()
		shared, cancel := context.WithTimeout(shared, budget)
		defer cancel()
		return probe(shared)
	})
	// DoChan buffers the callback result, so this waiter can leave on cancellation
	// while shared completion still delivers to other registered waiters.
	select {
	case <-waitCtx.Done():
		return nil, waitCtx.Err()
	case result := <-result:
		return result.Val, result.Err
	}
}

// Schedule admits one background callback for key and reports whether it
// launched. An occupied background key or closed runtime returns false;
// on-demand Run keys use a separate sharing group. Callers supply a usable
// callback that cooperates with its timeout context.
//
// The key is reserved before operation registration, then registration completes
// before the goroutine is launched. A rejected registration releases the
// reservation synchronously. An admitted callback uses Background with the
// supplied budget and runtime cancellation, and its deferred key deletion
// precedes operation completion, allowing a later task with that key after this
// callback returns.
func (r *ProbeRuntime) Schedule(key string, budget time.Duration, work func(context.Context)) bool {
	// Reserve the key while a callback is still waiting to start. Moving this
	// reservation inside the goroutine would allow concurrent schedulers to launch
	// duplicate callbacks before either one begins running.
	if _, loaded := r.background.LoadOrStore(key, struct{}{}); loaded {
		return false
	}
	ctx, finish, err := r.activity.begin(context.Background(), ErrProbeStopped)
	if err != nil {
		// A stopped runtime cannot run the reserved task. Remove its key immediately
		// because no callback goroutine will execute the deferred reservation cleanup.
		r.background.Delete(key)
		return false
	}
	// Work may not have begun when Schedule returns true. Complete registration
	// before launching the goroutine so shutdown tracks this accepted callback
	// even while it is waiting to run.
	go func() {
		defer finish()
		defer r.background.Delete(key)
		ctx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		work(ctx)
	}()
	return true
}

// StopContext cancels admitted waiters and callbacks and waits within ctx for
// their completion. Admission stays closed afterward. The first completed stop
// result is retained, including unfinished-work timeout; concurrent later stops
// can return their own context error while that first result is still pending.
func (r *ProbeRuntime) StopContext(ctx context.Context) error {
	return r.activity.stop(ctx, "provider probes")
}
