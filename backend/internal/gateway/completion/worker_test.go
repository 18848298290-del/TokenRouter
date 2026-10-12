package completion

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestUsageRecordWorkerPool_SubmitEnqueued submits one callback to available
// capacity and observes its completion signal. It checks enqueued mode and eventual
// pond submitted/successful callback counters after the callback returns.
func TestUsageRecordWorkerPool_SubmitEnqueued(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             8,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
	})
	pool.Start()
	t.Cleanup(pool.Stop)

	// The callback signal can arrive before pond publishes completion metrics; wait
	// for both observations so the test distinguishes execution from counter publication.
	done := make(chan struct{})
	mode := pool.Submit(func(ctx context.Context) {
		close(done)
	})
	require.Equal(t, UsageRecordSubmitModeEnqueued, mode)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("task not executed")
	}

	require.Eventually(t, func() bool {
		stats := pool.Stats()
		return stats.SubmittedTasks == 1 && stats.SuccessfulTasks == 1
	}, time.Second, 10*time.Millisecond)
}

// TestUsageRecordWorkerPool_OverflowDrop holds one worker and one queued callback
// before a third submission. It checks ordinary dropped mode, later queued execution,
// and the full-queue rejection counter after the worker is released.
func TestUsageRecordWorkerPool_OverflowDrop(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
	})
	pool.Start()
	t.Cleanup(pool.Stop)

	// Wait for one blocked worker before filling its queue; later closing block
	// releases both accepted callbacks after the overflow policy has been observed.
	block := make(chan struct{})
	started := make(chan struct{})
	secondDone := make(chan struct{})

	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		close(started)
		<-block
	}))
	<-started

	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		close(secondDone)
	}))
	require.Equal(t, UsageRecordSubmitModeDropped, pool.Submit(func(ctx context.Context) {}))

	close(block)
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("queued task not executed")
	}

	require.Eventually(t, func() bool {
		return pool.Stats().DroppedQueueFull >= 1
	}, time.Second, 10*time.Millisecond)
}

// TestUsageRecordWorkerPool_OverflowSync fills worker and queue capacity before
// submitting another callback. It checks inline execution before Submit returns,
// the sync mode, and the completion-specific inline counter after queued work is released.
func TestUsageRecordWorkerPool_OverflowSync(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "sync",
		OverflowSamplePercent: 0,
	})
	pool.Start()
	t.Cleanup(pool.Stop)

	// Wait for one blocked worker before filling its queue; later closing block
	// releases both accepted callbacks after the overflow policy has been observed.
	block := make(chan struct{})
	started := make(chan struct{})
	secondDone := make(chan struct{})
	var syncExecuted atomic.Bool

	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		close(started)
		<-block
	}))
	<-started

	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		close(secondDone)
	}))

	mode := pool.Submit(func(ctx context.Context) {
		syncExecuted.Store(true)
	})
	require.Equal(t, UsageRecordSubmitModeSync, mode)
	require.True(t, syncExecuted.Load())

	close(block)
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("queued task not executed")
	}

	require.Eventually(t, func() bool {
		return pool.Stats().SyncFallbackTasks >= 1
	}, time.Second, 10*time.Millisecond)
}

// TestUsageRecordWorkerPool_OverflowSample fills capacity under a one-percent
// sample policy. It checks that the first overflow executes inline and the next
// is dropped, with both diagnostic counters visible after releasing the queued callbacks.
func TestUsageRecordWorkerPool_OverflowSample(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "sample",
		OverflowSamplePercent: 1,
	})
	pool.Start()
	t.Cleanup(pool.Stop)

	// Wait for one blocked worker before filling its queue; later closing block
	// releases both accepted callbacks after the overflow policy has been observed.
	block := make(chan struct{})
	started := make(chan struct{})
	secondDone := make(chan struct{})
	var syncExecuted atomic.Bool

	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		close(started)
		<-block
	}))
	<-started

	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		close(secondDone)
	}))

	firstOverflow := pool.Submit(func(ctx context.Context) {
		syncExecuted.Store(true)
	})
	require.Equal(t, UsageRecordSubmitModeSync, firstOverflow)
	require.True(t, syncExecuted.Load())

	secondOverflow := pool.Submit(func(ctx context.Context) {})
	require.Equal(t, UsageRecordSubmitModeDropped, secondOverflow)

	close(block)
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("queued task not executed")
	}

	require.Eventually(t, func() bool {
		stats := pool.Stats()
		return stats.SyncFallbackTasks >= 1 && stats.DroppedQueueFull >= 1
	}, time.Second, 10*time.Millisecond)
}

// TestUsageRecordWorkerPool_SubmitAfterStop drains a constructed pool before
// submitting new work. It checks the distinct stopped rejection, drop predicates
// for all four modes, and the stopped-pool rejection counter.
func TestUsageRecordWorkerPool_SubmitAfterStop(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
	})
	pool.Start()

	pool.Stop()
	mode := pool.Submit(func(ctx context.Context) {})
	require.Equal(t, UsageRecordSubmitModeDroppedStopped, mode)
	require.True(t, mode.Dropped())
	require.True(t, UsageRecordSubmitModeDropped.Dropped())
	require.False(t, UsageRecordSubmitModeEnqueued.Dropped())
	require.False(t, UsageRecordSubmitModeSync.Dropped())
	require.GreaterOrEqual(t, pool.Stats().DroppedPoolStopped, uint64(1))
}

// TestUsageRecordWorkerPool_AutoScaleUpAndDown holds eight admitted callbacks
// while a short-interval scaler observes queue pressure. It checks growth to at
// least three, completion of the released work, and eventual shrinkage to the minimum.
func TestUsageRecordWorkerPool_AutoScaleUpAndDown(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:           2,
		QueueSize:             8,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      true,
		AutoScaleMinWorkers:   1,
		AutoScaleMaxWorkers:   4,
		AutoScaleUpPercent:    40,
		AutoScaleDownPercent:  10,
		AutoScaleUpStep:       1,
		AutoScaleDownStep:     1,
		AutoScaleInterval:     20 * time.Millisecond,
		AutoScaleCooldown:     20 * time.Millisecond,
	})
	pool.Start()
	t.Cleanup(pool.Stop)

	block := make(chan struct{})

	// Hold admitted callbacks until queue pressure causes growth, then release them
	// so the same scaler can observe idle capacity and shrink toward its minimum.
	for range 8 {
		require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
			<-block
		}))
	}

	require.Eventually(t, func() bool {
		return pool.Stats().MaxConcurrency >= 3
	}, 2*time.Second, 20*time.Millisecond)

	close(block)

	require.Eventually(t, func() bool {
		return pool.Stats().CompletedTasks >= 8
	}, 2*time.Second, 20*time.Millisecond)

	require.Eventually(t, func() bool {
		return pool.Stats().MaxConcurrency == 1
	}, 2*time.Second, 20*time.Millisecond)
}

// TestUsageRecordWorkerPool_AutoScaleDownRequiresLowRunningUtilization occupies
// both workers with blocked callbacks and no waiting work. It checks unchanged
// capacity during observation and eventual shrinkage after those callbacks are released.
func TestUsageRecordWorkerPool_AutoScaleDownRequiresLowRunningUtilization(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:           2,
		QueueSize:             8,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      true,
		AutoScaleMinWorkers:   1,
		AutoScaleMaxWorkers:   2,
		AutoScaleUpPercent:    80,
		AutoScaleDownPercent:  50,
		AutoScaleUpStep:       1,
		AutoScaleDownStep:     1,
		AutoScaleInterval:     20 * time.Millisecond,
		AutoScaleCooldown:     20 * time.Millisecond,
	})
	pool.Start()
	t.Cleanup(pool.Stop)

	block := make(chan struct{})
	for range 2 {
		require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
			<-block
		}))
	}

	// Allow repeated scaler observations while both callbacks remain blocked; capacity
	// should stay fixed until release makes their worker occupancy fall below the threshold.
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, 2, pool.Stats().MaxConcurrency)

	close(block)
	require.Eventually(t, func() bool {
		return pool.Stats().MaxConcurrency == 1
	}, 2*time.Second, 20*time.Millisecond)
}

// TestUsageRecordWorkerPool_SubmitNilReceiverAndNilTask invokes submission with
// a nil pool and then a nil callback on a constructed pool. It checks the ordinary
// dropped mode for both invalid submission inputs.
func TestUsageRecordWorkerPool_SubmitNilReceiverAndNilTask(t *testing.T) {
	var nilPool *UsageRecordWorkerPool
	require.Equal(t, UsageRecordSubmitModeDropped, nilPool.Submit(func(ctx context.Context) {}))

	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	pool.Start()
	t.Cleanup(pool.Stop)

	require.Equal(t, UsageRecordSubmitModeDropped, pool.Submit(nil))
}

// TestUsageRecordWorkerPool_AutoScaleDisabledKeepsFixedConcurrency supplies
// otherwise valid scaling settings but disables the scaler. It checks fixed worker
// capacity before and during blocked admitted work over the observation interval.
func TestUsageRecordWorkerPool_AutoScaleDisabledKeepsFixedConcurrency(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:           2,
		QueueSize:             4,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
		AutoScaleMinWorkers:   1,
		AutoScaleMaxWorkers:   4,
		AutoScaleUpPercent:    10,
		AutoScaleDownPercent:  1,
		AutoScaleUpStep:       2,
		AutoScaleDownStep:     2,
		AutoScaleInterval:     10 * time.Millisecond,
		AutoScaleCooldown:     10 * time.Millisecond,
	})
	pool.Start()
	t.Cleanup(pool.Stop)

	require.Equal(t, 2, pool.Stats().MaxConcurrency)

	block := make(chan struct{})
	for range 4 {
		require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
			<-block
		}))
	}

	// Keep accepted callbacks blocked over the observation period; disabled scaling
	// leaves configured concurrency fixed even while worker and queue capacity are occupied.
	time.Sleep(120 * time.Millisecond)
	require.Equal(t, 2, pool.Stats().MaxConcurrency)
	close(block)
}

// TestUsageRecordWorkerPool_StringHelpers formats one submission mode and a
// captured statistics value. It checks the exact enqueued label and the running
// and waiting values included in the compact summary.
func TestUsageRecordWorkerPool_StringHelpers(t *testing.T) {
	require.Equal(t, "enqueued", UsageRecordSubmitModeEnqueued.String())
	stats := UsageRecordWorkerPoolStats{RunningWorkers: 2, WaitingTasks: 3, SubmittedTasks: 5, DroppedTasks: 1}
	require.Contains(t, stats.String(), "running=2")
	require.Contains(t, stats.String(), "waiting=3")
}

// TestUsageRecordWorkerPool_NormalizeOptions_BoundsAndDefaults supplies missing
// capacities, an unknown policy, and out-of-range sampling or scaling values.
// It checks the selected defaults and bounded percentage used by the normalized pool.
func TestUsageRecordWorkerPool_NormalizeOptions_BoundsAndDefaults(t *testing.T) {
	opts := NormalizeOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:           0,
		QueueSize:             0,
		TaskTimeout:           0,
		OverflowPolicy:        "invalid",
		OverflowSamplePercent: 300,
		AutoScaleEnabled:      true,
		AutoScaleMinWorkers:   0,
		AutoScaleMaxWorkers:   0,
		AutoScaleUpPercent:    0,
		AutoScaleDownPercent:  100,
		AutoScaleUpStep:       0,
		AutoScaleDownStep:     0,
		AutoScaleInterval:     0,
		AutoScaleCooldown:     -time.Second,
	})

	require.Equal(t, defaultUsageRecordWorkerCount, opts.WorkerCount)
	require.Equal(t, defaultUsageRecordQueueSize, opts.QueueSize)
	require.Equal(t, time.Duration(defaultUsageRecordTaskTimeoutSeconds)*time.Second, opts.TaskTimeout)
	require.Equal(t, defaultUsageRecordOverflowPolicy, opts.OverflowPolicy)
	require.Equal(t, 100, opts.OverflowSamplePercent)
	require.Equal(t, defaultUsageRecordAutoScaleMinWorkers, opts.AutoScaleMinWorkers)
	require.Equal(t, defaultUsageRecordAutoScaleMaxWorkers, opts.AutoScaleMaxWorkers)
	require.Equal(t, defaultUsageRecordAutoScaleUpPercent, opts.AutoScaleUpPercent)
	require.Equal(t, defaultUsageRecordAutoScaleDownPercent, opts.AutoScaleDownPercent)
	require.Equal(t, defaultUsageRecordAutoScaleUpStep, opts.AutoScaleUpStep)
	require.Equal(t, defaultUsageRecordAutoScaleDownStep, opts.AutoScaleDownStep)
	require.Equal(t, defaultUsageRecordAutoScaleInterval, opts.AutoScaleInterval)
	require.Equal(t, defaultUsageRecordAutoScaleCooldown, opts.AutoScaleCooldown)
}

// TestUsageRecordWorkerPool_NormalizeOptions_SampleAndAutoScaleDisabled supplies
// zero sample percentage and conflicting enabled scaling bounds, then a disabled
// configuration. It checks default sampling, repaired bounds/thresholds, and the
// fixed disabled range.
func TestUsageRecordWorkerPool_NormalizeOptions_SampleAndAutoScaleDisabled(t *testing.T) {
	sampleOpts := NormalizeOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:           32,
		QueueSize:             128,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "sample",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      true,
		AutoScaleMinWorkers:   64,
		AutoScaleMaxWorkers:   48,
		AutoScaleUpPercent:    30,
		AutoScaleDownPercent:  40,
		AutoScaleUpStep:       1,
		AutoScaleDownStep:     1,
		AutoScaleInterval:     time.Second,
		AutoScaleCooldown:     time.Second,
	})
	require.Equal(t, defaultUsageRecordOverflowSampleRatio, sampleOpts.OverflowSamplePercent)
	require.Equal(t, 64, sampleOpts.AutoScaleMinWorkers)
	require.Equal(t, 64, sampleOpts.AutoScaleMaxWorkers)
	require.Equal(t, 64, sampleOpts.WorkerCount)
	require.Equal(t, 15, sampleOpts.AutoScaleDownPercent)

	fixedOpts := NormalizeOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:      20,
		AutoScaleEnabled: false,
	})
	require.Equal(t, 20, fixedOpts.AutoScaleMinWorkers)
	require.Equal(t, 20, fixedOpts.AutoScaleMaxWorkers)
}

// TestUsageRecordWorkerPool_ShouldSyncFallbackEdgeCases calls the sampler directly
// with zero and then full percentage. It checks rejection without a positive
// percentage and inline selection on repeated full-percentage observations.
func TestUsageRecordWorkerPool_ShouldSyncFallbackEdgeCases(t *testing.T) {
	pool := &UsageRecordWorkerPool{overflowSamplePercent: 0}
	require.False(t, pool.shouldSyncFallback())

	pool.overflowSamplePercent = 100
	require.True(t, pool.shouldSyncFallback())
	require.True(t, pool.shouldSyncFallback())
}

// TestUsageRecordWorkerPool_StatsAndStop_NilBranches inspects nil and empty pool
// values. It checks zero diagnostic structures and nonpanicking stop calls when
// no pond pool was constructed, without starting or admitting work.
func TestUsageRecordWorkerPool_StatsAndStop_NilBranches(t *testing.T) {
	var nilPool *UsageRecordWorkerPool
	require.Equal(t, UsageRecordWorkerPoolStats{}, nilPool.Stats())
	require.NotPanics(t, func() { nilPool.Stop() })

	emptyPool := &UsageRecordWorkerPool{}
	require.Equal(t, UsageRecordWorkerPoolStats{}, emptyPool.Stats())
	require.NotPanics(t, func() { emptyPool.Stop() })
}

// TestUsageRecordWorkerPool_Execute_PanicAndTimeout invokes the execution helper
// directly with a panic and a callback waiting for context expiry. It checks panic
// consumption and cooperative timeout completion rather than forced callback interruption.
func TestUsageRecordWorkerPool_Execute_PanicAndTimeout(t *testing.T) {
	pool := &UsageRecordWorkerPool{taskTimeout: 30 * time.Millisecond}

	// Direct execution recovers task panic even without pond or an observer, which
	// keeps this assertion separate from the later context-timeout callback below.
	require.NotPanics(t, func() {
		pool.execute(func(ctx context.Context) {
			panic("boom")
		})
	})

	// This callback cooperates by waiting on its supplied context; completing it
	// demonstrates timeout signalling without relying on forcible interruption of work.
	done := make(chan struct{})
	pool.execute(func(ctx context.Context) {
		<-ctx.Done()
		close(done)
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout context not cancelled")
	}
}

// TestUsageRecordWorkerPool_ResizeAndLogDropBranches calls an unchanged resize
// and seeds the recent-log timestamp before drop reporting. It checks unchanged
// capacity and nonpanicking diagnostics, with no assertion about emitted log count.
func TestUsageRecordWorkerPool_ResizeAndLogDropBranches(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{
		WorkerCount:      1,
		QueueSize:        8,
		TaskTimeout:      time.Second,
		OverflowPolicy:   "drop",
		AutoScaleEnabled: false,
	})
	pool.Start()
	t.Cleanup(pool.Stop)

	// Equal capacities exercise the early-return path before pond receives any resize.
	// The assertion below concerns capacity and does not observe diagnostic callbacks.
	pool.resizePool(1, 1, 0, 0, 0, 8, "noop")
	require.Equal(t, 1, pool.Stats().MaxConcurrency)

	// Seed a recent wall-clock timestamp to enter the suppression path when invoked
	// promptly; the assertion protects nonpanicking behavior rather than emitted message count.
	pool.lastDropLogNanos.Store(time.Now().UnixNano())
	require.NotPanics(t, func() {
		pool.logDrop("full")
	})
}

// TestStopWaitsForSynchronousOverflow blocks accepted queued and inline callbacks,
// then releases queued work while inline work remains active. It checks a bounded
// stop error, inline completion after release, and successful continuation of the same drain.
func TestStopWaitsForSynchronousOverflow(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 1, TaskTimeout: time.Second, OverflowPolicy: "sync"})
	// Startup signals establish occupied capacity before each later submission; the
	// two release channels let queued and inline work finish at separate test-selected times.
	first, releaseQueue := make(chan struct{}), make(chan struct{})
	inline, releaseInline := make(chan struct{}), make(chan struct{})
	submitted := make(chan UsageRecordSubmitMode, 1)
	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) { close(first); <-releaseQueue }))
	<-first
	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) { <-releaseQueue }))
	// Submit inline overflow from another goroutine so this test can time out its
	// stop wait while that accepted callback remains blocked on releaseInline.
	go func() { submitted <- pool.Submit(func(context.Context) { close(inline); <-releaseInline }) }()
	<-inline
	close(releaseQueue)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	// Expiry limits this caller's wait, while the accepted inline callback continues;
	// releasing it allows the second caller to observe completion of the same drain.
	err := pool.StopContext(ctx)
	close(releaseInline)
	require.Equal(t, UsageRecordSubmitModeSync, <-submitted)
	require.NoError(t, pool.StopContext(context.Background()))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "unfinished tasks")
}

// TestStartAfterStopDoesNotCreateWorker stops the pool before sixteen joined
// concurrent Start/Stop calls. It checks that no scaler cancel handle was published
// and later submission retains the stopped rejection without executing its callback.
func TestStartAfterStopDoesNotCreateWorker(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 1, AutoScaleEnabled: true, AutoScaleMinWorkers: 1, AutoScaleMaxWorkers: 2})
	pool.Stop()
	// Join all repeated lifecycle calls before inspecting the shared scaler handle,
	// so the assertion covers post-stop calls that may overlap under stateMu.
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { ; pool.Start(); pool.Stop() })
	}
	wg.Wait()
	require.Nil(t, pool.autoScaleCancel)
	require.Equal(t, UsageRecordSubmitModeDroppedStopped, pool.Submit(func(context.Context) { t.Error("停止后任务不应执行") }))
}
