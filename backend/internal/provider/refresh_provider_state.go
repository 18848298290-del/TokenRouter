package provider

import (
	"context"
	"errors"
	"sync"
)

// A platform cycle can stop admitting work after consecutive attempt failures,
// shared configuration rejection or an uncertain contained outcome. Permanent
// credential rejection belongs to its individual provider, so it resets the
// platform streak and leaves other candidates available. Success also resets the
// streak, while outer cancellation and ordinary skipped work leave it unchanged.
//
// The mutex protects streak and trip state as workers record outcomes
// concurrently. Capacity and rate waits use gates shared with other cycles, and
// admission rechecks trip state after each wait before allowing exchange. Once
// tripped, this state remains closed even if already running work later
// succeeds. A new execution state starts the next cycle with an independent
// streak.

// RefreshAttemptGate supplies cancellable admission and a caller-owned release
// callback for attempts.
type RefreshAttemptGate interface {
	// Acquire admits one attempt or returns a cancellation or admission error. On
	// success it returns a callable release function; the caller invokes that
	// function once when its attempt leaves the gate.
	Acquire(context.Context) (func(), error)
}

// RefreshProviderState combines shared admission gates with one cycle's platform
// failure decisions.
type RefreshProviderState struct {
	rateGate RefreshAttemptGate // optional shared rate admission
	// optional shared capacity admission
	poolGate *RefreshConcurrencyGate
	// failureThreshold trips this cycle once counted failures reach it; a
	// nonpositive value therefore trips on the first counted failure.
	failureThreshold int
	// optional individual credential classifier
	nonRetryable func(error) bool
	// mu protects consecutiveFailures and tripped across worker results. A nil
	// result resets the streak, while an established trip remains set until
	// callers construct a different state for another cycle.
	mu                  sync.Mutex // failure state synchronization
	consecutiveFailures int        // counted consecutive platform failures
	tripped             bool       // platform cycle admission stopped
}

// NewRefreshProviderState creates an independent cycle failure state with
// borrowed admission gates. Threshold is used as supplied, nil gates allow
// immediate admission, and a nil nonRetryable callback disables its additional
// credential classification.
func NewRefreshProviderState(rate RefreshAttemptGate, pool *RefreshConcurrencyGate, threshold int, nonRetryable func(error) bool) *RefreshProviderState {
	return &RefreshProviderState{rateGate: rate, poolGate: pool, failureThreshold: threshold, nonRetryable: nonRetryable}
}

// IsTripped reports the current platform cycle admission flag under its mutex.
func (p *RefreshProviderState) IsTripped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tripped
}

// Acquire obtains platform capacity while this cycle remains open and returns
// its release callback. A nil state or established trip returns
// ErrRefreshSkipped, and gate wait failures pass through.
//
// Trip state is rechecked after capacity becomes available because another
// worker may fail during this wait. If the cycle has tripped, the newly obtained
// token is returned before reporting a skip so unrelated cycles can use their
// shared pool. A successful caller releases capacity exactly once when the
// attempt finishes; this check does not cancel work that starts before a later
// trip.
func (p *RefreshProviderState) Acquire(ctx context.Context) (func(), error) {
	if p == nil || p.IsTripped() {
		return nil, ErrRefreshSkipped
	}
	release, err := p.poolGate.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	// A sibling failure may trip the cycle during admission. Release this unused
	// grant before skipping so shared admission remains available to other
	// cycles.
	if p.IsTripped() {
		release()
		return nil, ErrRefreshSkipped
	}
	return release, nil
}

// AcquireRate obtains rate admission immediately before exchange while this
// cycle remains open. A nil state or trip returns ErrRefreshSkipped, and a
// missing rate gate supplies a no-op release.
//
// Another worker can trip the platform while this call waits, so trip state is
// rechecked after admission and the obtained release is invoked before a skip.
// Shared rate schedules can retain canceled or skipped reservations according to
// their gate implementation. A successful caller owns the returned release
// callback, while work already admitted can continue if a later worker trips the
// state.
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
	// A sibling failure may trip the cycle during the rate wait. Invoke its
	// release before skipping; a time-based gate keeps the reserved timestamp.
	if p.IsTripped() {
		release()
		return nil, ErrRefreshSkipped
	}
	return release, nil
}

// RecordResult updates this cycle's streak and trip flag under the mutex.
// Success resets the streak; ordinary skipped work and outer cancellation leave
// it unchanged, while RefreshAttemptTimeoutError still counts as an attempt
// failure.
//
// After cancellation filtering, configuration and containment wrappers trip
// the platform immediately.
// Permanent credential wrappers or the nonRetryable classifier reset the streak
// because another provider may still refresh successfully. Other failures
// increment it and trip when the threshold is reached. A prior trip remains set
// across all branches, so completion of an already running successful attempt
// does not reopen admission for this state.
func (p *RefreshProviderState) RecordResult(err error) {
	// Classify and change the streak under one mutex so concurrent worker results
	// cannot lose increments or reopen a platform that another result has
	// tripped.
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
		// A credential-specific rejection does not establish a shared platform fault.
		// Reset its streak so another provider can refresh through the same admission.
		p.consecutiveFailures = 0
		return
	}
	p.consecutiveFailures++
	if p.consecutiveFailures >= p.failureThreshold {
		p.tripped = true
	}
}
