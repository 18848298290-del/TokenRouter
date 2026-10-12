package scheduler

import (
	"context"
	"time"
)

const (
	WaitNotCounted WaitOwnership = iota
	WaitCounted
	WaitUncertain
)

// WaitCounters reads and updates user and provider wait counts.
type WaitCounters interface {
	IncrementWaitCount(context.Context, int64, int) (bool, error)
	DecrementWaitCount(context.Context, int64) error
	IncrementProviderWaitCount(context.Context, int64, int) (bool, error)
	DecrementProviderWaitCount(context.Context, int64) error
}

// WaitOwnership distinguishes absent, confirmed, and uncertain increments; confirmed increments
// require release.
type WaitOwnership uint8

// WaitResult copies share one release. WaitUncertain allows the request but skips decrementing an
// unconfirmed count.
type WaitResult struct {
	Allowed   bool
	Ownership WaitOwnership
	resource  *Lease
}

func (r WaitResult) Release() { r.resource.Release() }

// EnterUserWait registers a waiting user request and allows it with uncertain ownership on cache
// failure.
func EnterUserWait(ctx context.Context, cache WaitCounters, id int64, maxWait int, diagnostics Diagnostics) (WaitResult, error) {
	if cache == nil {
		return WaitResult{Allowed: true}, nil
	}
	return enterWait(ctx, id, maxWait, "user", cache.IncrementWaitCount, cache.DecrementWaitCount, diagnostics)
}

// EnterProviderWait enforces the provider queue limit, allowing cache failures and releasing confirmed
// increments.
func EnterProviderWait(ctx context.Context, cache WaitCounters, id int64, maxWait int, diagnostics Diagnostics) (WaitResult, error) {
	if cache == nil {
		return WaitResult{Allowed: true}, nil
	}
	return enterWait(ctx, id, maxWait, "provider", cache.IncrementProviderWaitCount, cache.DecrementProviderWaitCount, diagnostics)
}

func enterWait(ctx context.Context, id int64, maxWait int, kind string, increment func(context.Context, int64, int) (bool, error), decrement func(context.Context, int64) error, diagnostics Diagnostics) (WaitResult, error) {
	allowed, err := increment(ctx, id, maxWait)
	if err != nil {
		diagnostics.printf("service.concurrency", "Warning: increment wait count failed for %s %d: %v", kind, id, err)
		return WaitResult{Allowed: true, Ownership: WaitUncertain}, nil
	}
	if !allowed {
		return WaitResult{}, nil
	}
	resource := NewLease(context.Background(), ReleaseOnCompletion, func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := decrement(cleanup, id); err != nil {
			diagnostics.printf("service.concurrency", "Warning: decrement wait count failed for %s %d: %v", kind, id, err)
		}
	})
	return WaitResult{Allowed: true, Ownership: WaitCounted, resource: resource}, nil
}
