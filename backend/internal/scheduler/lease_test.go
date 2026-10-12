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

// TestLeaseConcurrentReleaseOwnership checks single cleanup and reverse resource order when cancellation races with completion.
func TestLeaseConcurrentReleaseOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var order []string
	l := NewLease(ctx, ReleaseOnCancel, func() { order = append(order, "user") }, func() { order = append(order, "provider") })
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { ; l.Release() })
	}
	cancel()
	wg.Wait()
	require.Equal(t, []string{"provider", "user"}, order)
}

// TestLeaseCompletionIgnoresClientCancellation checks that completion mode holds capacity until upstream completion after client cancellation.
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

// TestLeaseRejectsLateResource checks immediate release of resources received after closure.
func TestLeaseRejectsLateResource(t *testing.T) {
	l := NewLease(context.Background(), ReleaseOnCompletion)
	l.Release()
	var count int
	require.False(t, l.Own(func() { count++ }))
	require.Equal(t, 1, count)
}

// TestAttemptLeaseRetainsCompletionOutcome checks independent outcomes for successful and unfinished attempts.
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

// TestWrapReleaseOnDone_NoGoroutineLeak checks goroutine counts after release.
func TestWrapReleaseOnDone_NoGoroutineLeak(t *testing.T) {
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

	// Allow cancellation callbacks to exit.
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

// TestWrapReleaseOnDone_ContextCancellation checks release on context cancellation.
func TestWrapReleaseOnDone_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var releaseCount int32
	_ = WrapRelease(ctx, ReleaseOnCancel, func() {
		atomic.AddInt32(&releaseCount, 1)
	})

	// Cancellation triggers release.
	cancel()

	time.Sleep(100 * time.Millisecond)

	if count := atomic.LoadInt32(&releaseCount); count != 1 {
		t.Errorf("expected release count to be 1, got %d", count)
	}
}

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

// TestWrapReleaseOnDone_MultipleCallsOnlyReleaseOnce checks idempotent release.
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

// TestWrapReleaseOnDone_NilReleaseFunc checks the nil callback result.
func TestWrapReleaseOnDone_NilReleaseFunc(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := WrapRelease(ctx, ReleaseOnCancel, nil)

	if release != nil {
		t.Error("expected nil release function when releaseFunc is nil")
	}
}

// TestWrapReleaseOnDone_ConcurrentCalls checks single cleanup across concurrent callers.
func TestWrapReleaseOnDone_ConcurrentCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var releaseCount int32
	release := WrapRelease(ctx, ReleaseOnCancel, func() {
		atomic.AddInt32(&releaseCount, 1)
	})

	const numGoroutines = 10
	for range numGoroutines {
		go release()
	}

	time.Sleep(200 * time.Millisecond)

	if count := atomic.LoadInt32(&releaseCount); count != 1 {
		t.Errorf("expected release count to be 1, got %d", count)
	}
}

// BenchmarkWrapReleaseOnDone measures lease wrapper overhead.
func BenchmarkWrapReleaseOnDone(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b.ResetTimer()
	for range b.N {
		release := WrapRelease(ctx, ReleaseOnCancel, func() {})
		release()
	}
}
