package provider

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Concurrent startup must claim one periodic loop, and shutdown needs to
// distinguish context delivery from a cycle's actual exit. These fixtures hold
// the first cycle through cancellation, reject startup after stopping, and
// observe the retained timeout when a callback ignores cancellation.

// TestRefreshLoopConcurrentStartAndBoundedStop starts one loop from twelve
// concurrent callers while its first cycle waits for cancellation. Shutdown
// joins that cycle, exactly one caller owns startup, and subsequent starts
// remain rejected.
func TestRefreshLoopConcurrentStartAndBoundedStop(t *testing.T) {
	var cycles, starts atomic.Int32
	entered := make(chan struct{})
	finished := make(chan struct{})
	// The initial cycle signals entry and exit around its cancellation wait so
	// joined shutdown can be distinguished from simply setting the context flag.
	loop := NewRefreshLoop(func(ctx context.Context) { cycles.Add(1); close(entered); <-ctx.Done(); close(finished) })
	require.Zero(t, cycles.Load())
	// Join competing starts before counting ownership, while the first cycle
	// remains blocked and the long interval cannot add another cycle observation.
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			started, err := loop.StartContext(context.Background(), time.Hour)
			if err != nil {
				t.Error(err)
			}
			if started {
				starts.Add(1)
			}
		})
	}
	wg.Wait()
	waitUsageSignal(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, loop.StopContext(ctx))
	waitUsageSignal(t, finished)
	require.Equal(t, int32(1), cycles.Load())
	require.Equal(t, int32(1), starts.Load())
	started, err := loop.StartContext(context.Background(), time.Hour)
	require.NoError(t, err)
	require.False(t, started)
	require.NoError(t, loop.StopContext(ctx))
}

// TestRefreshLoopCannotStartAfterStop stops an unstarted loop before requesting
// startup. The later start returns false with no error, and its callback would
// fail the test if invoked.
func TestRefreshLoopCannotStartAfterStop(t *testing.T) {
	loop := NewRefreshLoop(func(context.Context) { t.Error("停止后开始扫描") })
	require.NoError(t, loop.StopContext(context.Background()))
	started, err := loop.StartContext(context.Background(), time.Hour)
	require.NoError(t, err)
	require.False(t, started)
}

// TestRefreshLoopStopReportsIgnoredCancellation blocks a cycle on a release
// channel instead of its context. Stop returns a timeout identifying token
// refresh, and a repeated call retains that same error after release is
// signaled.
func TestRefreshLoopStopReportsIgnoredCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	// Hold actual cycle completion independently of cancellation so the stop budget
	// expires with tracked work still active; release later permits it to return.
	loop := NewRefreshLoop(func(context.Context) { close(entered); <-release })
	started, err := loop.StartContext(context.Background(), time.Hour)
	require.True(t, started)
	require.NoError(t, err)
	waitUsageSignal(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err = loop.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "token refresh")
	close(release)
	require.Same(t, err, loop.StopContext(context.Background()))
}

// TestRefreshLoopCancelledStartDoesNotClaim starts with an already canceled
// parent context. Startup returns cancellation and false, and stop succeeds
// after the rejected registration without running the callback.
func TestRefreshLoopCancelledStartDoesNotClaim(t *testing.T) {
	loop := NewRefreshLoop(func(context.Context) { t.Error("取消的启动产生扫描") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started, err := loop.StartContext(ctx, time.Hour)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, started)
	require.NoError(t, loop.StopContext(context.Background()))
}
