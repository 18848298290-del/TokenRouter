package live

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// TestObserverStopCancelsAndWaitsForEnteredTask starts one registered observer
// while Stop runs concurrently. Shutdown must cancel its context, reject a
// later owner, and wait until the accepted cleanup releases its task count.
func TestObserverStopCancelsAndWaitsForEnteredTask(t *testing.T) {
	state := &ObserverManager{}
	run, finish, ok := state.Begin("observer")
	require.True(t, ok)
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stopDone := make(chan error, 1)
	// Run stopping separately while holding the accepted observer cleanup, then
	// observe cancellation before completing cleanup to distinguish notification
	// from finished shutdown waiting.
	go func() { stopDone <- state.Stop(stopCtx) }()
	select {
	case <-run.Done():
	case <-stopCtx.Done():
		t.Fatal("observer context not cancelled")
	}
	select {
	case <-stopDone:
		t.Fatal("stop completed before observer returned")
	default:
	}
	_, _, accepted := state.Begin("late")
	require.False(t, accepted)
	finish()
	require.NoError(t, <-stopDone)
	require.NoError(t, state.Stop(context.Background()))
}

// TestObserverStopTimeoutLeavesCleanupPending keeps an admitted observer alive
// past the shutdown deadline. Stop returns that deadline error, and another
// Stop can finish after the caller performs its pending cleanup.
func TestObserverStopTimeoutLeavesCleanupPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := &ObserverManager{}
		run, finish, ok := state.Begin("observer")
		require.True(t, ok)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		require.ErrorIs(t, state.Stop(ctx), context.DeadlineExceeded)
		require.ErrorIs(t, run.Err(), context.Canceled)
		late, cleanup, accepted := state.Begin("late")
		require.False(t, accepted)
		require.Nil(t, late)
		require.Nil(t, cleanup)
		finish()
		require.NoError(t, state.Stop(context.Background()))
	})
}

// TestObserverCleanupRequiresOneCall releases an admitted observer before
// shutdown and calls its cleanup again. The second call panics because cleanup
// decrements the accepted task count on every invocation.
func TestObserverCleanupRequiresOneCall(t *testing.T) {
	state := &ObserverManager{}
	run, finish, ok := state.Begin("observer")
	require.True(t, ok)
	finish()
	require.ErrorIs(t, run.Err(), context.Canceled)
	require.PanicsWithValue(t, "sync: negative WaitGroup counter", finish)
}

// TestObserverStopBeforeRegistration closes an empty registry before any task
// enters it. Repeated shutdown succeeds and later registration returns no
// context or cleanup function.
func TestObserverStopBeforeRegistration(t *testing.T) {
	state := &ObserverManager{}
	require.NoError(t, state.Stop(context.Background()))
	require.NoError(t, state.Stop(context.Background()))
	run, finish, ok := state.Begin("")
	require.False(t, ok)
	require.Nil(t, run)
	require.Nil(t, finish)
}

// TestObserverEmptyOwnerUsesDetachedContext registers an empty owner with a
// zero-value manager. Admission returns a context with no deadline, and cleanup
// cancels that context before the empty registry stops successfully.
func TestObserverEmptyOwnerUsesDetachedContext(t *testing.T) {
	state := &ObserverManager{}
	run, finish, ok := state.Begin("")
	require.True(t, ok)
	require.NoError(t, run.Err())
	_, hasDeadline := run.Deadline()
	require.False(t, hasDeadline)
	finish()
	require.ErrorIs(t, run.Err(), context.Canceled)
	require.NoError(t, state.Stop(context.Background()))
}

// TestObserverConcurrentStopsWaitForCleanup starts two shutdown calls for one
// admitted observer. Both calls cancel that observer and stay blocked until
// its single cleanup releases the shared completion count.
func TestObserverConcurrentStopsWaitForCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := &ObserverManager{}
		run, finish, ok := state.Begin("observer")
		require.True(t, ok)
		stopped := make(chan error, 2)
		// Wait until both shutdown callers are blocked before releasing cleanup,
		// which distinguishes observer cancellation from the completion each caller awaits.
		for range 2 {
			go func() {
				stopped <- state.Stop(context.Background())
			}()
		}
		synctest.Wait()
		require.ErrorIs(t, run.Err(), context.Canceled)
		require.Empty(t, stopped)
		finish()
		require.NoError(t, <-stopped)
		require.NoError(t, <-stopped)
	})
}

// TestObserverStopNilContextClosesAdmission passes nil while an observer is
// registered. Stop cancels the observer and closes admission before panicking
// when it reads the supplied context.
func TestObserverStopNilContextClosesAdmission(t *testing.T) {
	state := &ObserverManager{}
	run, finish, ok := state.Begin("observer")
	require.True(t, ok)
	require.Panics(t, func() {
		_ = state.Stop(nil) //nolint:staticcheck // Exercise shutdown's nil-context panic after cancellation.
	})
	require.ErrorIs(t, run.Err(), context.Canceled)
	late, cleanup, accepted := state.Begin("late")
	require.False(t, accepted)
	require.Nil(t, late)
	require.Nil(t, cleanup)
	finish()
	require.NoError(t, state.Stop(context.Background()))
}
