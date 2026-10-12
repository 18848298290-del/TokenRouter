package provider

import (
	"context"
	"sync"
	"time"
)

type RefreshRateGate struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

type RefreshConcurrencyGate struct {
	slots chan struct{}
}

func NewRefreshRateGate(qps int) *RefreshRateGate {
	if qps <= 0 {
		return &RefreshRateGate{}
	}
	return NewRefreshRateGateWithInterval(time.Second / time.Duration(qps))
}

// NewRefreshRateGateWithInterval creates a gate with explicit slot spacing for timing and cancellation tests.
func NewRefreshRateGateWithInterval(interval time.Duration) *RefreshRateGate {
	return &RefreshRateGate{interval: interval}
}

func (g *RefreshRateGate) ReserveSlot(now time.Time) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.next.Before(now) {
		g.next = now
	}
	slot := g.next
	g.next = g.next.Add(g.interval)
	return slot
}

func (g *RefreshRateGate) Wait(ctx context.Context) error {
	if g == nil || g.interval <= 0 {
		return nil
	}
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

func (g *RefreshRateGate) Acquire(ctx context.Context) (func(), error) {
	if err := g.Wait(ctx); err != nil {
		return nil, err
	}
	return func() {}, nil
}

func NewRefreshConcurrencyGate(concurrency int) *RefreshConcurrencyGate {
	if concurrency < 1 {
		concurrency = 1
	}
	return &RefreshConcurrencyGate{slots: make(chan struct{}, concurrency)}
}

func (g *RefreshConcurrencyGate) Acquire(ctx context.Context) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// InFlight returns the number of occupied concurrency slots.
func (g *RefreshConcurrencyGate) InFlight() int {
	if g == nil {
		return 0
	}
	return len(g.slots)
}
