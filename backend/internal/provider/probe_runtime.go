package provider

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrProbeStopped indicates that the runtime has stopped accepting work.
var ErrProbeStopped = errors.New("provider probe runtime stopped")

// ProbeRuntime tracks shared on-demand probes and background tasks. Its zero value is ready to use.
type ProbeRuntime struct {
	activity   operationActivity
	flights    singleflight.Group
	background sync.Map
}

// Run lets each waiter cancel independently while shared work uses its own timeout and shutdown cancellation.
func (r *ProbeRuntime) Run(ctx context.Context, key string, budget time.Duration, probe func(context.Context) (any, error)) (any, error) {
	waitCtx, finishWait, err := r.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer finishWait()
	result := r.flights.DoChan(key, func() (any, error) {
		shared, finish, err := r.activity.begin(context.WithoutCancel(ctx), ErrProbeStopped)
		// An accepted waiter sees cancellation if shutdown prevents its shared work from starting.
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
	select {
	case <-waitCtx.Done():
		return nil, waitCtx.Err()
	case result := <-result:
		return result.Val, result.Err
	}
}

// Schedule registers work before returning and rejects duplicate in-flight keys or stopped runtimes.
func (r *ProbeRuntime) Schedule(key string, budget time.Duration, work func(context.Context)) bool {
	if _, loaded := r.background.LoadOrStore(key, struct{}{}); loaded {
		return false
	}
	ctx, finish, err := r.activity.begin(context.Background(), ErrProbeStopped)
	if err != nil {
		r.background.Delete(key)
		return false
	}
	go func() {
		defer finish()
		defer r.background.Delete(key)
		ctx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		work(ctx)
	}()
	return true
}

// StopContext cancels and waits for registered work, retaining the first shutdown result.
func (r *ProbeRuntime) StopContext(ctx context.Context) error {
	return r.activity.stop(ctx, "provider probes")
}
