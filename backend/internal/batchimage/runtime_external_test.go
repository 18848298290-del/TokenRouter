package batchimage_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

type blockingBatchImageRuntimeQueue struct {
	reserveCalls atomic.Int64
}

type blockedQueue struct {
	blockingBatchImageRuntimeQueue
	entered, cancelled, release chan struct{}
}

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

func TestRepeatedStopWaitsForSameWork(t *testing.T) {
	q := &blockedQueue{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	r := batchimage.NewWorkerRuntime(batchimage.NewBatchImageWorker(q, &fakeBatchImageProcessor{}, batchimage.BatchImageWorkerOptions{}), nil, true)
	r.Start()
	<-q.entered
	first := make(chan struct{})
	go func() { r.Stop(); close(first) }()
	<-q.cancelled
	second := make(chan struct{})
	go func() { r.Stop(); close(second) }()
	returned := false
	select {
	case <-second:
		returned = true
	case <-time.After(30 * time.Millisecond):
	}
	close(q.release)
	<-first
	<-second
	require.False(t, returned, "第一次停止仍有在途工作时，重复 Stop 不能报告完成")
}

// TestStoppedRuntimesRejectStart checks that stopping permanently prevents a runtime restart.
func TestStoppedRuntimesRejectStart(t *testing.T) {
	t.Run("batchimage", func(t *testing.T) {
		r := batchimage.NewWorkerRuntime(batchimage.NewBatchImageWorker(&blockingBatchImageRuntimeQueue{}, &fakeBatchImageProcessor{}, batchimage.BatchImageWorkerOptions{}), nil, true)
		r.Stop()
		r.Start()
		defer r.Stop()
		require.False(t, r.Running(), "停止后的批量图片 runtime 不应重开")
	})
}

func (q *blockingBatchImageRuntimeQueue) Enqueue(context.Context, string) error {
	return nil
}

func (q *blockingBatchImageRuntimeQueue) Reserve(ctx context.Context, _ time.Duration) (batchimage.ReservedBatchImageJob, error) {
	q.reserveCalls.Add(1)
	<-ctx.Done()
	return batchimage.ReservedBatchImageJob{}, ctx.Err()
}

func (q *blockingBatchImageRuntimeQueue) RequeueAfter(context.Context, string, time.Duration) error {
	return nil
}

func (q *blockingBatchImageRuntimeQueue) Ack(context.Context, string) error {
	return nil
}

func (q *blockingBatchImageRuntimeQueue) Heartbeat(context.Context, string) error {
	return nil
}

func (q *blockingBatchImageRuntimeQueue) MoveDueDelayedToReady(context.Context, int) (int, error) {
	return 0, nil
}

func (q *blockingBatchImageRuntimeQueue) RecoverStaleActive(context.Context, time.Duration, int) (int, error) {
	return 0, nil
}

func (q *blockingBatchImageRuntimeQueue) TryAcquireJobLock(context.Context, string, time.Duration) (batchimage.BatchImageJobLock, bool, error) {
	return nil, false, nil
}

func (q *blockedQueue) Reserve(ctx context.Context, _ time.Duration) (batchimage.ReservedBatchImageJob, error) {
	close(q.entered)
	<-ctx.Done()
	close(q.cancelled)
	<-q.release
	return batchimage.ReservedBatchImageJob{}, ctx.Err()
}
