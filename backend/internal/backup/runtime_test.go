package backup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRepeatedStartAndStop repeats startup around a completed shutdown. The
// same cron instance must remain installed, a post-stop start must not recreate
// it, and a new asynchronous backup must be rejected after that lifetime ends.
func TestRepeatedStartAndStop(t *testing.T) {
	s := runtimeBackup(&runtimeSettings{}, &runtimeArchive{})
	require.NoError(t, s.StartContext(context.Background()))
	first := s.cronSched
	require.NoError(t, s.StartContext(context.Background()))
	require.Same(t, first, s.cronSched)
	require.NoError(t, s.StopContext(context.Background()))
	require.NoError(t, s.StartContext(context.Background()))
	require.Same(t, first, s.cronSched)
	_, err := s.StartBackup(context.Background(), "manual", 1)
	require.Error(t, err)
}

// TestStopCancelsWarmup holds initial record loading inside the settings fixture.
// Shutdown must cancel that background read and finish within its waiting budget,
// while the startup goroutine completes after observing service cancellation.
func TestStopCancelsWarmup(t *testing.T) {
	repo := &runtimeSettings{entered: make(chan struct{})}
	s := runtimeBackup(repo, &runtimeArchive{})
	started := make(chan struct{})
	// Keep startup asynchronous and wait for its fixture's read-entry signal before
	// stopping, so the cancellation assertion describes active warmup rather than an already idle service.
	go func() { _ = s.StartContext(context.Background()); close(started) }()
	<-repo.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, s.StopContext(ctx))
	<-started
	require.True(t, repo.cancelled.Load())
}

// TestStopBudget admits work that remains incomplete past the first stop budget.
// StopContext must return deadline expiration, and a later caller must receive
// the same stored error even with an unlimited waiting context.
func TestStopBudget(t *testing.T) {
	s := runtimeBackup(&runtimeSettings{}, &runtimeArchive{})
	_, done, err := s.begin(context.Background())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	first := s.StopContext(ctx)
	require.ErrorIs(t, first, context.DeadlineExceeded)
	require.Equal(t, first, s.StopContext(context.Background()))
	// Release the tracked operation after observing the shared timeout so the
	// background waiter can finish, while retaining the stop result already returned to callers.
	done()
}

// TestStoredCronFailureDegrades supplies an enabled schedule with invalid cron
// text. Startup and shutdown must both return success, protecting the service's
// decision to report schedule application failure while keeping its lifetime available.
func TestStoredCronFailureDegrades(t *testing.T) {
	repo := &runtimeSettings{values: map[string]string{settingKeyBackupSchedule: `{"enabled":true,"cron_expr":"not-a-cron"}`}}
	s := runtimeBackup(repo, &runtimeArchive{})
	require.NoError(t, s.StartContext(context.Background()))
	require.NoError(t, s.StopContext(context.Background()))
}

// TestCleanupUsesShutdownBudget creates cleanup before an application shutdown
// budget is adopted. The shorter budget must cancel that existing cleanup context,
// and service shutdown must still finish afterward.
func TestCleanupUsesShutdownBudget(t *testing.T) {
	s := runtimeBackup(&runtimeSettings{}, &runtimeArchive{})
	cleanup, cancelCleanup := s.cleanupContext(time.Hour)
	defer cancelCleanup()
	budget, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	s.BeginStopContext(budget)
	// Observe cleanup cancellation after budget adoption rather than cancelling its
	// own completion function first, proving that already registered cleanup receives the shutdown deadline.
	select {
	case <-cleanup.Done():
		require.Error(t, cleanup.Err())
	case <-time.After(time.Second):
		t.Fatal("清理没有接入停止预算")
	}
	s.Stop()
}
