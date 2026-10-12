package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Shutdown cancellation can reach an exchange after another caller has queued on
// its provider lock. These fixtures join canceled operations, release a callback
// that ignores cancellation to check late writes, and delay cancellation itself
// so the separately published stop barrier can be tested.

// TestRefreshStopCancelsExchangeAndQueuedWork registers one active exchange and
// one queued caller before stopping. Both return cancellation, only the first
// exchanges, no credentials are written, and later refresh is rejected.
func TestRefreshStopCancelsExchangeAndQueuedWork(t *testing.T) {
	repo := &lifecycleRefreshRepository{}
	executor := &lifecycleRefreshExecutor{started: make(chan struct{}), release: make(chan struct{})}
	api := NewOAuthRefreshAPI(repo, nil, RefreshOptions{})
	require.Zero(t, executor.calls.Load())
	results := make(chan error, 2)
	refresh := func() {
		_, err := api.RefreshIfNeeded(context.Background(), &Record{ID: 1}, executor, time.Minute)
		results <- err
	}
	// Enter one exchange before queuing the second caller, then await both
	// registrations so stopping exercises the owner and its waiting lock user.
	go refresh()
	<-executor.started
	go refresh()
	require.Eventually(t, func() bool {
		api.activity.mu.Lock()
		defer api.activity.mu.Unlock()
		return len(api.activity.active) == 2
	}, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, api.StopContext(ctx))
	for range 2 {
		require.ErrorIs(t, <-results, context.Canceled)
	}
	require.Equal(t, int32(1), executor.calls.Load())
	require.Zero(t, repo.writes.Load())
	_, err := api.RefreshIfNeeded(context.Background(), &Record{ID: 1}, executor, time.Minute)
	require.ErrorIs(t, err, ErrRefreshStopped)
	require.NoError(t, api.StopContext(context.Background()))
}

// TestRefreshStopTimeoutDoesNotReportDrainOrPersistLateResult holds an exchange
// that ignores shutdown cancellation beyond its budget. Stop reports unfinished
// work, the joined late response cannot persist credentials, and subsequent stop
// returns that same timeout.
func TestRefreshStopTimeoutDoesNotReportDrainOrPersistLateResult(t *testing.T) {
	repo := &lifecycleRefreshRepository{}
	executor := &lifecycleRefreshExecutor{started: make(chan struct{}), release: make(chan struct{}), ignoreCancel: true}
	api := NewOAuthRefreshAPI(repo, nil, RefreshOptions{})
	// The executor ignores cancellation until released. Join its eventual result
	// after the stop timeout before checking that no credential write occurred.
	finished := make(chan error, 1)
	go func() {
		_, err := api.RefreshIfNeeded(context.Background(), &Record{ID: 1}, executor, time.Minute)
		finished <- err
	}()
	<-executor.started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := api.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "unfinished")
	close(executor.release)
	require.ErrorIs(t, <-finished, context.Canceled)
	require.Zero(t, repo.writes.Load())
	// The joined exchange has exited, but the retained timeout still describes
	// what the first shutdown call observed and cannot become a later success.
	require.Same(t, err, api.StopContext(context.Background()))
}

// TestRefreshLockRejectsStoppedOwnerBeforeCancellationArrives delays
// cancellation after the stop flag is published. Acquiring the lock returns
// cancellation despite a live operation context, and a later acquisition proves
// that rejection returned the local token.
func TestRefreshLockRejectsStoppedOwnerBeforeCancellationArrives(t *testing.T) {
	api := NewOAuthRefreshAPI(&lifecycleRefreshRepository{}, nil, RefreshOptions{})
	ctx, finish, err := api.beginRefresh(context.Background())
	require.NoError(t, err)
	cancelEntered := make(chan struct{})
	allowCancel := make(chan struct{})
	defer close(allowCancel)
	defer finish()

	// Replace this registered cancel callback under the activity mutex so
	// StopContext publishes stopped before delivery reaches ctx. The lock path must
	// consult that barrier independently of the still-live operation context.
	api.activity.mu.Lock()
	for id, cancel := range api.activity.active {
		api.activity.active[id] = func() {
			close(cancelEntered)
			<-allowCancel
			cancel()
		}
	}
	api.activity.mu.Unlock()
	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	stopped := make(chan error, 1)
	go func() {
		stopped <- api.StopContext(stopCtx)
	}()
	t.Cleanup(func() {
		require.NoError(t, <-stopped)
	})
	<-cancelEntered
	require.NoError(t, ctx.Err(), "等待者的逐项取消尚未执行")

	release, held, err := api.acquireRefreshLock(ctx, 1, "lifecycle:provider")
	if release != nil {
		release()
	}
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, held)
	// Reacquire after rejection to prove the stop check returned its local token;
	// otherwise this wait would reach the stop context deadline instead of
	// succeeding.
	lock := api.getLocalLock("lifecycle:provider")
	require.NoError(t, lock.Lock(stopCtx))
	lock.Unlock()
}
