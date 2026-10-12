package provider

import (
	"context"
	"sync"
	"time"
)

// Credential exchanges share platform capacity and rate limits, but the two
// limits have different lifetimes. A concurrency acquisition occupies one
// channel slot until its caller releases it; a rate acquisition reserves one
// start time, and its release callback has no effect on that schedule.
//
// Rate reservations advance a mutex-protected timestamp even if their later wait
// is canceled, so cancellation leaves a gap rather than pulling another
// reservation forward. Concurrency acquisition selects between a slot and
// context cancellation; when both are ready, either result may win. Callers
// release each granted capacity slot exactly once and let the surrounding
// refresh state recheck whether platform work has stopped.

// RefreshRateGate reserves spaced exchange start times shared by platform
// workers.
type RefreshRateGate struct {
	// mu protects next while interval is fixed at construction. Each reservation
	// advances next by interval, including reservations whose callers later cancel
	// their wait, so concurrent workers keep a single platform schedule.
	mu       sync.Mutex    // reservation timestamp synchronization
	next     time.Time     // next exchange reservation time
	interval time.Duration // configured exchange start spacing
}

// RefreshConcurrencyGate bounds occupied exchange capacity using a buffered
// token channel.
type RefreshConcurrencyGate struct {
	// slots records granted capacity until the caller invokes its release once.
	// It stays open throughout the gate lifetime, allowing later attempts to
	// wait.
	slots chan struct{}
}

// NewRefreshRateGate converts positive QPS into spacing between exchange start
// reservations. Nonpositive QPS creates an unrestricted gate, and integer
// duration division can also yield zero spacing for very large values.
func NewRefreshRateGate(qps int) *RefreshRateGate {
	if qps <= 0 {
		return &RefreshRateGate{}
	}
	return NewRefreshRateGateWithInterval(time.Second / time.Duration(qps))
}

// NewRefreshRateGateWithInterval creates a schedule with the supplied exchange
// spacing for production and timing fixtures. Wait treats nonpositive intervals
// as unrestricted, while ReserveSlot still applies the supplied interval
// directly.
func NewRefreshRateGateWithInterval(interval time.Duration) *RefreshRateGate {
	return &RefreshRateGate{interval: interval}
}

// ReserveSlot returns the next start time and advances the shared schedule by
// interval. Callers supply the current observation time; the receiver must be
// initialized and its interval stays fixed.
//
// Under the mutex, a schedule behind now moves to now before reservation, while
// a future reservation remains queued. The returned timestamp is consumed even
// if a subsequent wait cancels. A positive interval spaces overlapping
// reservations; nonpositive intervals are still applied as supplied. Callers
// keep interval fixed while the gate is shared.
func (g *RefreshRateGate) ReserveSlot(now time.Time) time.Time {
	// Reserve and advance under one mutex so overlapping callers use one shared
	// schedule, including when an earlier reservation is still waiting or
	// canceled.
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.next.Before(now) {
		g.next = now
	}
	slot := g.next
	g.next = g.next.Add(g.interval)
	return slot
}

// Wait reserves the next start time and waits for it or context cancellation.
// Nil gates and nonpositive intervals return immediately, and a due reservation
// can return before consulting the context.
//
// A canceled wait returns the context error while leaving its reservation in the
// shared schedule. Future workers keep their assigned spacing rather than
// reusing the canceled timestamp. The timer is stopped on exit, and the caller
// supplies a nonnil context when a positive wait is reached; simultaneous timer
// readiness and cancellation can select either result.
func (g *RefreshRateGate) Wait(ctx context.Context) error {
	if g == nil || g.interval <= 0 {
		return nil
	}
	// Keep the reservation on cancellation so later workers retain their scheduled
	// spacing; a release callback cannot move the shared timestamp backward.
	slot := g.ReserveSlot(time.Now())

	Wait := time.Until(slot)
	if Wait <= 0 {
		return nil
	}
	timer := time.NewTimer(Wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Acquire delegates to Wait and returns a callable no-op release after
// admission. Rate reservations have no occupied capacity to return, so invoking
// that callback does not undo their scheduled time.
func (g *RefreshRateGate) Acquire(ctx context.Context) (func(), error) {
	if err := g.Wait(ctx); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// NewRefreshConcurrencyGate creates a capacity gate, using one slot for
// nonpositive concurrency. Each successful acquisition returns a release
// function that the caller invokes exactly once after its attempt finishes.
func NewRefreshConcurrencyGate(concurrency int) *RefreshConcurrencyGate {
	if concurrency < 1 {
		concurrency = 1
	}
	return &RefreshConcurrencyGate{slots: make(chan struct{}, concurrency)}
}

// Acquire waits for available exchange capacity or context cancellation and
// returns the granted slot's release function. A nil gate yields a callable
// no-op; a configured gate requires a nonnil context.
//
// The channel send and cancellation compete in one select, so a ready slot can
// win even when cancellation is also ready. The caller owns one granted token
// and invokes release exactly once after finishing the attempt. Repeated release
// can consume another holder's token or block, and this gate leaves
// post-admission cancellation checks to the caller.
func (g *RefreshConcurrencyGate) Acquire(ctx context.Context) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	// Cancellation can interrupt an occupied pool, while a successful send
	// returns caller-owned capacity that remains held through the exchange attempt.
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// InFlight reports the currently occupied capacity slots, returning zero for a
// nil gate. The channel length is an instantaneous observation and may change
// before a caller acts on it.
func (g *RefreshConcurrencyGate) InFlight() int {
	if g == nil {
		return 0
	}
	return len(g.slots)
}
