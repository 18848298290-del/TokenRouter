package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWorkerRuntimeStartsOnceAndCannotRestart records one startup and issues
// repeated starts on used and stopped runtime instances. It checks successful
// shutdown and a call count of one at the two subsequent observations.
func TestWorkerRuntimeStartsOnceAndCannotRestart(t *testing.T) {
	var runtime WorkerRuntime
	var calls atomic.Int64
	// The first notification confirms startup before duplicate calls are
	// issued. Buffer unexpected additional notifications so the recorded count
	// can expose duplicate execution instead of blocking its task.
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

// TestWorkerRuntimeStopCancelsAndWaits starts work that waits for runtime
// cancellation before recording completion. StopContext must return
// successfully with the task's completion flag set, preserving
// cancellation-before-join behavior.
func TestWorkerRuntimeStopCancelsAndWaits(t *testing.T) {
	var runtime WorkerRuntime
	started := make(chan struct{})
	var completed atomic.Bool
	// The task announces entry before awaiting cancellation. Stop's successful
	// join must include the completion observation written after the runtime
	// context closes.
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

// TestWorkerRuntimeStopTimeoutRemainsUnfinished stops a task that ignores
// cancellation and waits on a test barrier. It checks the canceled stop cause
// and pending task name, then requires the identical error after receiving the
// callback's completion signal.
func TestWorkerRuntimeStopTimeoutRemainsUnfinished(t *testing.T) {
	var runtime WorkerRuntime
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// Hold work independently of runtime cancellation so the first stop
	// records an unfinished task. Releasing the barrier later must leave that
	// recorded stop error intact.
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
	// Let the blocked callback reach its completion signal after the first
	// Stop returns. The signal precedes runtime deregistration, while the
	// later Stop call tests retention of the original error.
	close(release)
	<-finished
	require.Same(t, err, runtime.StopContext(context.Background()))
}
