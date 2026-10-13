package scheduler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

// A request can wait for user or provider capacity while the HTTP adapter
// synchronously emits begin and heartbeat events. Slot polling, heartbeat
// timing, request cancellation, runtime shutdown, and the wait deadline
// progress independently within the same selection loop.
//
// AcquireUser first tries immediate admission, enters the bounded user queue
// only when necessary, and returns that queue capacity when the wait ends.
// Acquired user and API-key resources then belong to the request lease under
// its selected cancellation policy. Poll retries use jittered capped backoff
// so simultaneous waiters do not all retry at one fixed interval. Observer
// errors stop waiting immediately; they do not create a slot grant or a
// resource owner.

// Slot polling starts at InitialBackoff and uses capped multiplicative growth
// with jitter so concurrent waiters distribute repeated acquisition attempts
// over time.
const (
	InitialBackoff    = 100 * time.Millisecond
	MaxBackoff        = 2 * time.Second
	backoffMultiplier = 1.5
)

// ConcurrencyError identifies user or provider capacity rejection and whether
// its wait timed out.
type ConcurrencyError struct {
	SlotType  string // user or provider capacity
	IsTimeout bool   // wait deadline expired flag
}

// WaitQueueFullError identifies bounded user waiting admission that was denied
// before slot polling.
type WaitQueueFullError struct {
	SlotType string // user or provider capacity
}

// WaitObserver carries synchronous callbacks for HTTP waiting notifications
// and their heartbeat interval.
type WaitObserver struct {
	// Interval and Heartbeat jointly enable periodic notifications: a
	// positive Interval with a non-nil Heartbeat creates the ticker. Begin
	// runs once when waiting actually starts and can abort before polling.
	Interval  time.Duration // heartbeat callback interval
	Begin     func() error  // optional wait entry callback
	Heartbeat func() error  // optional synchronous heartbeat callback
}

// UserAcquireOptions holds user admission, optional key statistics, wait
// timing, and cleanup policy.
type UserAcquireOptions struct {
	UserID int64 // user capacity identifier
	// APIKeyID enables statistics tracking when positive. Tracking follows
	// user admission, and its cleanup joins the returned request lease
	// after the user resource has an owner.
	APIKeyID int64 // optional statistics key identifier
	// Limit bounds active user requests when positive. Nonpositive values
	// let AcquireUserSlot bypass storage admission while retaining service
	// shutdown tracking.
	Limit int // user request capacity limit
	// Timeout bounds polling after immediate user admission fails. Parent
	// cancellation or scheduler shutdown can end that wait before this
	// budget expires.
	Timeout  time.Duration // maximum slot wait duration
	Mode     ReleaseMode   // request cleanup cancellation policy
	Observer WaitObserver  // synchronous waiting notification callbacks
}

// Error formats a capacity-limit message or a wait-timeout message for the
// selected slot type.
func (e *ConcurrencyError) Error() string {
	if e.IsTimeout {
		return fmt.Sprintf("timeout waiting for %s concurrency slot", e.SlotType)
	}
	return fmt.Sprintf("%s concurrency limit reached", e.SlotType)
}

// Error returns the pending-request message consumed by queue admission error
// responses.
func (e *WaitQueueFullError) Error() string {
	return "Too many pending requests, please retry later"
}

// WaitForSlot polls until a slot is acquired, an observer fails, cancellation
// is observed, or the wait deadline ends. Callers supply a non-nil parent and
// release the returned slot callback after use.
//
// An optional immediate attempt can succeed before Begin is emitted. Later
// polling uses a retry timer alongside synchronous heartbeat notifications and
// the cancellation channel. Parent cancellation takes its own error, runtime
// cancellation follows it, and an expired wait budget yields ConcurrencyError.
// The runtime tracks this wait loop until return, while a granted slot carries
// its own completion registration.
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
	// Heartbeats and acquisition retries have separate timers. Run
	// notifications in this waiting goroutine so a write error can stop
	// polling before any later slot attempt.
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
		// Retry after the current backoff rather than on every
		// heartbeat. Advance the retry timer only after capacity
		// remains unavailable so observer traffic does not increase
		// acquisition frequency.
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

// AcquireUser returns a request lease after user capacity and optional API-key
// statistics are acquired. It tries the user slot first and registers bounded
// waiting only when that attempt is denied.
//
// The waiting count is returned on every exit from the wait phase, including
// observer or cancellation failure. Queue errors can allow uncertain admission
// but never create unconfirmed decrement ownership. After user admission,
// physical user cleanup is owned before key tracking begins. The combined
// callback returns user capacity before key statistics, and options.Mode
// determines whether client cancellation can invoke that cleanup.
func (s *ConcurrencyService) AcquireUser(ctx context.Context, options UserAcquireOptions) (*Lease, WaitResult, error) {
	result, err := s.AcquireUserSlot(ctx, options.UserID, options.Limit)
	if err != nil {
		return nil, WaitResult{}, err
	}
	var waited WaitResult
	release := result.ReleaseFunc
	if !result.Acquired {
		// CalculateMaxWait includes active user capacity as well as
		// extra waiters. Subtract the active limit before queue
		// admission, retaining one waiter if that remainder is
		// nonpositive.
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
	// Establish user capacity ownership before statistics tracking. A
	// tracking failure can return a no-op callback, while the already
	// acquired user resource remains owned for cleanup.
	lease := NewLease(context.Background(), ReleaseOnCompletion, release)
	if options.APIKeyID > 0 {
		keyRelease := s.TrackAPIKeySlot(ctx, options.APIKeyID)
		// Keep the user slot and statistics cleanup inside one
		// callback. Their release order is user first, followed by key
		// statistics, regardless of the outer cancellation cleanup
		// trigger.
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

// NextBackoff returns the next slot-poll delay after growing the previous
// interval and applying jitter. It caps the grown interval before sampling a
// factor from 0.8 up to 1.2, then clamps the resulting duration between
// InitialBackoff and MaxBackoff.
//
// The second clamp handles jitter moving a capped delay outside the polling
// bounds. A later delay can be shorter than the previous one near the cap, so
// callers use this result as another retry interval and keep heartbeat timing
// on its separate ticker.
func NextBackoff(current time.Duration) time.Duration {
	// Truncate fractional growth to whole nanoseconds before capping, so
	// jitter uses the bounded base retry interval.
	next := min(time.Duration(float64(current)*backoffMultiplier), MaxBackoff)
	// Spread retry attempts even when callers reach the same growth cap.
	// Jitter varies that interval by up to twenty percent in either
	// direction; converting the sample discards fractional nanoseconds
	// before final clamping.
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
