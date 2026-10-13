package provider

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOllamaUsageRuntimeStartsOnceAndStopsAllWork races ten starts with a
// blocking initial cycle and registers manual work. It asserts one cycle runs,
// stop cancels and waits for both paths, and later starts or admissions cannot
// restart work.
func TestOllamaUsageRuntimeStartsOnceAndStopsAllWork(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	runtime := NewOllamaUsageRuntime(func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			// Signal the first cycle after entry so manual work can
			// be
			// registered while that cycle still waits for
			// cancellation.
			close(entered)
		}
		// Keep the initial cycle active until runtime shutdown cancels
		// it;
		// stop must wait for this callback to return.
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	require.Zero(t, calls.Load())
	// Race independent startup callers before observing cycle completion,
	// exercising the shared startup flags through concurrent admission.
	var starters sync.WaitGroup
	for range 10 {
		starters.Go(func() { require.NoError(t, runtime.StartContext(context.Background())) })
	}
	starters.Wait()
	<-entered
	ctx, finish, err := runtime.Begin(context.Background())
	require.NoError(t, err)
	done := make(chan struct{})
	// Finish the separately registered operation only after cancellation
	// arrives, so shutdown must include manual work in its completion wait.
	go func() { <-ctx.Done(); finish(); close(done) }()
	budget, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.StopContext(budget))
	<-done
	require.Equal(t, int32(1), calls.Load())
	require.NoError(t, runtime.StartContext(context.Background()))
	require.Equal(t, int32(1), calls.Load())
	_, _, err = runtime.Begin(context.Background())
	require.ErrorIs(t, err, ErrOllamaUsageStopped)
	require.NoError(t, runtime.StopContext(context.Background()))
}

// TestOllamaUsageRuntimeRetainsIncompleteStopResult registers manual work that
// delays its finish until after a bounded stop. It checks cancellation is
// delivered, unfinished work yields a deadline error, and subsequent stop
// retains that exact result.
func TestOllamaUsageRuntimeRetainsIncompleteStopResult(t *testing.T) {
	runtime := NewOllamaUsageRuntime(func(context.Context) error { t.Fatal("停止后不得启动"); return nil }, nil)
	ctx, finish, err := runtime.Begin(context.Background())
	require.NoError(t, err)
	budget, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err = runtime.StopContext(budget)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "unfinished")
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	// Complete the activity after its first stop timed out; this reproduces
	// late completion while checking that the retained error stays
	// unchanged.
	finish()
	require.Same(t, err, runtime.StopContext(context.Background()))
	require.NoError(t, runtime.StartContext(context.Background()))
}
