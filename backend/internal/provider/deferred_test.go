package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/timingwheel"
)

// deferredDrainRepository blocks writes to test serialization of periodic and final flushes without losing queued activity.
type deferredDrainRepository struct {
	DeferredRepository
	mu               sync.Mutex
	calls            int
	entered, release chan struct{}
	err              error
}

// deferredBlockedRepository ignores cancellation to test bounded shutdown while a write remains blocked.
type deferredBlockedRepository struct{ entered, release chan struct{} }

func (r *deferredDrainRepository) BatchUpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if call == 1 && r.entered != nil {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.err
}

func TestDeferredStopSerializesFinalFlush(t *testing.T) {
	wheel, err := NewTimingWheelService()
	require.NoError(t, err)
	require.NoError(t, wheel.Start())
	defer wheel.Stop()
	repo := &deferredDrainRepository{entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewDeferredService(repo, wheel, DeferredOptions{Interval: time.Hour})
	svc.Start()
	svc.ScheduleLastUsedUpdate(1)
	flushed := make(chan struct{})
	go func() { svc.flushLastUsed(); close(flushed) }()
	<-repo.entered
	svc.ScheduleLastUsedUpdate(2)
	stopped := make(chan error, 1)
	go func() { stopped <- svc.StopContext(context.Background()) }()
	select {
	case <-stopped:
		t.Fatal("周期写回未结束")
	case <-time.After(20 * time.Millisecond):
	}
	close(repo.release)
	<-flushed
	require.NoError(t, <-stopped)
	require.NoError(t, svc.StopContext(context.Background()))
	svc.Start()
	repo.mu.Lock()
	require.Equal(t, 2, repo.calls)
	repo.mu.Unlock()
}

func TestDeferredFinalFlushReportsFailure(t *testing.T) {
	failure := errors.New("database unavailable")
	repo := &deferredDrainRepository{err: failure}
	wheel, err := NewTimingWheelService()
	require.NoError(t, err)
	svc := NewDeferredService(repo, wheel, DeferredOptions{Interval: time.Hour})
	svc.ScheduleLastUsedUpdate(1)
	require.ErrorIs(t, svc.StopContext(context.Background()), failure)
	require.ErrorIs(t, svc.StopContext(context.Background()), failure)
	_, retained := svc.lastUsedUpdates.Load(int64(1))
	require.True(t, retained)
}

// NewTimingWheelService constructs a timing wheel; tests start it explicitly.
func NewTimingWheelService() (*timingwheel.Wheel, error) { return timingwheel.New(), nil }

func (r *deferredBlockedRepository) BatchUpdateLastUsed(context.Context, map[int64]time.Time) error {
	close(r.entered)
	<-r.release
	return nil
}

func TestDeferredStopBudgetKeepsUnfinishedBatch(t *testing.T) {
	repo := &deferredBlockedRepository{entered: make(chan struct{}), release: make(chan struct{})}
	wheel, err := NewTimingWheelService()
	require.NoError(t, err)
	svc := NewDeferredService(repo, wheel, DeferredOptions{Interval: time.Hour})
	svc.ScheduleLastUsedUpdate(1)
	flushed := make(chan error, 1)
	go func() { flushed <- svc.flushLastUsedErr() }()
	<-repo.entered
	svc.ScheduleLastUsedUpdate(2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = svc.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	svc.ScheduleLastUsedUpdate(3)
	_, retained := svc.lastUsedUpdates.Load(int64(2))
	require.True(t, retained)
	_, rejected := svc.lastUsedUpdates.Load(int64(3))
	require.False(t, rejected)
	close(repo.release)
	require.NoError(t, <-flushed)
	require.ErrorIs(t, svc.StopContext(context.Background()), context.DeadlineExceeded)
}

// TestDeferredFailureKeepsNewerPendingActivity protects newer queued activity when an older batch fails.
func TestDeferredFailureKeepsNewerPendingActivity(t *testing.T) {
	repo := &deferredDrainRepository{entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("forced write failure")}
	wheel, err := NewTimingWheelService()
	require.NoError(t, err)
	svc := NewDeferredService(repo, wheel, DeferredOptions{Interval: time.Hour})
	svc.lastUsedUpdates.Store(int64(1), time.Unix(1, 0))
	flushed := make(chan error, 1)
	go func() { flushed <- svc.flushLastUsedErr() }()
	<-repo.entered
	svc.ScheduleLastUsedUpdate(1)
	expected, ok := svc.lastUsedUpdates.Load(int64(1))
	require.True(t, ok)
	close(repo.release)
	require.Error(t, <-flushed)
	actual, ok := svc.lastUsedUpdates.Load(int64(1))
	require.True(t, ok)
	require.Equal(t, expected, actual)
}
