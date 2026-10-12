package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWorkerRuntimeStartsOnceAndCannotRestart checks one startup per instance and permanent shutdown.
func TestWorkerRuntimeStartsOnceAndCannotRestart(t *testing.T) {
	var runtime WorkerRuntime
	var calls atomic.Int64
	started := make(chan struct{}, 4)
	task := RuntimeTask{Name: "initial", Run: func(context.Context) { calls.Add(1); started <- struct{}{} }}
	runtime.Start(task)
	<-started
	runtime.Start(task)
	require.NoError(t, runtime.StopContext(context.Background()))
	runtime.Start(task)
	require.EqualValues(t, 1, calls.Load())

	var stopped WorkerRuntime
	require.NoError(t, stopped.StopContext(context.Background()))
	stopped.Start(task)
	require.EqualValues(t, 1, calls.Load())
}

// TestWorkerRuntimeStopCancelsAndWaits checks cancellation before waiting for task completion.
func TestWorkerRuntimeStopCancelsAndWaits(t *testing.T) {
	var runtime WorkerRuntime
	started := make(chan struct{})
	var completed atomic.Bool
	runtime.Start(RuntimeTask{Name: "poll", Run: func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		completed.Store(true)
	}})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.StopContext(ctx))
	require.True(t, completed.Load())
}

// TestWorkerRuntimeStopTimeoutRemainsUnfinished checks pending task names and reuse of the first shutdown error.
func TestWorkerRuntimeStopTimeoutRemainsUnfinished(t *testing.T) {
	var runtime WorkerRuntime
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	runtime.Start(RuntimeTask{Name: "blocked-store", Run: func(context.Context) {
		close(started)
		<-release
		close(finished)
	}})
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runtime.StopContext(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "blocked-store")
	close(release)
	<-finished
	require.Same(t, err, runtime.StopContext(context.Background()))
}
