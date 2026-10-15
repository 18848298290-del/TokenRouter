package scheduler

import (
	"context"
	"time"
)

// wait.go admits user and provider waiters and attaches cleanup to confirmed
// counter ownership.
// Wait queues use shared counts for a user or provider, rather than one stored
// member for each request. An admission error can leave the increment
// unconfirmed, so the scheduler permits that request with uncertain ownership
// and does not decrement the count on its behalf.
//
// A confirmed increment receives a lease-managed cleanup callback shared by
// every copy of WaitResult. Cleanup can run after request cancellation using
// its own bounded context, and repeated releases do not consume another count.
// A denied queue owns no counter resource. The concurrency service can add
// shutdown registration to the same result even when the cache did not confirm
// a count; Ownership still describes the counter decision, while resource
// controls any attached cleanup.

// Wait ownership values distinguish queue rejection or bypass from a confirmed
// increment and an admitted request with uncertain counter ownership.
const (
	WaitNotCounted WaitOwnership = iota // no confirmed counter increment
	WaitCounted                         // confirmed increment owns cleanup
	WaitUncertain                       // admission with unconfirmed increment
)

// WaitCounters reads and updates user and provider wait counts.
type WaitCounters interface {
	// IncrementWaitCount admits one user waiter below maxWait and reports
	// whether it incremented. Callers decrement only confirmed admissions;
	// an error leaves ownership uncertain and the configured counter TTL
	// bounds leftover state.
	IncrementWaitCount(context.Context, int64, int) (bool, error)
	// DecrementWaitCount returns one confirmed user waiter count after
	// waiting finishes. The Redis adapter leaves absent or zero counters
	// unchanged, so cleanup does not create negative queue occupancy.
	DecrementWaitCount(context.Context, int64) error
	// IncrementProviderWaitCount admits one provider waiter below maxWait
	// and reports the increment. An error leaves ownership uncertain;
	// consumers retain capacity cleanup only when admission was confirmed
	// by a successful result.
	IncrementProviderWaitCount(context.Context, int64, int) (bool, error)
	// DecrementProviderWaitCount returns one confirmed provider waiter
	// count after waiting finishes. The Redis adapter leaves missing or
	// zero counters unchanged and does not refresh their expiry during
	// decrement.
	DecrementProviderWaitCount(context.Context, int64) error
}

// WaitOwnership distinguishes absent, confirmed, and uncertain increments; confirmed increments
// require release.
type WaitOwnership uint8

// WaitResult copies share one release. WaitUncertain allows the request but
// skips decrementing an unconfirmed count. Call Release for every allowed
// result; service wrappers can attach shutdown tracking even when counter
// ownership is uncertain.
type WaitResult struct {
	Allowed   bool          // request may enter waiting
	Ownership WaitOwnership // counter acquisition result category
	// resource may own confirmed count cleanup and service shutdown
	// registration. Copies share this lease; service wrappers can attach
	// registration even when Ownership reports no confirmed counter.
	resource *Lease // shared wait cleanup owner
}

// Release runs this result's shared cleanup owner, if one was attached. Copies
// and repeated calls share the same lease, and a nil resource succeeds without
// decrementing an unconfirmed counter.
func (r WaitResult) Release() { r.resource.Release() }

// EnterUserWait attempts user queue admission and returns counter ownership
// with cleanup. Nil cache allows without counting; cache failure allows with
// WaitUncertain and a nil error so an unconfirmed increment is never
// decremented here.
func EnterUserWait(ctx context.Context, cache WaitCounters, id int64, maxWait int, diagnostics Diagnostics) (WaitResult, error) {
	if cache == nil {
		return WaitResult{Allowed: true}, nil
	}
	return enterWait(ctx, id, maxWait, "user", cache.IncrementWaitCount, cache.DecrementWaitCount, diagnostics)
}

// EnterProviderWait attempts provider queue admission and returns counter
// ownership with cleanup. Nil cache allows without counting; cache failure
// allows with uncertain ownership, while a confirmed full queue returns a
// denied result.
func EnterProviderWait(ctx context.Context, cache WaitCounters, id int64, maxWait int, diagnostics Diagnostics) (WaitResult, error) {
	if cache == nil {
		return WaitResult{Allowed: true}, nil
	}
	return enterWait(ctx, id, maxWait, "provider", cache.IncrementProviderWaitCount, cache.DecrementProviderWaitCount, diagnostics)
}

// enterWait associates confirmed queue admission with its cleanup owner and
// reports whether the request can wait. Cache errors are logged and converted
// to an allowed, uncertain result.
//
// A counter error does not establish that this request owns a count, so no
// decrement callback is installed for it. Confirmed admission returns one
// idempotent owner whose cleanup uses a bounded background context after
// waiting ends. Cleanup failure is logged and left to counter expiry; queue
// denial returns no counter resource and no cache error.
func enterWait(ctx context.Context, id int64, maxWait int, kind string, increment func(context.Context, int64, int) (bool, error), decrement func(context.Context, int64) error, diagnostics Diagnostics) (WaitResult, error) {
	allowed, err := increment(ctx, id, maxWait)
	if err != nil {
		diagnostics.printf("service.concurrency", "Warning: increment wait count failed for %s %d: %v", kind, id, err)
		// The failed increment did not confirm ownership. Skipping
		// decrement avoids reducing a count held by another waiting
		// request; cache TTL handles uncertain leftovers.
		return WaitResult{Allowed: true, Ownership: WaitUncertain}, nil
	}
	if !allowed {
		return WaitResult{}, nil
	}
	// Give confirmed admission one shared cleanup owner. Its separate
	// deadline lets waiting completion attempt the decrement after request
	// cancellation, and repeated releases share that execution.
	resource := NewLease(context.Background(), ReleaseOnCompletion, func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Cleanup can end with the decrement unconfirmed. Report that
		// failure and rely on the wait counter's TTL to expire any
		// remaining occupancy.
		if err := decrement(cleanup, id); err != nil {
			diagnostics.printf("service.concurrency", "Warning: decrement wait count failed for %s %d: %v", kind, id, err)
		}
	})
	return WaitResult{Allowed: true, Ownership: WaitCounted, resource: resource}, nil
}
