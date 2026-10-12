package provider

import (
	"context"
	"errors"
	"sync"
)

// RefreshAttemptGate admits refresh work and returns a release function for any reserved capacity.
type RefreshAttemptGate interface {
	Acquire(context.Context) (func(), error)
}

// RefreshProviderState tracks failures for one platform cycle; rate and concurrency gates are shared across cycles.
type RefreshProviderState struct {
	rateGate            RefreshAttemptGate
	poolGate            *RefreshConcurrencyGate
	failureThreshold    int
	nonRetryable        func(error) bool
	mu                  sync.Mutex
	consecutiveFailures int
	tripped             bool
}

func NewRefreshProviderState(rate RefreshAttemptGate, pool *RefreshConcurrencyGate, threshold int, nonRetryable func(error) bool) *RefreshProviderState {
	return &RefreshProviderState{rateGate: rate, poolGate: pool, failureThreshold: threshold, nonRetryable: nonRetryable}
}

func (p *RefreshProviderState) IsTripped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tripped
}

func (p *RefreshProviderState) Acquire(ctx context.Context) (func(), error) {
	if p == nil || p.IsTripped() {
		return nil, ErrRefreshSkipped
	}
	release, err := p.poolGate.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if p.IsTripped() {
		release()
		return nil, ErrRefreshSkipped
	}
	return release, nil
}

func (p *RefreshProviderState) AcquireRate(ctx context.Context) (func(), error) {
	if p == nil || p.IsTripped() {
		return nil, ErrRefreshSkipped
	}
	release := func() {}
	if p.rateGate != nil {
		var err error
		release, err = p.rateGate.Acquire(ctx)
		if err != nil {
			return nil, err
		}
	}
	if p.IsTripped() {
		release()
		return nil, ErrRefreshSkipped
	}
	return release, nil
}

func (p *RefreshProviderState) RecordResult(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err == nil {
		p.consecutiveFailures = 0
		return
	}
	if errors.Is(err, ErrRefreshSkipped) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if _, ok := errors.AsType[*RefreshAttemptTimeoutError](err); !ok {
			return
		}
	}
	if _, ok := errors.AsType[*RefreshAttemptTimeoutError](err); ok {
		p.consecutiveFailures++
		if p.consecutiveFailures >= p.failureThreshold {
			p.tripped = true
		}
		return
	}
	if _, ok := errors.AsType[*ProviderConfigurationRefreshError](err); ok {
		p.tripped = true
		return
	}
	if _, ok := errors.AsType[*ProviderCycleContainmentRefreshError](err); ok {
		p.tripped = true
		return
	}
	if _, ok := errors.AsType[*ProviderPermanentRefreshError](err); ok {
		p.consecutiveFailures = 0
		return
	}
	if p.nonRetryable != nil && p.nonRetryable(err) {
		// Permanent credential failures affect one provider; other providers on the platform continue.
		p.consecutiveFailures = 0
		return
	}
	p.consecutiveFailures++
	if p.consecutiveFailures >= p.failureThreshold {
		p.tripped = true
	}
}
