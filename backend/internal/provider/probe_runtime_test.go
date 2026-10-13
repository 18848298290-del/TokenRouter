package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A shared probe has a different lifetime from each caller waiting for it. Two
// callers with the same key use one cancellable callback, while cancelling a
// waiter leaves that callback available until the runtime stops. The tests
// inspect registered activities to establish that overlap before triggering
// cancellation.
//
// Background callbacks can also ignore cancellation and remain blocked after the
// shutdown budget expires. A release channel holds such a callback independently
// of its context, so the test can require duplicate-key rejection and a retained
// timeout without mistaking cancellation delivery for completed work. Cleanup
// releases the blocked callback after the assertions and waits for its exit
// signal.

// TestProbeRuntimeSharedCancellationAndStop overlaps two same-key waiters and
// cancels their initiating caller. It verifies one shared callback remains
// active until shutdown, both waits observe cancellation, and the stopped
// runtime rejects later work.
func TestProbeRuntimeSharedCancellationAndStop(t *testing.T) {
	var runtime ProbeRuntime
	var calls atomic.Int32
	entered, exited := make(chan struct{}), make(chan struct{})
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	// Signal callback entry and exit around its shared-context wait. Cancelling the
	// caller after entry should leave exit unreported until StopContext cancels
	// that separate operation.
	probe := func(ctx context.Context) (any, error) {
		calls.Add(1)
		close(entered)
		defer close(exited)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	// Start one caller first and wait for callback entry before adding another
	// waiter, so the second request joins an existing flight rather than replacing
	// it.
	go func() { _, err := runtime.Run(caller, "same", time.Minute, probe); done <- err }()
	<-entered
	go func() { _, err := runtime.Run(context.Background(), "same", time.Minute, probe); done <- err }()
	// Three registrations establish both waiter lifetimes and the shared callback
	// before cancellation. This makes survival of shared work observable
	// independently of goroutine startup timing.
	require.Eventually(t, func() bool {
		runtime.activity.mu.Lock()
		defer runtime.activity.mu.Unlock()
		return len(runtime.activity.active) == 3
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	select {
	case <-exited:
		t.Fatal("等待者取消结束了共享工作")
	default:
	}
	// Bound the shutdown wait so a missing completion signal fails the test instead
	// of blocking indefinitely.
	budget, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	require.NoError(t, runtime.StopContext(budget))
	require.ErrorIs(t, <-done, context.Canceled)
	require.Equal(t, int32(1), calls.Load())
	_, err := runtime.Run(context.Background(), "later", time.Minute, probe)
	require.ErrorIs(t, err, ErrProbeStopped)
	require.False(t, runtime.Schedule("later", time.Minute, func(context.Context) { t.Error("停止后启动") }))
}

// TestProbeRuntimeBackgroundStopReportsUnfinished schedules a callback held by a
// release channel despite context cancellation. It verifies duplicate-key
// rejection and that a subsequent StopContext returns the same unfinished-work
// timeout error.
func TestProbeRuntimeBackgroundStopReportsUnfinished(t *testing.T) {
	var runtime ProbeRuntime
	entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// Keep the callback blocked through both stop assertions. Cleanup releases it
	// and waits for its deferred exit signal, which precedes the runtime wrapper's
	// key deletion and activity completion.
	t.Cleanup(func() { close(release); <-exited })
	// Signal entry before waiting for the independent release channel, and signal
	// exit before cleanup can finish its join. The held return exposes unfinished
	// background work.
	require.True(t, runtime.Schedule("models", time.Minute, func(context.Context) {
		close(entered)
		defer close(exited)
		<-release
	}))
	<-entered
	require.False(t, runtime.Schedule("models", time.Minute, func(context.Context) { t.Error("同 key 重复执行") }))
	// Expire the wait while the release channel still holds the callback, exposing
	// an unfinished operation rather than a cooperative cancellation.
	budget, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := runtime.StopContext(budget)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "unfinished")
	require.Same(t, err, runtime.StopContext(context.Background()))
}
