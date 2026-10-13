package systemone

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// runProbe supplies serial exchange outcomes and records provider, slot, and
// completion observations.
type runProbe struct {
	// outcomes supplies successive Forward results indexed by calls. Tests provide
	// enough entries for their retry scenario and serialize all probe observations.
	outcomes []Outcome // preset ordered exchange outcomes
	// selected and forwarded retain selection and exchange provider histories.
	// Selection derives IDs from the exchange count, so same-provider retries
	// append another forwarded ID before the next outer selection receives its ID.
	// selected is the ordered provider selection history.
	// forwarded is the provider used by each exchange.
	selected, forwarded []int64
	cancel              context.CancelFunc // optional delayed cancellation callback
	// calls indexes outcomes; completed counts usage capture, warnings counts missing
	// usage, released counts cleanup, slots counts held leases, and switches counts
	// outer recovery. Tests inspect these serial counters after Run returns.
	calls, completed, warnings, released, slots, switches int
}

// Select records a fixture provider ID derived from the current exchange
// count.
func (p *runProbe) Select(context.Context, map[int64]struct{}) (provider.ProviderSnapshot, error) {
	id := int64(p.calls + 1)
	p.selected = append(p.selected, id)
	return provider.ProviderSnapshot{ID: id}, nil
}

// Acquire counts a held fixture slot and returns its release callback with
// success. Callers invoke that callback once; each invocation decrements slots
// and increments the recorded release total.
func (p *runProbe) Acquire(context.Context) (func(), bool) {
	p.slots++
	return func() { p.slots--; p.released++ }, true
}

// Forward records the current selected provider and consumes the next preset
// outcome. When cancel is supplied it schedules cancellation after one hundred
// milliseconds; tests provide enough outcomes for their expected serial calls.
func (p *runProbe) Forward(context.Context) Outcome {
	p.forwarded = append(p.forwarded, p.selected[len(p.selected)-1])
	if p.cancel != nil {
		// Cancel after Forward returns its first retryable failure so the test
		// exercises the backoff wait, with another call prevented by the context
		// result.
		go func() {
			time.Sleep(100 * time.Millisecond)
			p.cancel()
		}()
	}
	out := p.outcomes[p.calls]
	p.calls++
	return out
}

// Report accepts exchange feedback while leaving probe observations to other
// callbacks.
func (p *runProbe) Report(context.Context, Outcome) {}

// Complete increments the usage-capture observation for a served fixture
// result.
func (p *runProbe) Complete(context.Context, upstream.AttemptResult) { p.completed++ }

// MissingUsage increments the warning observation for a served usage-less
// fixture result.
func (p *runProbe) MissingUsage(context.Context, upstream.AttemptResult) { p.warnings++ }

// Switch counts an outer provider transition after the fixture attempt fails.
func (p *runProbe) Switch() { p.switches++ }

// TestRunCompletesOnceAndReleasesSlots covers provider switching, output
// errors, unavailable usage, and recovery exhaustion. Each served outcome must
// select its completion path once while every held slot is released.
func TestRunCompletesOnceAndReleasesSlots(t *testing.T) {
	failure := Outcome{Err: errors.New("busy"), Failure: &failover.FailureInfo{RetryNext: true}}
	for _, tc := range []struct {
		name     string    // descriptive retry scenario label
		outcomes []Outcome // ordered supplied exchange outcome fixtures
		// calls is the expected exchange count, completed the usage capture count,
		// and warnings the missing-usage count. Each result permits at most one
		// completion path, and Run releases every acquired provider lease.
		calls, completed, warnings int
	}{
		{"switch", []Outcome{failure, {Result: upstream.AttemptResult{Served: true, HasUsage: true}}}, 2, 1, 0},
		{"missing usage", []Outcome{{Result: upstream.AttemptResult{Served: true}, Err: errors.New("write"), Failure: &failover.FailureInfo{RetryNext: true}}}, 1, 0, 1},
		{"write failed", []Outcome{{Result: upstream.AttemptResult{Served: true, HasUsage: true}, Err: errors.New("write"), Failure: &failover.FailureInfo{RetryNext: true}}}, 1, 1, 0},
		{"invalid response", []Outcome{{Err: errors.New("invalid")}}, 1, 0, 0},
		{"exhausted", []Outcome{failure, failure, failure}, 3, 0, 0},
		{"committed", []Outcome{{Result: upstream.AttemptResult{HTTPCommitted: true}, Err: errors.New("write"), Failure: &failover.FailureInfo{RetryNext: true}}}, 1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &runProbe{outcomes: tc.outcomes}
			_ = Run(context.Background(), 2, p)
			require.Equal(t, tc.calls, p.calls)
			require.Equal(t, tc.completed, p.completed)
			require.Equal(t, tc.warnings, p.warnings)
			require.Equal(t, tc.calls, p.released)
			require.Zero(t, p.slots)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &runProbe{}
	require.ErrorIs(t, Run(ctx, 2, p), context.Canceled)
	require.Zero(t, p.calls)
}

// TestRunPoolRetries supplies same-provider failures with a seven-second retry
// delay and independent switch allowances. Provider histories, completion
// counts, released leases, and elapsed time must match each recovery scenario.
func TestRunPoolRetries(t *testing.T) {
	failure := Outcome{Err: errors.New("busy"), Failure: &failover.FailureInfo{RetryNext: true, RetryableOnSameProvider: true, SameProviderRetryDelay: 7 * time.Second}, RetryLimit: 2}
	success := Outcome{Result: upstream.AttemptResult{Served: true, HasUsage: true}}
	zero := failure
	zero.RetryLimit = 0
	committed := failure
	committed.Result.RetryCommitted = true
	served := failure
	served.Result = upstream.AttemptResult{Served: true, HasUsage: true}
	for _, tc := range []struct {
		name          string    // descriptive retry scenario label
		outcomes      []Outcome // ordered supplied exchange outcome fixtures
		maxSwitches   int       // permitted outer provider switch count
		wantProviders []int64   // expected exchanged provider identifier history
		// completed is the expected usage capture count and switches the outer
		// provider transition count. Same-provider retries consume delay while
		// keeping the current lease, so these counts remain independent.
		completed, switches int
		delay               time.Duration // expected accumulated retry wait duration
	}{
		{name: "retry succeeds with no switches", outcomes: []Outcome{failure, success}, wantProviders: []int64{1, 1}, completed: 1, delay: 7 * time.Second},
		{name: "exhausted", outcomes: []Outcome{failure, failure, failure}, wantProviders: []int64{1, 1, 1}, delay: 14 * time.Second},
		{name: "switch after retries", outcomes: []Outcome{failure, failure, failure, success}, maxSwitches: 1, wantProviders: []int64{1, 1, 1, 4}, completed: 1, switches: 1, delay: 14 * time.Second},
		{name: "zero retries", outcomes: []Outcome{zero}, wantProviders: []int64{1}},
		{name: "committed", outcomes: []Outcome{committed}, wantProviders: []int64{1}},
		{name: "served", outcomes: []Outcome{served}, wantProviders: []int64{1}, completed: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := &runProbe{outcomes: tc.outcomes}
				started := time.Now()
				_ = Run(context.Background(), tc.maxSwitches, p)
				require.Equal(t, tc.wantProviders, p.forwarded)
				require.Equal(t, tc.completed, p.completed)
				require.Equal(t, tc.switches, p.switches)
				require.Equal(t, tc.switches+1, p.released)
				require.Equal(t, tc.delay, time.Since(started))
				require.Zero(t, p.slots)
			})
		})
	}
}

// TestRunPoolRetryCancellation cancels the context during a same-provider
// backoff wait. Run must return cancellation after one exchange and release
// the held lease before another inference can begin.
func TestRunPoolRetryCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		p := &runProbe{cancel: cancel, outcomes: []Outcome{{Err: errors.New("busy"), Failure: &failover.FailureInfo{RetryNext: true, RetryableOnSameProvider: true}, RetryLimit: 2}}}
		require.ErrorIs(t, Run(ctx, 3, p), context.Canceled)
		require.Equal(t, 1, p.calls)
		require.Equal(t, 1, p.released)
		require.Zero(t, p.completed)
		require.Zero(t, p.slots)
	})
}
