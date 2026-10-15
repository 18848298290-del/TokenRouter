package scheduler

import (
	"context"
	"fmt"
	"time"
)

// message_queue_wait.go waits for serialization locks and spacing delays while
// reporting progress to request observers.
// Serialization admission includes waiting for a provider lock and spacing work
// after acquisition. The lock enters a completion lease before the delay, so
// cancellation during that preparation can return ownership before an acquired
// event is reported.
//
// Observer callbacks run synchronously in the waiting goroutine. Begin or
// heartbeat errors stop acquisition, while void event and delay callbacks
// report progress. The returned owner keeps runtime completion registered until
// lock cleanup finishes. WaitForLock's waiting select returns a provider
// timeout message on context termination. Cancellation during acquired-lock
// spacing returns the context error through finishQueueAcquire, and runtime
// admission errors pass through acquisition calls. ThrottleWithWait returns its
// context error when cancellation wins its waiting select.

// QueueObserver carries request-scoped wait, lock event, and throttle delay
// callbacks.
type QueueObserver struct {
	Wait WaitObserver // synchronous wait notification callbacks
	// Event runs synchronously after acquisition or reported cleanup.
	// Callers supply callbacks that return normally; errors from storage
	// are supplied as event data rather than callback results.
	Event func(name string, providerID int64, err error)
	// Delay receives the chosen positive throttle interval synchronously
	// before Begin. Callbacks return normally because this notification
	// cannot report an error through its result.
	Delay func(time.Duration) // optional throttle duration callback
}

// event reports lock progress through the optional Event callback. A nil
// callback discards it, while a configured callback receives the provider ID
// and any cleanup error synchronously.
func (o QueueObserver) event(name string, id int64, err error) {
	if o.Event != nil {
		o.Event(name, id, err)
	}
}

// acquireWithWait attempts a lock immediately using a non-nil parent and a
// bounded preparation context. Acquired admission proceeds through spacing; a
// busy result begins the observer-aware polling loop until acquisition or
// termination.
func (s *UserMessageQueueService) acquireWithWait(parent context.Context, providerID int64, baseRPM int, timeout time.Duration, observer QueueObserver) (*Lease, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result, err := s.TryAcquire(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if result.Acquired {
		return s.finishQueueAcquire(ctx, providerID, baseRPM, result, observer)
	}
	return s.WaitForLock(ctx, providerID, baseRPM, observer)
}

// finishQueueAcquire gives admitted lock cleanup to a lease before applying
// remaining RPM spacing. Callers release the returned lease when their
// serialized work finishes.
//
// A spacing error accompanied by ctx cancellation releases preparation
// ownership and returns that context error. Other spacing errors do not reject
// this admitted result. Event eligibility is enabled only after preparation
// succeeds, so cancellation cleanup before the acquired notification omits
// lock events. Later release reports storage cleanup success or failure after
// attempting owner-checked removal.
func (s *UserMessageQueueService) finishQueueAcquire(ctx context.Context, id int64, rpm int, result *QueueLockResult, observer QueueObserver) (*Lease, error) {
	released := false
	// Own lock cleanup before the delay can fail. Reporting is gated until
	// acquisition is announced, so preparation rollback avoids an
	// unmatched released event.
	lease := NewLease(context.Background(), ReleaseOnCompletion, func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := s.Release(releaseCtx, id, result.RequestID)
		if released {
			if err != nil {
				observer.event("gateway.umq_release_failed", id, err)
			} else {
				observer.event("gateway.umq_lock_released", id, nil)
			}
		}
	})
	// Roll back preparation when spacing failed with ctx terminated. Other
	// delay errors keep the admitted lock owner available for the caller's
	// serialized work.
	if err := s.EnforceDelay(ctx, id, rpm); err != nil && ctx.Err() != nil {
		// Preparation was canceled before an acquired event was sent.
		// Return the owner without reporting a released event for
		// admission the observer never saw.
		lease.Release()
		return nil, ctx.Err()
	}
	released = true
	observer.event("gateway.umq_lock_acquired", id, nil)
	return lease, nil
}

// WaitForLock polls for serialization admission and returns its prepared lock
// lease. Callers supply a non-nil context, normally after an immediate busy
// result, and release successful ownership after work.
//
// Begin and heartbeat observers execute synchronously and can stop the loop
// with their errors. A separate retry timer grows capped backoff after
// contention. Context termination selected while polling returns a provider
// timeout message without wrapping its cause; TryAcquire errors pass through
// directly. A granted result delegates spacing and cleanup to
// finishQueueAcquire, whose canceled preparation can return the context cause.
func (s *UserMessageQueueService) WaitForLock(ctx context.Context, providerID int64, baseRPM int, observer QueueObserver) (*Lease, error) {
	if observer.Wait.Begin != nil {
		if err := observer.Wait.Begin(); err != nil {
			return nil, err
		}
	}
	// Heartbeat timing runs alongside lock retry timing in this goroutine.
	// A synchronous observer failure returns before another acquisition
	// attempt and owns no lock to release.
	var ping <-chan time.Time
	if observer.Wait.Heartbeat != nil && observer.Wait.Interval > 0 {
		t := time.NewTicker(observer.Wait.Interval)
		defer t.Stop()
		ping = t.C
	}
	backoff := InitialBackoff
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("umq wait timeout for provider %d", providerID)
		case <-ping:
			if err := observer.Wait.Heartbeat(); err != nil {
				return nil, err
			}
		case <-timer.C:
			result, err := s.TryAcquire(ctx, providerID)
			if err != nil {
				return nil, err
			}
			if result.Acquired {
				return s.finishQueueAcquire(ctx, providerID, baseRPM, result, observer)
			}
			backoff = NextBackoff(backoff)
			timer.Reset(backoff)
		}
	}
}

// ThrottleWithWait applies one RPM-based delay under runtime tracking and the
// caller timeout. It sends the delay observation and Begin before waiting,
// then synchronously emits configured heartbeats.
//
// The timer is independent from heartbeat ticks, so notifications do not
// recalculate or extend the selected duration. Context cancellation returns
// its error, and any heartbeat failure stops the wait. This path owns no
// serialization lock; runtime completion is deferred until return, including
// observer failures and elapsed delay.
func (s *UserMessageQueueService) ThrottleWithWait(parent context.Context, id int64, rpm int, timeout time.Duration, observer QueueObserver) error {
	operation, done, err := s.runtime.Enter(parent, "message-throttle")
	if err != nil {
		return err
	}
	defer done()
	parent = operation
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	delay := s.CalculateRPMAwareDelay(ctx, id, rpm)
	if delay <= 0 {
		return nil
	}
	if observer.Delay != nil {
		observer.Delay(delay)
	}
	if observer.Wait.Begin != nil {
		if err := observer.Wait.Begin(); err != nil {
			return err
		}
	}
	var ping <-chan time.Time
	if observer.Wait.Heartbeat != nil && observer.Wait.Interval > 0 {
		t := time.NewTicker(observer.Wait.Interval)
		defer t.Stop()
		ping = t.C
	}
	// Keep this selected throttle interval independent from heartbeat
	// ticks. Its timer competes with cancellation while deferred runtime
	// completion handles every loop exit.
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ping:
			if err := observer.Wait.Heartbeat(); err != nil {
				return err
			}
		case <-timer.C:
			return nil
		}
	}
}

// AcquireWithWait tracks serialization preparation and the admitted lock until
// its cleanup completes. It returns a lease whose Release returns lock
// ownership before ending runtime registration.
//
// Acquisition failure ends registration immediately. Successful preparation
// transfers registration into the combined owner and attaches it to a request
// lease when parent carries one; otherwise the caller retains the returned
// owner responsibility. The returned completion lease is not automatically
// released by parent cancellation after acquisition, allowing execution to
// choose the point at which serialized work actually ends.
func (s *UserMessageQueueService) AcquireWithWait(parent context.Context, id int64, rpm int, timeout time.Duration, observer QueueObserver) (*Lease, error) {
	operation, done, err := s.runtime.Enter(parent, fmt.Sprintf("message-lock:%d", id))
	if err != nil {
		return nil, err
	}
	lease, err := s.acquireWithWait(operation, id, rpm, timeout, observer)
	if err != nil {
		done()
		return nil, err
	}
	// Transfer runtime registration after preparation succeeds. Combined
	// cleanup releases the lock first, then signals completion so shutdown
	// waits for the remote cleanup attempt.
	owned := NewLease(context.Background(), ReleaseOnCompletion, done, lease.Release)
	ownRequestResource(parent, owned.Release)
	return owned, nil
}
