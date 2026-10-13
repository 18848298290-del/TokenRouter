package batchimage_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

// A runtime can receive repeated stop calls while its reserved queue
// operation is still returning. The blocking fixtures separate queue entry,
// observed cancellation and final release so the tests can inspect that
// lifecycle ordering directly. Ordinary idle queue methods contribute no task
// work.
//
// The enabled runtime case checks that reservation begins and ends after
// Stop, while the disabled case confirms no reserve call starts. The
// delayed-return case launches two stop callers and holds the queue after
// cancellation, proving the second caller waits for the same unfinished
// operation. A stopped-before-start case verifies admission remains
// permanently closed, including when queue processing was originally enabled.

// blockingBatchImageRuntimeQueue counts reservations and blocks each one
// until runtime cancellation.
type blockingBatchImageRuntimeQueue struct {
	reserveCalls atomic.Int64 // atomic reservation invocation count
}

// blockedQueue exposes queue entry, cancellation and final return as separate
// test signals.
type blockedQueue struct {
	blockingBatchImageRuntimeQueue // inherited idle queue operations
	// entered, cancelled and release separate reservation entry, observed
	// cancellation and final return. The test holds release until both Stop
	// callers are waiting on the unfinished invocation.
	// entered: reservation entry signal; cancelled: observed cancellation signal;
	// release: permit reservation return
	entered, cancelled, release chan struct{}
}

// TestBatchImageWorkerRuntime_QueueDisabledDoesNotStart constructs a disabled
// worker runtime and calls Start. The queue sees no reservations, Running
// stays false, and Stop remains safe for the unstarted lifecycle.
func TestBatchImageWorkerRuntime_QueueDisabledDoesNotStart(t *testing.T) {
	queue := &blockingBatchImageRuntimeQueue{}
	runtime := batchimage.NewWorkerRuntime(
		batchimage.NewBatchImageWorker(queue, &fakeBatchImageProcessor{}, batchimage.BatchImageWorkerOptions{}), nil, false,
	)

	runtime.Start()

	require.False(t, runtime.Running())
	require.Zero(t, queue.reserveCalls.Load())
	require.NotPanics(t, runtime.Stop)
}

// TestBatchImageWorkerRuntime_QueueEnabledStartsAndStops starts an enabled
// worker against a cancellation-blocked idle queue. It observes reservation
// activity, stops that work and checks repeated Stop with no processor calls.
func TestBatchImageWorkerRuntime_QueueEnabledStartsAndStops(t *testing.T) {
	queue := &blockingBatchImageRuntimeQueue{}
	processor := &fakeBatchImageProcessor{}
	runtime := batchimage.NewWorkerRuntime(
		batchimage.NewBatchImageWorker(queue, processor, batchimage.BatchImageWorkerOptions{
			DelayedPollInterval: time.Hour,
			RecoveryInterval:    time.Hour,
		}), nil, true,
	)

	runtime.Start()

	require.Eventually(t, func() bool {
		return runtime.Running() && queue.reserveCalls.Load() > 0
	}, time.Second, 10*time.Millisecond)
	require.Empty(t, processor.processed)
	require.NotPanics(t, runtime.Stop)
	require.False(t, runtime.Running())
	require.NotPanics(t, runtime.Stop)
}

// TestRepeatedStopWaitsForSameWork holds a reservation after cancellation
// while two goroutines call Stop. The second stop cannot report completion
// before the fixture releases the same original queue operation.
func TestRepeatedStopWaitsForSameWork(t *testing.T) {
	q := &blockedQueue{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	r := batchimage.NewWorkerRuntime(batchimage.NewBatchImageWorker(q, &fakeBatchImageProcessor{}, batchimage.BatchImageWorkerOptions{}), nil, true)
	r.Start()
	<-q.entered
	first := make(chan struct{})
	// Start the first stop while the reservation is held so cancellation becomes
	// observable before its worker invocation is allowed to return.
	go func() { r.Stop(); close(first) }()
	<-q.cancelled
	second := make(chan struct{})
	// The second caller joins the same in-progress stop; observe whether it
	// returns before releasing the original cancelled queue operation.
	go func() { r.Stop(); close(second) }()
	returned := false
	select {
	case <-second:
		returned = true
	case <-time.After(30 * time.Millisecond):
	}
	// Release the cancelled reservation after observing the second stop, then
	// join both callers before checking the recorded shutdown ordering.
	close(q.release)
	<-first
	<-second
	require.False(t, returned, "第一次停止仍有在途工作时，重复 Stop 不能报告完成")
}

// TestStoppedRuntimesRejectStart stops an enabled worker runtime before any
// Start call. A later Start leaves Running false, protecting the irreversible
// shutdown rule used by application lifecycle ownership.
func TestStoppedRuntimesRejectStart(t *testing.T) {
	t.Run("batchimage", func(t *testing.T) {
		r := batchimage.NewWorkerRuntime(batchimage.NewBatchImageWorker(&blockingBatchImageRuntimeQueue{}, &fakeBatchImageProcessor{}, batchimage.BatchImageWorkerOptions{}), nil, true)
		r.Stop()
		r.Start()
		defer r.Stop()
		require.False(t, r.Running(), "停止后的批量图片 runtime 不应重开")
	})
}

// Enqueue accepts fixture publication requests while retaining an idle queue.
func (q *blockingBatchImageRuntimeQueue) Enqueue(context.Context, string) error {
	return nil
}

// Reserve counts the visit and waits for runtime context cancellation before
// returning its error. The idle fixture never returns a batch, so
// enabled-runtime tests exercise shutdown without invoking the processor.
func (q *blockingBatchImageRuntimeQueue) Reserve(ctx context.Context, _ time.Duration) (batchimage.ReservedBatchImageJob, error) {
	q.reserveCalls.Add(1)
	<-ctx.Done()
	return batchimage.ReservedBatchImageJob{}, ctx.Err()
}

// RequeueAfter accepts delayed scheduling requests for the idle runtime
// fixture.
func (q *blockingBatchImageRuntimeQueue) RequeueAfter(context.Context, string, time.Duration) error {
	return nil
}

// Ack accepts acknowledgement requests for the idle runtime queue fixture.
func (q *blockingBatchImageRuntimeQueue) Ack(context.Context, string) error {
	return nil
}

// Heartbeat accepts activity requests for the idle runtime queue fixture.
func (q *blockingBatchImageRuntimeQueue) Heartbeat(context.Context, string) error {
	return nil
}

// MoveDueDelayedToReady reports no delayed entries in the runtime queue
// fixture.
func (q *blockingBatchImageRuntimeQueue) MoveDueDelayedToReady(context.Context, int) (int, error) {
	return 0, nil
}

// RecoverStaleActive reports no abandoned entries in the runtime queue
// fixture.
func (q *blockingBatchImageRuntimeQueue) RecoverStaleActive(context.Context, time.Duration, int) (int, error) {
	return 0, nil
}

// TryAcquireJobLock reports no acquired lease for the idle runtime fixture.
func (q *blockingBatchImageRuntimeQueue) TryAcquireJobLock(context.Context, string, time.Duration) (batchimage.BatchImageJobLock, bool, error) {
	return nil, false, nil
}

// Reserve exposes its entry and cancellation before waiting for an explicit
// test release. The fixture is used for one reservation, so each signal
// closes once. Its caller receives the runtime context error only after the
// final release.
//
// That delayed return keeps worker work unfinished even though cancellation
// has already been observed. The repeated-stop test can therefore distinguish
// cancellation from completed shutdown and verify both Stop calls wait on the
// original queue invocation rather than reporting completion from a newly
// selected signal.
func (q *blockedQueue) Reserve(ctx context.Context, _ time.Duration) (batchimage.ReservedBatchImageJob, error) {
	close(q.entered)
	<-ctx.Done()
	close(q.cancelled)
	// Keep the reservation unfinished after cancellation so repeated runtime
	// stops must wait for this same blocked queue invocation to return.
	<-q.release
	return batchimage.ReservedBatchImageJob{}, ctx.Err()
}
