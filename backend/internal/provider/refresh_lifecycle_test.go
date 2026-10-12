package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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

func TestRefreshStopTimeoutDoesNotReportDrainOrPersistLateResult(t *testing.T) {
	repo := &lifecycleRefreshRepository{}
	executor := &lifecycleRefreshExecutor{started: make(chan struct{}), release: make(chan struct{}), ignoreCancel: true}
	api := NewOAuthRefreshAPI(repo, nil, RefreshOptions{})
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
	// Retain the first timeout result even after the unfinished task exits.
	require.Same(t, err, api.StopContext(context.Background()))
}

// TestRefreshLockRejectsStoppedOwnerBeforeCancellationArrives checks that the stop barrier rejects work before
// cancellation reaches each waiter.
func TestRefreshLockRejectsStoppedOwnerBeforeCancellationArrives(t *testing.T) {
	api := NewOAuthRefreshAPI(&lifecycleRefreshRepository{}, nil, RefreshOptions{})
	ctx, finish, err := api.beginRefresh(context.Background())
	require.NoError(t, err)
	cancelEntered := make(chan struct{})
	allowCancel := make(chan struct{})
	defer close(allowCancel)
	defer finish()

	// Hold cancellation propagation open to exercise the stop barrier.
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
	// Rejecting the exchange must also release the local lock.
	lock := api.getLocalLock("lifecycle:provider")
	require.NoError(t, lock.Lock(stopCtx))
	lock.Unlock()
}
