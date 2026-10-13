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

// Activity batching can overlap a provider's newer queued timestamp or a
// shutdown deadline. Blocking repository doubles make those interleavings
// visible, so the tests inspect write serialization, retained failed batches,
// rejected post-stop admission, and the first shutdown result.

// deferredDrainRepository blocks writes to test serialization of periodic and
// final flushes without losing queued activity.
type deferredDrainRepository struct {
	DeferredRepository // embedded repository fixture interface
	// mu protects calls so the first-write barrier remains consistent while
	// periodic and final flushes compete to reach this repository double.
	mu    sync.Mutex
	calls int // observed repository write count
	// entered and release bracket the first repository call when configured.
	// The test waits for entered before adding activity or starting shutdown,
	// then closes release to let the blocked batch finish.
	entered, release chan struct{} // entry and completion barriers
	err              error         // configured repository write failure
}

// deferredBlockedRepository ignores cancellation to test bounded shutdown while
// a write remains blocked.
type deferredBlockedRepository struct {
	// entered and release bracket an intentionally uncooperative write. Tests
	// trigger shutdown after entered closes and release the writer after inspecting
	// the timeout result and retained pending queue.
	entered, release chan struct{} // blocked write lifecycle barriers
}

// BatchUpdateLastUsed counts writes and optionally blocks the first call on its
// release channel. That wait responds to ctx cancellation; after admission the
// double returns its configured database error.
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

// TestDeferredStopSerializesFinalFlush blocks a periodic write and queues
// another provider before stopping. Stop waits for that write, then performs
// the final batch; repeated stop and start calls keep the completed lifecycle
// fixed.
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
	// Hold the first batch inside persistence before stopping. The stop owner
	// must then wait for that flush lock and drain the separately queued provider.
	go func() { svc.flushLastUsed(); close(flushed) }()
	<-repo.entered
	svc.ScheduleLastUsedUpdate(2)
	stopped := make(chan error, 1)
	go func() { stopped <- svc.StopContext(context.Background()) }()
	select {
	case <-stopped:
		t.Fatal("周期写回未结束")
	// A short observation interval checks that shutdown remains blocked here.
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

// TestDeferredFinalFlushReportsFailure makes the final activity write return a
// database error. Stop retains that error across another call, and the failed
// provider timestamp remains queued.
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

// NewTimingWheelService constructs an unstarted timing wheel for deferred
// service tests.
func NewTimingWheelService() (*timingwheel.Wheel, error) { return timingwheel.New(), nil }

// BatchUpdateLastUsed signals that persistence has started and waits for
// release while ignoring cancellation. The test uses this unfinished call to
// show shutdown budgeting cannot complete an active uncooperative repository
// write.
func (r *deferredBlockedRepository) BatchUpdateLastUsed(context.Context, map[int64]time.Time) error {
	close(r.entered)
	<-r.release
	return nil
}

// TestDeferredStopBudgetKeepsUnfinishedBatch blocks a periodic write that
// ignores cancellation and times out shutdown. New admission is rejected, the
// later queued timestamp remains, and another stop returns the first timeout.
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

// TestDeferredFailureKeepsNewerPendingActivity queues newer activity while an
// older batch is blocked, then fails that write. Failure restoration leaves the
// newer pending timestamp in place.
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
