package scheduler

import (
	"context"
	"sync"
	"time"
)

// image_limiter.go admits image requests through a process-local capacity
// limit and bounded waiting queue.
// Image requests have a process-local capacity gate in addition to provider
// Redis slots. Active requests and waiters share one mutex, and returning
// capacity closes the current notification channel so every waiter on that
// generation can retry admission.
//
// Waiting is bounded by caller timeout and an optional positive queue limit.
// Notifications convey that capacity may have changed, rather than granting a
// specific waiter a slot; admission order is determined by the retry race
// under the mutex. Each waiter returns its waiting count before retrying or
// exiting. The caller releases an admitted slot through an idempotent
// callback, while disabled or nonpositive limits allow requests without a
// local resource owner.

// ImageConcurrencyLimiter owns process-local image slots and waiting counts
// under one mutex.
type ImageConcurrencyLimiter struct {
	// mu protects notify, limit, active, waiting, and enabled.
	// tryAcquireLocked records settings for counted admission, while
	// existing active slots retain their owners across later limit changes.
	mu      sync.Mutex    // protects local capacity state
	notify  chan struct{} // current capacity notification generation
	limit   int           // current local capacity limit
	active  int           // occupied image request slots
	waiting int           // registered image request waiters
	enabled bool          // last enabled admission setting
}

// TryAcquire attempts immediate local admission using enabled and limit.
// Disabled or nonpositive limits return allowed with nil release; a counted
// acquisition returns an idempotent callback the caller invokes after image
// work finishes.
func (l *ImageConcurrencyLimiter) TryAcquire(enabled bool, limit int) (func(), bool) {
	return l.acquire(context.Background(), enabled, limit, false, 0, 0)
}

// Acquire attempts local admission and optionally waits within timeout.
// Disabled or nonpositive limits allow with nil release; a positive maxWaiting
// bounds waiters, and other values impose no waiting-count ceiling.
//
// Waiting mode requires a positive timeout, but admission checks available
// capacity before observing ctx cancellation. Thus an immediately available
// slot can be acquired with a canceled context. A counted result supplies an
// idempotent release callback; a failed wait returns false with nil release,
// and nil ctx uses a background context.
func (l *ImageConcurrencyLimiter) Acquire(ctx context.Context, enabled bool, limit int, wait bool, timeout time.Duration, maxWaiting int) (func(), bool) {
	return l.acquire(ctx, enabled, limit, wait, timeout, maxWaiting)
}

// acquire coordinates immediate admission with a registered wait on a
// notification generation. Disabled limits return allowed without recording
// capacity, and waiting mode requires a positive timeout.
//
// A failed immediate attempt can register one waiter under the mutex and
// capture that generation channel. Wakeups cause a new admission attempt,
// after returning the old waiting count; cancellation also returns that count
// before failure. Notifications grant no FIFO position, and capacity can be
// won by another request before the awakened waiter reacquires the mutex.
func (l *ImageConcurrencyLimiter) acquire(ctx context.Context, enabled bool, limit int, wait bool, timeout time.Duration, maxWaiting int) (func(), bool) {
	if !enabled || limit <= 0 {
		return nil, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if wait {
		if timeout <= 0 {
			return nil, false
		}
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = waitCtx
	}
	if maxWaiting < 0 {
		maxWaiting = 0
	}
	for {
		release, acquired, waitRelease, notify := l.tryAcquireLocked(enabled, limit, wait, maxWaiting)
		if acquired {
			return release, acquired
		}
		if !wait || notify == nil {
			return nil, false
		}
		// This attempt registered a waiter on one notification
		// generation. Return that count on cancellation or wakeup
		// before retrying so the next admission does not count it
		// twice.
		if !l.waitForSlot(ctx, notify) {
			if waitRelease != nil {
				waitRelease()
			}
			return nil, false
		}
		if waitRelease != nil {
			waitRelease()
		}
	}
}

// tryAcquireLocked tries a slot or records a waiter while holding the capacity
// mutex. It returns an active-slot release on success, or a waiter release and
// notification generation when waiting is admitted.
//
// The latest supplied enabled and limit values become the local settings
// snapshot. Active occupancy is retained across those changes. A full active
// limit and reached positive waiting ceiling reject immediately; otherwise
// waiting registration and notification capture occur together, preventing a
// capacity release between separate registration and subscription steps.
func (l *ImageConcurrencyLimiter) tryAcquireLocked(enabled bool, limit int, wait bool, maxWaiting int) (func(), bool, func(), <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.notify == nil {
		l.notify = make(chan struct{})
	}
	if l.enabled != enabled || l.limit != limit {
		l.enabled = enabled
		l.limit = limit
	}
	// Try available active capacity before allocating a waiter. A request
	// admitted here does not consume waiting capacity as well as its image
	// slot.
	if l.active < l.limit {
		l.active++
		return l.releaseFunc(), true, nil, nil
	}
	if !wait {
		return nil, false, nil, nil
	}
	if maxWaiting > 0 && l.waiting >= maxWaiting {
		return nil, false, nil, nil
	}
	// Record waiting and capture the current channel under the same mutex.
	// Slot release cannot publish a wakeup between separate count
	// registration and channel subscription.
	l.waiting++
	return nil, false, l.waiterReleaseFunc(), l.notify
}

// waitForSlot reports which notification or cancellation event the select
// observes. Callers provide the captured generation channel and a non-nil
// context, then return their registered waiting count.
//
// A notification asks the caller to retry admission rather than reserving a
// slot. A simultaneous notification and cancellation can choose either result,
// and a closed generation can be observed before another retry discovers that
// capacity was consumed by another request. The admission loop therefore
// repeats the mutex-protected capacity check after each successful wakeup.
func (l *ImageConcurrencyLimiter) waitForSlot(ctx context.Context, notify <-chan struct{}) bool {
	select {
	case <-notify:
		return true
	case <-ctx.Done():
		return false
	}
}

// releaseFunc returns the idempotent cleanup callback for an occupied image
// slot. It is created while the limiter mutex is held and invoked after the
// caller finishes work.
//
// Cleanup decrements active capacity under that mutex, then closes and
// replaces the current notification generation. Existing waiters wake to
// compete for admission, and later waiters receive the new channel. Closing
// and replacement occur together with the capacity update, so a waiter cannot
// subscribe to an already consumed generation after recording its wait.
func (l *ImageConcurrencyLimiter) releaseFunc() func() {
	return NewLease(context.Background(), ReleaseOnCompletion, func() {
		// Return active capacity and replace its notification
		// generation under the same mutex. A waiter then registers
		// either before this wakeup or on the replacement channel.
		l.mu.Lock()
		if l.active > 0 {
			l.active--
		}
		if l.notify != nil {
			close(l.notify)
			l.notify = make(chan struct{})
		}
		l.mu.Unlock()
	}).Release
}

// waiterReleaseFunc creates idempotent cleanup for one registered waiting
// count. Call it after a notification or cancellation before retrying
// admission; it decrements waiting under the limiter mutex without returning
// active capacity.
func (l *ImageConcurrencyLimiter) waiterReleaseFunc() func() {
	return NewLease(context.Background(), ReleaseOnCompletion, func() {
		l.mu.Lock()
		if l.waiting > 0 {
			// Returning a waiter reduces queue occupancy alone.
			// Keep active occupancy and the notification
			// generation intact because this callback did not
			// release an image slot.
			l.waiting--
		}
		l.mu.Unlock()
	}).Release
}
