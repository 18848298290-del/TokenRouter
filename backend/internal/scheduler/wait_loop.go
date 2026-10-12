package scheduler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

// Backoff starts at 100 milliseconds, grows by a factor of 1.5, and is capped at 2 seconds.
const (
	InitialBackoff    = 100 * time.Millisecond
	MaxBackoff        = 2 * time.Second
	backoffMultiplier = 1.5
)

// ConcurrencyError identifies the slot type and whether waiting timed out.
type ConcurrencyError struct {
	SlotType  string
	IsTimeout bool
}

// WaitQueueFullError reports that the user's wait queue is full.
type WaitQueueFullError struct {
	SlotType string
}

// WaitObserver receives synchronous wait events. Begin is skipped when a slot is acquired immediately.
type WaitObserver struct {
	Interval  time.Duration
	Begin     func() error
	Heartbeat func() error
}

// UserAcquireOptions contains the inputs for user admission and resource release.
type UserAcquireOptions struct {
	UserID   int64
	APIKeyID int64
	Limit    int
	Timeout  time.Duration
	Mode     ReleaseMode
	Observer WaitObserver
}

func (e *ConcurrencyError) Error() string {
	if e.IsTimeout {
		return fmt.Sprintf("timeout waiting for %s concurrency slot", e.SlotType)
	}
	return fmt.Sprintf("%s concurrency limit reached", e.SlotType)
}

func (e *WaitQueueFullError) Error() string {
	return "Too many pending requests, please retry later"
}

// WaitForSlot optionally tries immediately, then retries with backoff until acquisition, cancellation,
// or timeout.
func (s *ConcurrencyService) WaitForSlot(parent context.Context, slotType string, id int64, limit int, timeout time.Duration, immediate bool, observer WaitObserver) (func(), error) {
	operation, finish, err := s.runtime.Enter(parent, "slot-wait")
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(operation, timeout)
	defer cancel()
	acquire := func() (*AcquireResult, error) {
		if slotType == "user" {
			return s.AcquireUserSlot(ctx, id, limit)
		}
		return s.AcquireProviderSlot(ctx, id, limit)
	}
	if immediate {
		result, err := acquire()
		if err != nil {
			return nil, err
		}
		if result.Acquired {
			return result.ReleaseFunc, nil
		}
	}
	if observer.Begin != nil {
		if err := observer.Begin(); err != nil {
			return nil, err
		}
	}
	var ping <-chan time.Time
	if observer.Heartbeat != nil && observer.Interval > 0 {
		t := time.NewTicker(observer.Interval)
		defer t.Stop()
		ping = t.C
	}
	backoff := InitialBackoff
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := parent.Err(); err != nil {
				return nil, err
			}
			if operation.Err() != nil {
				return nil, operation.Err()
			}
			return nil, &ConcurrencyError{SlotType: slotType, IsTimeout: true}
		case <-ping:
			if err := observer.Heartbeat(); err != nil {
				return nil, err
			}
		case <-timer.C:
			result, err := acquire()
			if err != nil {
				return nil, err
			}
			if result.Acquired {
				return result.ReleaseFunc, nil
			}
			backoff = NextBackoff(backoff)
			timer.Reset(backoff)
		}
	}
}

// AcquireUser returns ownership of user and API key slots and releases queue capacity when waiting ends.
func (s *ConcurrencyService) AcquireUser(ctx context.Context, options UserAcquireOptions) (*Lease, WaitResult, error) {
	result, err := s.AcquireUserSlot(ctx, options.UserID, options.Limit)
	if err != nil {
		return nil, WaitResult{}, err
	}
	var waited WaitResult
	release := result.ReleaseFunc
	if !result.Acquired {
		limit := max(CalculateMaxWait(options.Limit)-options.Limit, 1)
		waited, err = s.EnterUserWait(ctx, options.UserID, limit)
		if err != nil {
			return nil, waited, err
		}
		if !waited.Allowed {
			return nil, waited, &WaitQueueFullError{SlotType: "user"}
		}
		defer waited.Release()
		release, err = s.WaitForSlot(ctx, "user", options.UserID, options.Limit, options.Timeout, false, options.Observer)
		if err != nil {
			return nil, waited, err
		}
	}
	// Own the user slot before tracking the API key slot; the caller selects the cancellation release mode.
	lease := NewLease(context.Background(), ReleaseOnCompletion, release)
	if options.APIKeyID > 0 {
		keyRelease := s.TrackAPIKeySlot(ctx, options.APIKeyID)
		// Release the user slot before the API key statistics slot.
		combined := NewLease(ctx, options.Mode, func() {
			lease.Release()
			if keyRelease != nil {
				keyRelease()
			}
		})
		return combined, waited, nil
	}
	return NewLease(ctx, options.Mode, lease.Release), waited, nil
}

func NextBackoff(current time.Duration) time.Duration {
	next := min(time.Duration(float64(current)*backoffMultiplier), MaxBackoff)
	// Apply +/-20% jitter to spread concurrent Redis retries over time.
	jitter := 0.8 + rand.Float64()*0.4
	jittered := time.Duration(float64(next) * jitter)
	if jittered < InitialBackoff {
		return InitialBackoff
	}
	if jittered > MaxBackoff {
		return MaxBackoff
	}
	return jittered
}
