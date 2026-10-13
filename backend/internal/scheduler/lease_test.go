package scheduler

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLeaseConcurrentReleaseOwnership races concurrent release calls with
// cancellation on one request lease. It requires one cleanup per resource in
// reverse ownership order after the release callers have joined.
func TestLeaseConcurrentReleaseOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var order []string
	l := NewLease(ctx, ReleaseOnCancel, func() { order = append(order, "user") }, func() { order = append(order, "provider") })
	var wg sync.WaitGroup
	// Concurrent release callers and the cancellation callback share one
	// lease. Join the callers before reading order so their cleanup has
	// returned before the assertion.
	for range 16 {
		wg.Go(func() { ; l.Release() })
	}
	cancel()
	wg.Wait()
	require.Equal(t, []string{"provider", "user"}, order)
}

// TestLeaseCompletionIgnoresClientCancellation cancels a request using
// completion-based resource release and then invokes release twice. It checks
// that cancellation leaves the count untouched and explicit completion cleans
// up exactly once.
func TestLeaseCompletionIgnoresClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var count atomic.Int64
	l := NewLease(ctx, ReleaseOnCompletion, func() { count.Add(1) })
	cancel()
	require.Zero(t, count.Load())
	l.Release()
	l.Release()
	require.EqualValues(t, 1, count.Load())
}

// TestLeaseRejectsLateResource offers a resource after its lease has already
// closed. It requires Own to reject ownership while invoking immediate cleanup
// for that late resource exactly once.
func TestLeaseRejectsLateResource(t *testing.T) {
	l := NewLease(context.Background(), ReleaseOnCompletion)
	l.Release()
	var count int
	require.False(t, l.Own(func() { count++ }))
	require.Equal(t, 1, count)
}

// TestAttemptLeaseRetainsCompletionOutcome finishes one attempt as served and
// leaves another unfinished before parent cleanup. It checks retained served
// and failed outcomes and exactly two physical cleanup calls.
func TestAttemptLeaseRetainsCompletionOutcome(t *testing.T) {
	parent := NewLease(context.Background(), ReleaseOnCompletion)
	var outcomes []bool
	var released int
	a := NewAttemptLease(parent, func(o AttemptOutcome) { outcomes = append(outcomes, o.Served) }, func() { released++ })
	a.Finish(AttemptOutcome{Served: true})
	NewAttemptLease(parent, func(o AttemptOutcome) { outcomes = append(outcomes, o.Served) }, func() { released++ })
	parent.Release()
	a.Release()
	require.Equal(t, []bool{true, false}, outcomes)
	require.Equal(t, 2, released)
}

// TestWrapReleaseOnDone_NoGoroutineLeak releases a cancellation wrapper and
// samples process goroutine counts after settling and garbage collection. It
// checks one cleanup and allows at most two additional goroutines for
// test-harness variation.
func TestWrapReleaseOnDone_NoGoroutineLeak(t *testing.T) {
	// Collect garbage and let existing goroutines settle before sampling the
	// baseline. Later measurements compare against this observed process
	// state.
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	initialGoroutines := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var releaseCount int32
	release := WrapRelease(ctx, ReleaseOnCancel, func() {
		atomic.AddInt32(&releaseCount, 1)
	})

	release()

	// Explicit release removes the registered cancellation callback. Allow
	// process goroutines to settle before sampling counts used by this
	// approximate leak check.
	time.Sleep(200 * time.Millisecond)

	if count := atomic.LoadInt32(&releaseCount); count != 1 {
		t.Errorf("expected release count to be 1, got %d", count)
	}

	// Run GC before sampling the final goroutine count.
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	// Allow two goroutines of variation from the test harness.
	finalGoroutines := runtime.NumGoroutine()
	if finalGoroutines > initialGoroutines+2 {
		t.Errorf("goroutine leak detected: initial=%d, final=%d, leaked=%d",
			initialGoroutines, finalGoroutines, finalGoroutines-initialGoroutines)
	}
}

// TestWrapReleaseOnDone_ContextCancellation installs a release wrapper and
// cancels its request context. After allowing callback execution, it requires
// the recorded cleanup count to be one.
func TestWrapReleaseOnDone_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var releaseCount int32
	_ = WrapRelease(ctx, ReleaseOnCancel, func() {
		atomic.AddInt32(&releaseCount, 1)
	})

	// Cancellation dispatches the registered cleanup asynchronously. The
	// following settling interval permits that callback to run before its
	// atomic count is inspected.
	cancel()

	time.Sleep(100 * time.Millisecond)

	if count := atomic.LoadInt32(&releaseCount); count != 1 {
		t.Errorf("expected release count to be 1, got %d", count)
	}
}

// TestWrapReleaseOnDone_AlreadyCancelledReleasesExactlyOnce wraps cleanup
// under a context canceled before construction and also invokes the returned
// release. It waits for exactly one cleanup, protecting coordination between
// immediate caller cleanup and cancellation handling.
func TestWrapReleaseOnDone_AlreadyCancelledReleasesExactlyOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var releaseCount int32
	release := WrapRelease(ctx, ReleaseOnCancel, func() {
		atomic.AddInt32(&releaseCount, 1)
	})
	release()

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&releaseCount) == 1
	}, time.Second, time.Millisecond)
}

// TestWrapReleaseOnDone_MultipleCallsOnlyReleaseOnce calls the same returned
// release three times under a live request context. It requires one recorded
// cleanup after the calls, protecting the wrapper's repeated-release behavior.
func TestWrapReleaseOnDone_MultipleCallsOnlyReleaseOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var releaseCount int32
	release := WrapRelease(ctx, ReleaseOnCancel, func() {
		atomic.AddInt32(&releaseCount, 1)
	})

	release()
	release()
	release()

	time.Sleep(100 * time.Millisecond)

	if count := atomic.LoadInt32(&releaseCount); count != 1 {
		t.Errorf("expected release count to be 1, got %d", count)
	}
}

// TestWrapReleaseOnDone_NilReleaseFunc constructs a cancellation wrapper with
// no cleanup callback. It requires a nil returned handle so callers can
// represent absence of an owned resource.
func TestWrapReleaseOnDone_NilReleaseFunc(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := WrapRelease(ctx, ReleaseOnCancel, nil)

	if release != nil {
		t.Error("expected nil release function when releaseFunc is nil")
	}
}

// TestWrapReleaseOnDone_ConcurrentCalls dispatches ten goroutines using one
// cancellation release wrapper. After a settling interval, it requires one
// cleanup observation across those concurrent callers.
func TestWrapReleaseOnDone_ConcurrentCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var releaseCount int32
	release := WrapRelease(ctx, ReleaseOnCancel, func() {
		atomic.AddInt32(&releaseCount, 1)
	})

	// Reuse one wrapper across multiple cleanup callers to exercise its shared
	// release guard; the later delay allows dispatched goroutines to reach
	// that guard.
	const numGoroutines = 10
	for range numGoroutines {
		go release()
	}

	time.Sleep(200 * time.Millisecond)

	if count := atomic.LoadInt32(&releaseCount); count != 1 {
		t.Errorf("expected release count to be 1, got %d", count)
	}
}

// BenchmarkWrapReleaseOnDone constructs and releases cancellation wrappers
// under one shared context. Timed iterations include wrapper creation and
// explicit cleanup, providing a local overhead workload for that lifecycle
// path.
func BenchmarkWrapReleaseOnDone(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b.ResetTimer()
	for range b.N {
		release := WrapRelease(ctx, ReleaseOnCancel, func() {})
		release()
	}
}
