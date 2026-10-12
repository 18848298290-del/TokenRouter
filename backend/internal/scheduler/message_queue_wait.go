package scheduler

import (
	"context"
	"fmt"
	"time"
)

// QueueObserver contains request-scoped queue event callbacks.
type QueueObserver struct {
	Wait  WaitObserver
	Event func(name string, providerID int64, err error)
	Delay func(time.Duration)
}

func (o QueueObserver) event(name string, id int64, err error) {
	if o.Event != nil {
		o.Event(name, id, err)
	}
}

// acquireWithWait tries the serialization lock immediately, then applies RPM spacing or waits for the lock.
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

// finishQueueAcquire gives the lock to a Lease before delaying and releases it if the delay is canceled.
func (s *UserMessageQueueService) finishQueueAcquire(ctx context.Context, id int64, rpm int, result *QueueLockResult, observer QueueObserver) (*Lease, error) {
	released := false
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
	if err := s.EnforceDelay(ctx, id, rpm); err != nil && ctx.Err() != nil {
		// Acquisition has not been reported, so cancellation cleanup also skips lock event notifications.
		lease.Release()
		return nil, ctx.Err()
	}
	released = true
	observer.event("gateway.umq_lock_acquired", id, nil)
	return lease, nil
}

// WaitForLock polls for the lock and stops when a synchronous observer fails.
func (s *UserMessageQueueService) WaitForLock(ctx context.Context, providerID int64, baseRPM int, observer QueueObserver) (*Lease, error) {
	if observer.Wait.Begin != nil {
		if err := observer.Wait.Begin(); err != nil {
			return nil, err
		}
	}
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

// ThrottleWithWait applies an RPM-based delay within the timeout and sends heartbeats while waiting.
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

// AcquireWithWait tracks acquisition, delay, and the acquired lock until lifecycle cleanup completes.
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
	owned := NewLease(context.Background(), ReleaseOnCompletion, done, lease.Release)
	ownRequestResource(parent, owned.Release)
	return owned, nil
}
