package backup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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

func TestStopCancelsWarmup(t *testing.T) {
	repo := &runtimeSettings{entered: make(chan struct{})}
	s := runtimeBackup(repo, &runtimeArchive{})
	started := make(chan struct{})
	go func() { _ = s.StartContext(context.Background()); close(started) }()
	<-repo.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, s.StopContext(ctx))
	<-started
	require.True(t, repo.cancelled.Load())
}

// TestStopBudget checks that later Stop calls return the same timeout from a blocked operation.
func TestStopBudget(t *testing.T) {
	s := runtimeBackup(&runtimeSettings{}, &runtimeArchive{})
	_, done, err := s.begin(context.Background())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	first := s.StopContext(ctx)
	require.ErrorIs(t, first, context.DeadlineExceeded)
	require.Equal(t, first, s.StopContext(context.Background()))
	done()
}

// TestStoredCronFailureDegrades checks that invalid stored cron settings still permit startup and shutdown.
func TestStoredCronFailureDegrades(t *testing.T) {
	repo := &runtimeSettings{values: map[string]string{settingKeyBackupSchedule: `{"enabled":true,"cron_expr":"not-a-cron"}`}}
	s := runtimeBackup(repo, &runtimeArchive{})
	require.NoError(t, s.StartContext(context.Background()))
	require.NoError(t, s.StopContext(context.Background()))
}

// TestCleanupUsesShutdownBudget checks that shutdown cancels cleanup already in progress.
func TestCleanupUsesShutdownBudget(t *testing.T) {
	s := runtimeBackup(&runtimeSettings{}, &runtimeArchive{})
	cleanup, cancelCleanup := s.cleanupContext(time.Hour)
	defer cancelCleanup()
	budget, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	s.BeginStopContext(budget)
	select {
	case <-cleanup.Done():
		require.Error(t, cleanup.Err())
	case <-time.After(time.Second):
		t.Fatal("清理没有接入停止预算")
	}
	s.Stop()
}
