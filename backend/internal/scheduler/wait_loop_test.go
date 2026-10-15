package scheduler

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// delayedSlotCache models slow capacity checks before a configured successful poll.
type delayedSlotCache struct {
	stubConcurrencyCacheForTest     // inherited slot cleanup methods
	checks                      int // remaining denied capacity checks
}

// AcquireProviderSlot consumes cache latency before returning the next admission result.
func (c *delayedSlotCache) AcquireProviderSlot(context.Context, int64, int, string) (bool, error) {
	time.Sleep(time.Second)
	c.checks--
	return c.checks < 0, nil
}

// AcquireUserSlot gives user admission the same controlled cache latency.
func (c *delayedSlotCache) AcquireUserSlot(ctx context.Context, id int64, limit int, member string) (bool, error) {
	return c.AcquireProviderSlot(ctx, id, limit, member)
}

// TestWaitObservationExcludesCapacityChecks separates polling delay from slow
// cache calls for both queue kinds. Immediate acquisition has no queue interval,
// and a later successful poll reports only time spent waiting between checks.
func TestWaitObservationExcludesCapacityChecks(t *testing.T) {
	for _, kind := range []string{"user", "provider"} {
		for _, immediate := range []bool{false, true} {
			for _, denied := range []int{0, 1} {
				synctest.Test(t, func(t *testing.T) {
					cache := &delayedSlotCache{checks: denied}
					core := NewConcurrencyService(cache)
					var waited time.Duration
					ctx := WithWaitObservation(t.Context(), func(observed string, d time.Duration) {
						require.Equal(t, kind, observed)
						waited += d
					})
					start := time.Now()
					release, err := core.WaitForSlot(ctx, kind, 1, 1, time.Minute, immediate, WaitObserver{})
					require.NoError(t, err)
					release()
					require.Equal(t, time.Since(start)-time.Duration(denied+1)*time.Second, waited)
					if immediate && denied == 0 {
						require.Zero(t, waited)
					} else {
						require.GreaterOrEqual(t, waited, InitialBackoff)
					}
					require.NoError(t, core.StopContext(t.Context()))
				})
			}
		}
	}
}

// TestNextBackoff_ExponentialGrowth feeds each generated interval into the
// next jittered backoff call. It checks initial and maximum bounds throughout
// the sequence, without asserting that individual intervals grow
// monotonically.
func TestNextBackoff_ExponentialGrowth(t *testing.T) {
	// Jitter changes each exact interval. Chain returned values to exercise
	// their use as later inputs while checking the configured polling bounds.
	current := InitialBackoff

	for i := range 10 {
		next := NextBackoff(current)

		assert.GreaterOrEqual(t, int64(next), int64(InitialBackoff),
			"第 %d 次退避不应低于初始值 %v", i, InitialBackoff)
		assert.LessOrEqual(t, int64(next), int64(MaxBackoff),
			"第 %d 次退避不应超过最大值 %v", i, MaxBackoff)

		current = next
	}
}

// TestNextBackoff_BoundedByMaxBackoff samples backoff from an input already
// above the configured cap. Every result must remain at or below MaxBackoff
// after the jitter calculation.
func TestNextBackoff_BoundedByMaxBackoff(t *testing.T) {
	for range 100 {
		result := NextBackoff(10 * time.Second)
		assert.LessOrEqual(t, int64(result), int64(MaxBackoff),
			"退避值不应超过 MaxBackoff")
	}
}

// TestNextBackoff_BoundedByInitialBackoff samples backoff from an input below
// the configured initial interval. Every result must remain at or above
// InitialBackoff after jitter is applied.
func TestNextBackoff_BoundedByInitialBackoff(t *testing.T) {
	for range 100 {
		result := NextBackoff(1 * time.Millisecond)
		assert.GreaterOrEqual(t, int64(result), int64(InitialBackoff),
			"退避值不应低于 InitialBackoff")
	}
}

// TestNextBackoff_HasJitter repeats the same half-second backoff input across
// fifty calls. It requires more than one observed duration, checking that
// fixed input still produces varied intervals.
func TestNextBackoff_HasJitter(t *testing.T) {
	results := make(map[time.Duration]bool)
	current := 500 * time.Millisecond

	for range 50 {
		result := NextBackoff(current)
		results[result] = true
	}

	require.Greater(t, len(results), 1,
		"NextBackoff 应产生随机抖动，但所有 50 次调用结果相同")
}

// TestNextBackoff_InitialValueGrows chains one hundred backoff calls from the
// initial interval. It requires their average generated duration to exceed
// InitialBackoff, checking aggregate growth while allowing jitter between
// samples.
func TestNextBackoff_InitialValueGrows(t *testing.T) {
	current := InitialBackoff
	var sum time.Duration

	runs := 100
	for range runs {
		next := NextBackoff(current)
		sum += next
		current = next
	}

	avg := sum / time.Duration(runs)
	assert.Greater(t, int64(avg), int64(InitialBackoff),
		"平均退避时间应大于初始退避值")
}

// TestNextBackoff_ConvergesToMaxBackoff chains twenty jittered intervals from
// InitialBackoff. Its final duration must reach at least the configured cap's
// lower jitter band, protecting the sequence's eventual cap neighborhood.
func TestNextBackoff_ConvergesToMaxBackoff(t *testing.T) {
	current := InitialBackoff
	for range 20 {
		current = NextBackoff(current)
	}

	// Use the lower edge of the cap's jitter band as the acceptance bound.
	lowerBound := time.Duration(float64(MaxBackoff) * 0.8)
	assert.GreaterOrEqual(t, int64(current), int64(lowerBound),
		"经过多次退避后应收敛到 MaxBackoff 附近")
}

// BenchmarkNextBackoff chains generated backoff durations in a timed local
// loop. It includes jitter calculation and the loop's bound reset check while
// performing no real waits for the generated intervals.
func BenchmarkNextBackoff(b *testing.B) {
	current := InitialBackoff
	for range b.N {
		current = NextBackoff(current)
		if current > MaxBackoff {
			current = InitialBackoff
		}
	}
}

// TestSlotWaitStopCancelsObserverLoop starts a denied provider-slot wait and
// observes its Begin callback before shutdown. StopContext must join the loop
// successfully and the waiter must return context.Canceled.
func TestSlotWaitStopCancelsObserverLoop(t *testing.T) {
	core := NewConcurrencyService(&runtimeSlotCache{})
	waiting := make(chan struct{})
	finished := make(chan error, 1)
	// Begin signals that the wait loop has entered before shutdown cancels it.
	// Returning the error through a buffered channel lets Stop join the worker
	// independently of this receive.
	go func() {
		_, err := core.WaitForSlot(context.Background(), "provider", 1, 1, time.Hour, false, WaitObserver{Begin: func() error { close(waiting); return nil }})
		finished <- err
	}()
	<-waiting
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, core.StopContext(ctx))
	require.ErrorIs(t, <-finished, context.Canceled)
}

// TestSlotWaitObserverFailureDoesNotAcquire runs a denied slot wait whose
// heartbeat returns a specific error. It checks that error identity reaches
// the caller and that the service can subsequently stop successfully.
func TestSlotWaitObserverFailureDoesNotAcquire(t *testing.T) {
	core := NewConcurrencyService(&runtimeSlotCache{})
	failed := errors.New("下游心跳写失败")
	_, err := core.WaitForSlot(context.Background(), "provider", 1, 1, time.Second, false, WaitObserver{Interval: time.Millisecond, Heartbeat: func() error { return failed }})
	require.ErrorIs(t, err, failed)
	require.NoError(t, core.StopContext(context.Background()))
}

// TestWaitObservationReportsCancellation measures a real canceled polling interval.
// Queue diagnostics must survive cancellation without reporting a successful
// provider grant or changing the cancellation error returned by the scheduler.
func TestWaitObservationReportsCancellation(t *testing.T) {
	core := NewConcurrencyService(&runtimeSlotCache{})
	ctx, cancel := context.WithCancel(t.Context())
	var waited time.Duration
	ctx = WithWaitObservation(ctx, func(kind string, d time.Duration) { require.Equal(t, "provider", kind); waited = d })
	_, err := core.WaitForSlot(ctx, "provider", 1, 1, time.Second, false, WaitObserver{Begin: func() error { time.Sleep(time.Millisecond); cancel(); return nil }})
	require.ErrorIs(t, err, context.Canceled)
	require.GreaterOrEqual(t, waited, time.Millisecond)
	require.NoError(t, core.StopContext(t.Context()))
}
