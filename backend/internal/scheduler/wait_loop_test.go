package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextBackoff_ExponentialGrowth(t *testing.T) {
	// Jitter prevents exact interval assertions, so check the bounds throughout repeated backoff.
	current := InitialBackoff // 100ms

	for i := range 10 {
		next := NextBackoff(current)

		assert.GreaterOrEqual(t, int64(next), int64(InitialBackoff),
			"第 %d 次退避不应低于初始值 %v", i, InitialBackoff)
		assert.LessOrEqual(t, int64(next), int64(MaxBackoff),
			"第 %d 次退避不应超过最大值 %v", i, MaxBackoff)

		current = next
	}
}

func TestNextBackoff_BoundedByMaxBackoff(t *testing.T) {
	// Inputs above the cap remain bounded after jitter.
	for range 100 {
		result := NextBackoff(10 * time.Second)
		assert.LessOrEqual(t, int64(result), int64(MaxBackoff),
			"退避值不应超过 MaxBackoff")
	}
}

func TestNextBackoff_BoundedByInitialBackoff(t *testing.T) {
	// Inputs below the initial interval remain bounded after jitter.
	for range 100 {
		result := NextBackoff(1 * time.Millisecond)
		assert.GreaterOrEqual(t, int64(result), int64(InitialBackoff),
			"退避值不应低于 InitialBackoff")
	}
}

func TestNextBackoff_HasJitter(t *testing.T) {
	// Repeated calls with identical input should produce distinct intervals.
	results := make(map[time.Duration]bool)
	current := 500 * time.Millisecond

	for range 50 {
		result := NextBackoff(current)
		results[result] = true
	}

	require.Greater(t, len(results), 1,
		"NextBackoff 应产生随机抖动，但所有 50 次调用结果相同")
}

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

func TestNextBackoff_ConvergesToMaxBackoff(t *testing.T) {
	current := InitialBackoff
	for range 20 {
		current = NextBackoff(current)
	}

	// After 20 iterations, the interval should reach the cap's jitter range.
	lowerBound := time.Duration(float64(MaxBackoff) * 0.8)
	assert.GreaterOrEqual(t, int64(current), int64(lowerBound),
		"经过多次退避后应收敛到 MaxBackoff 附近")
}

func BenchmarkNextBackoff(b *testing.B) {
	current := InitialBackoff
	for range b.N {
		current = NextBackoff(current)
		if current > MaxBackoff {
			current = InitialBackoff
		}
	}
}

func TestSlotWaitStopCancelsObserverLoop(t *testing.T) {
	core := NewConcurrencyService(&runtimeSlotCache{})
	waiting := make(chan struct{})
	finished := make(chan error, 1)
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

func TestSlotWaitObserverFailureDoesNotAcquire(t *testing.T) {
	core := NewConcurrencyService(&runtimeSlotCache{})
	failed := errors.New("下游心跳写失败")
	_, err := core.WaitForSlot(context.Background(), "provider", 1, 1, time.Second, false, WaitObserver{Interval: time.Millisecond, Heartbeat: func() error { return failed }})
	require.ErrorIs(t, err, failed)
	require.NoError(t, core.StopContext(context.Background()))
}
