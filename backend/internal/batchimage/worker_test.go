package batchimage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A worker can finish processing after a heartbeat has lost its lease. The
// fixture waits for processing cancellation and still returns a terminal
// result, reproducing the case where queue mutation needs to honor the
// ownership failure first.
//
// Injected heartbeat and renewal errors cover a reported owner mismatch and
// an unavailable Redis observation. Atomic counters record whether the old
// visit acknowledged, requeued or released its handle. The test waits for the
// processor's cancellation cause before inspecting those counters, so a
// terminal result cannot hide lost ownership; deferred release still executes
// once after the worker returns.

// leaseLossQueue returns one configured lease while unrelated queue methods
// remain embedded.
type leaseLossQueue struct {
	BatchImageQueue                // unused queue method dependencies
	lock            *leaseLossLock // configured ownership failure handle
}

// leaseLossLock injects ownership failures and atomically records worker
// cleanup actions.
type leaseLossLock struct {
	// heartbeatErr: injected active update failure; refreshErr: injected lease
	// renewal failure
	heartbeatErr, refreshErr error
	// acked, requeued and released record the worker's owned queue decisions.
	// The test reads them after RunOnce returns to distinguish suppressed queue
	// mutation from the deferred lease cleanup.
	// acked: atomic acknowledgement invocation count; requeued: atomic delayed
	// revisit count; released: atomic lease release count
	acked, requeued, released atomic.Int64
}

// leaseLossProcessor records the cancellation cause before reporting a
// terminal result.
type leaseLossProcessor struct {
	cause error /* observed processing cancellation cause */
}

// TestWorkerLostLeaseCancelsProcessingWithoutQueueMutation injects failed
// heartbeat, failed renewal and unconfirmed ownership while processing waits.
// The lease cause cancels work, suppresses ACK and requeue, and still invokes
// deferred release once.
func TestWorkerLostLeaseCancelsProcessingWithoutQueueMutation(t *testing.T) {
	for _, test := range []struct {
		name               string // scenario display name
		heartbeat, refresh error  // heartbeat: injected heartbeat failure; refresh: injected renewal failure
	}{
		{name: "heartbeat_owner_lost", heartbeat: ErrBatchImageLeaseLost},
		{name: "refresh_owner_lost", refresh: ErrBatchImageLeaseLost},
		{name: "redis_cannot_confirm_owner", heartbeat: errors.New("redis unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			lock := &leaseLossLock{heartbeatErr: test.heartbeat, refreshErr: test.refresh}
			processor := &leaseLossProcessor{}
			worker := NewBatchImageWorker(leaseLossQueue{lock: lock}, processor, BatchImageWorkerOptions{JobLockTTL: time.Second, StaleActiveAfter: time.Second})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.ErrorIs(t, worker.RunOnce(ctx), ErrBatchImageLeaseLost)
			require.ErrorIs(t, processor.cause, ErrBatchImageLeaseLost)
			require.Zero(t, lock.acked.Load())
			require.Zero(t, lock.requeued.Load())
			require.Equal(t, int64(1), lock.released.Load())
		})
	}
}

// Reserve returns the fixed batch used by ownership-loss worker tests.
func (q leaseLossQueue) Reserve(context.Context, time.Duration) (ReservedBatchImageJob, error) {
	return ReservedBatchImageJob{BatchID: "imgbatch_lease"}, nil
}

// TryAcquireJobLock returns the configured ownership-loss handle as an
// acquired lease.
func (q leaseLossQueue) TryAcquireJobLock(context.Context, string, time.Duration) (BatchImageJobLock, bool, error) {
	return q.lock, true, nil
}

// Heartbeat returns the injected failure of the active ownership update.
func (l *leaseLossLock) Heartbeat(context.Context) error { return l.heartbeatErr }

// Refresh returns the injected failure of the lease renewal attempt.
func (l *leaseLossLock) Refresh(context.Context, time.Duration) error { return l.refreshErr }

// Ack counts an unexpected acknowledgement after the simulated ownership
// loss.
func (l *leaseLossLock) Ack(context.Context) error { l.acked.Add(1); return nil }

// RequeueAfter counts an unexpected delayed revisit after simulated ownership
// loss.
func (l *leaseLossLock) RequeueAfter(context.Context, time.Duration) error {
	l.requeued.Add(1)
	return nil
}

// Release counts deferred handle cleanup after the worker visit returns.
func (l *leaseLossLock) Release(context.Context) error { l.released.Add(1); return nil }

// Process waits for cancellation, stores its cause and then returns a
// terminal result. The worker test verifies that the recorded lease loss
// still wins over that terminal scheduling outcome.
func (p *leaseLossProcessor) Process(ctx context.Context, _ string) (BatchImageProcessResult, error) {
	// Delay the terminal result until heartbeat cancellation arrives so the test
	// exercises ownership precedence when processing appears to finish
	// successfully.
	<-ctx.Done()
	p.cause = context.Cause(ctx)
	return BatchImageProcessResult{Terminal: true}, nil
}
