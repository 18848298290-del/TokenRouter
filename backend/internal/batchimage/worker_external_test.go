package batchimage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

// A queue visit returns a scheduling decision while the processor controls
// persistent progress. These RunOnce cases use a recording queue to
// distinguish default polling, requested delays, terminal acknowledgement,
// processor failure retries and lock-contention recovery before any task
// execution.

// TestBatchImageWorker_ProcessesJobOnce runs one configured reservation with
// an undecided processor result. It checks the processed batch identity,
// default delayed revisit and one deferred lease release.
func TestBatchImageWorker_ProcessesJobOnce(t *testing.T) {
	queue := newFakeBatchImageQueue("imgbatch_worker_once")
	processor := &fakeBatchImageProcessor{}
	worker := batchimage.NewBatchImageWorker(queue, processor, batchimage.BatchImageWorkerOptions{ReserveBlockTimeout: time.Millisecond})

	require.NoError(t, worker.RunOnce(context.Background()))
	require.Equal(t, []string{"imgbatch_worker_once"}, processor.processed)
	require.Len(t, queue.requeued, 1)
	require.Equal(t, batchimage.DefaultBatchImageWorkerRequeueDelay, queue.requeued[0].delay)
	require.Equal(t, 1, queue.releaseCount)
}

// TestBatchImageWorker_RequeuesNonTerminalResultWithRequestedDelay returns a
// positive polling delay from a nonterminal processor. The visit is requeued
// with that duration and its batch identity while acknowledgement stays
// empty.
func TestBatchImageWorker_RequeuesNonTerminalResultWithRequestedDelay(t *testing.T) {
	queue := newFakeBatchImageQueue("imgbatch_worker_requeue")
	processor := &fakeBatchImageProcessor{result: batchimage.BatchImageProcessResult{RequeueAfter: 42 * time.Second}}
	worker := batchimage.NewBatchImageWorker(queue, processor, batchimage.BatchImageWorkerOptions{})

	require.NoError(t, worker.RunOnce(context.Background()))
	require.Len(t, queue.requeued, 1)
	require.Equal(t, "imgbatch_worker_requeue", queue.requeued[0].batchID)
	require.Equal(t, 42*time.Second, queue.requeued[0].delay)
	require.Empty(t, queue.acked)
}

// TestBatchImageWorker_AcksTerminalResult returns terminal completion from
// the configured processor. The reserved batch is acknowledged and receives
// no delayed revisit, protecting the worker's completion scheduling decision.
func TestBatchImageWorker_AcksTerminalResult(t *testing.T) {
	queue := newFakeBatchImageQueue("imgbatch_worker_terminal")
	processor := &fakeBatchImageProcessor{result: batchimage.BatchImageProcessResult{Terminal: true}}
	worker := batchimage.NewBatchImageWorker(queue, processor, batchimage.BatchImageWorkerOptions{})

	require.NoError(t, worker.RunOnce(context.Background()))
	require.Equal(t, []string{"imgbatch_worker_terminal"}, queue.acked)
	require.Empty(t, queue.requeued)
}

// TestBatchImageWorker_RequeuesOnProcessorError injects a processor error
// during an owned visit. The queue receives the configured error retry delay
// and no acknowledgement, keeping incomplete persistent work available.
func TestBatchImageWorker_RequeuesOnProcessorError(t *testing.T) {
	queue := newFakeBatchImageQueue("imgbatch_worker_error")
	processor := &fakeBatchImageProcessor{err: errors.New("processor failed")}
	worker := batchimage.NewBatchImageWorker(queue, processor, batchimage.BatchImageWorkerOptions{ErrorRetryDelay: 7 * time.Second})

	require.NoError(t, worker.RunOnce(context.Background()))
	require.Len(t, queue.requeued, 1)
	require.Equal(t, 7*time.Second, queue.requeued[0].delay)
	require.Empty(t, queue.acked)
}

// TestBatchImageWorker_RequeuesWhenJobLockNotAcquired reserves a batch whose
// lock cannot be acquired. It checks the requested contention delay and
// confirms the processor and acknowledgement paths remain unused.
func TestBatchImageWorker_RequeuesWhenJobLockNotAcquired(t *testing.T) {
	queue := newFakeBatchImageQueue("imgbatch_worker_locked")
	queue.lockAcquired = false
	processor := &fakeBatchImageProcessor{}
	worker := batchimage.NewBatchImageWorker(queue, processor, batchimage.BatchImageWorkerOptions{LockConflictDelay: 3 * time.Second})

	// This recording queue accepts the contention requeue without modeling a
	// stored lock token. The assertions inspect the requested scheduling decision.
	require.NoError(t, worker.RunOnce(context.Background()))
	require.Empty(t, processor.processed)
	require.Len(t, queue.requeued, 1)
	require.Equal(t, 3*time.Second, queue.requeued[0].delay)
	require.Empty(t, queue.acked)
}
