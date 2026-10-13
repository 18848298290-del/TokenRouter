package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// qoderSessionTestProvider returns a fresh PAT-backed Qoder COSY record for
// lifecycle fixtures.
func qoderSessionTestProvider() *Record {
	return &Record{ID: 17, Platform: PlatformQoder, Type: ProviderTypeCosy, Credentials: map[string]any{"pat": "fixture"}}
}

// TestQoderSessionsCallerCancelKeepsSharedBuild cancels the first waiter while
// its builder remains blocked on a fixture channel. It checks another caller
// receives the same value after release and the shared callback ran once.
func TestQoderSessionsCallerCancelKeepsSharedBuild(t *testing.T) {
	var sessions QoderSessions[*int]
	entered, proceed := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	value := 42
	// entered observes the first callback and proceed releases its result;
	// the
	// shared context branch remains available for runtime stop
	// independently of
	// a waiter cancelling.
	build := func(ctx context.Context, _ *Record) (*int, time.Time, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-proceed:
			return &value, time.Time{}, nil
		case <-ctx.Done():
			return nil, time.Time{}, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// Observe the shared build's entry before cancelling this waiter;
	// releasing
	// proceed afterward checks that another caller can receive the same
	// completed value.
	go func() { _, err := sessions.GetSession(ctx, qoderSessionTestProvider(), build); done <- err }()
	<-entered
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	close(proceed)
	got, err := sessions.GetSession(context.Background(), qoderSessionTestProvider(), build)
	require.NoError(t, err)
	require.Same(t, &value, got)
	require.EqualValues(t, 1, calls.Load())
	require.NoError(t, sessions.StopContext(context.Background()))
}

// TestQoderSessionsStopCancelsAndRejectsNewWork starts a builder that returns
// only after shutdown cancellation. It asserts Stop waits for callback
// completion, reports an allowed cancellation/stopped result to the waiter, and
// rejects subsequent builds.
func TestQoderSessionsStopCancelsAndRejectsNewWork(t *testing.T) {
	var sessions QoderSessions[*int]
	entered, finished := make(chan struct{}), make(chan struct{})
	caller := make(chan error, 1)
	// Signal callback entry and completion around its cancellation wait, so
	// a
	// successful Stop is checked against actual builder return rather than
	// caller notification.
	go func() {
		_, err := sessions.GetSession(context.Background(), qoderSessionTestProvider(), func(ctx context.Context, _ *Record) (*int, time.Time, error) {
			close(entered)
			<-ctx.Done()
			close(finished)
			return nil, time.Time{}, ctx.Err()
		})
		caller <- err
	}()
	<-entered
	require.NoError(t, sessions.StopContext(context.Background()))
	select {
	case <-finished:
	default:
		t.Fatal("停止返回时构建仍未结束")
	}
	err := <-caller
	require.True(t, errors.Is(err, context.Canceled) || errors.Is(err, ErrQoderSessionsStopped))
	_, err = sessions.GetSession(context.Background(), qoderSessionTestProvider(), func(context.Context, *Record) (*int, time.Time, error) {
		t.Fatal("停止后不应构建")
		return nil, time.Time{}, nil
	})
	require.ErrorIs(t, err, ErrQoderSessionsStopped)
	require.NoError(t, sessions.StopContext(context.Background()))
}

// TestQoderSessionsStopReportsUnfinishedAndRejectsLateCache keeps a builder
// blocked while ignoring its context until after Stop times out. It asserts the
// waiter sees shutdown, late completion leaves cache empty, and repeated Stop
// retains the first error.
func TestQoderSessionsStopReportsUnfinishedAndRejectsLateCache(t *testing.T) {
	var sessions QoderSessions[*int]
	entered, proceed, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	caller := make(chan error, 1)
	// Hold a cancellation-insensitive callback behind proceed while Stop
	// times
	// out; only release it after the waiter has observed shutdown, exposing
	// late-cache rejection.
	go func() {
		_, err := sessions.GetSession(context.Background(), qoderSessionTestProvider(), func(context.Context, *Record) (*int, time.Time, error) {
			close(entered)
			<-proceed
			defer close(completed)
			value := 7
			return &value, time.Time{}, nil
		})
		caller <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	stopErr := sessions.StopContext(ctx)
	require.ErrorIs(t, stopErr, context.DeadlineExceeded)
	require.Contains(t, stopErr.Error(), "unfinished")
	require.ErrorIs(t, <-caller, ErrQoderSessionsStopped)
	close(proceed)
	<-completed
	// completed observes the builder callback return, while activity also
	// covers context/generation validation and unregistering; wait for that
	// later completion before inspecting the discarded cache result.
	require.Eventually(t, func() bool {
		sessions.activity.mu.Lock()
		defer sessions.activity.mu.Unlock()
		return len(sessions.activity.active) == 0
	}, time.Second, time.Millisecond)
	sessions.Mu.Lock()
	require.Empty(t, sessions.Sessions)
	sessions.Mu.Unlock()
	require.Equal(t, stopErr, sessions.StopContext(context.Background()))
}

// TestQoderAuthorizationStopWaitsAndRejectsCompletion blocks a claimed
// authorization callback until cancellation arrives. It checks repeated startup
// does not prevent Stop from draining completion, later polls are rejected, and
// repeated Stop succeeds after that drain.
func TestQoderAuthorizationStopWaitsAndRejectsCompletion(t *testing.T) {
	entered := make(chan struct{})
	auth := &QoderAuthorization[int]{Store: NewQoderAuthorizationStore[int]()}
	auth.Store.Set("fixture", &QoderAuthorizationSession[int]{State: "state", Flow: 1, CreatedAt: time.Now()})
	// Signal an accepted completion before waiting for shutdown, so the
	// stop
	// assertion observes a callback that has actually entered its claimed
	// flow.
	auth.Complete = func(ctx context.Context, _ int) (*QoderTokenInfo, bool, error) {
		close(entered)
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
	auth.Start()
	auth.Start()
	done := make(chan error, 1)
	// Begin completion in a separate caller and wait for callback entry
	// before
	// stopping; this establishes in-flight authorization work that shutdown
	// must cancel and drain.
	go func() { _, err := auth.Poll(context.Background(), "fixture", "state", nil); done <- err }()
	<-entered
	require.NoError(t, auth.StopContext(context.Background()))
	require.ErrorIs(t, <-done, context.Canceled)
	auth.Start()
	_, err := auth.Poll(context.Background(), "fixture", "state", nil)
	require.ErrorContains(t, err, "stopped")
	require.NoError(t, auth.StopContext(context.Background()))
}
