package batchimage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Each worker visit begins with a Redis reservation and then acquires the job
// lock before reading or advancing persistent task state. Queue activity and
// lock renewal keep the visit owned during long provider work; the held lock
// handle carries the token used by heartbeat, acknowledgement and delayed
// requeue.
//
// While processing remains active, a heartbeat or renewal failure cancels it
// with ErrBatchImageLeaseLost because ownership is unconfirmed. The worker
// joins that heartbeat before deciding the queue outcome and skips
// acknowledgement or requeue after ownership loss. Normal completion and
// errors use a short context detached from request cancellation to publish
// the queue outcome and release the lock. Separate loops move due delays and
// recover stale active entries so later visits can resume persisted progress.

// Worker defaults bound idle reservations, lease renewal, polling delays and
// recovery batches when configured options do not supply positive values.
const (
	DefaultBatchImageWorkerLockTTL             = 5 * time.Minute
	DefaultBatchImageWorkerLockConflictDelay   = 5 * time.Second
	DefaultBatchImageWorkerErrorRetryDelay     = time.Minute
	DefaultBatchImageWorkerRequeueDelay        = 30 * time.Second
	DefaultBatchImageWorkerDelayedPollInterval = 5 * time.Second
	DefaultBatchImageWorkerRecoveryInterval    = 5 * time.Minute
	DefaultBatchImageWorkerStaleActiveAfter    = 10 * time.Minute
	DefaultBatchImageWorkerDelayedMoveLimit    = 100
	DefaultBatchImageWorkerRecoverLimit        = 100
	DefaultBatchImageWorkerErrorBackoff        = time.Second
	DefaultBatchImageWorkerReserveBlockTimeout = 5 * time.Second
)

// BatchImageProcessor advances one reserved batch using its persistent
// lifecycle stage.
type BatchImageProcessor interface {
	// Process advances the persisted batch selected by a queue reservation and
	// returns its scheduling outcome. Implementations honor cancellation when
	// lease ownership is lost, so the caller can leave successor queue state
	// untouched.
	Process(ctx context.Context, batchID string) (BatchImageProcessResult, error)
}

// BatchImageProcessResult tells the worker whether to acknowledge or revisit
// a batch.
type BatchImageProcessResult struct {
	RequeueAfter time.Duration // suggested next visit delay
	// Terminal takes precedence over RequeueAfter when RunOnce chooses a queue
	// outcome. A terminal result is acknowledged; other results use a positive
	// requested delay or the worker default.
	Terminal bool // acknowledge this queue visit
}

// BatchImageWorkerOptions sets queue timing, lease maintenance and recovery
// batch sizes.
type BatchImageWorkerOptions struct {
	Observe             func(string, ...any) // optional worker diagnostic callback
	ReserveBlockTimeout time.Duration        // maximum idle reserve wait
	JobLockTTL          time.Duration        // held job lease duration
	LockConflictDelay   time.Duration        // unlocked contention retry delay
	DefaultRequeueDelay time.Duration        // unspecified processor visit delay
	ErrorRetryDelay     time.Duration        // processor failure visit delay
	ErrorBackoff        time.Duration        // failed visit loop delay
	DelayedPollInterval time.Duration        // empty delayed scan interval
	RecoveryInterval    time.Duration        // stale queue scan interval
	StaleActiveAfter    time.Duration        // old activity recovery window
	DelayedMoveLimit    int                  // delayed entries per scan
	RecoverLimit        int                  // stale entries per scan
}

// BatchImageWorker owns queue visits and coordinates processor work with the
// held job lease.
type BatchImageWorker struct {
	queue     BatchImageQueue         // queue coordination dependency
	processor BatchImageProcessor     // persistent job progression dependency
	opts      BatchImageWorkerOptions // normalized worker parameter snapshot
}

// NewBatchImageWorker retains the queue and processor and normalizes its
// runtime options. Construction starts no loops; application runtime invokes
// Run and the maintenance loops when queue processing is enabled.
func NewBatchImageWorker(queue BatchImageQueue, processor BatchImageProcessor, opts BatchImageWorkerOptions) *BatchImageWorker {
	return &BatchImageWorker{
		queue:     queue,
		processor: processor,
		opts:      NormalizeBatchImageWorkerOptions(opts),
	}
}

// NormalizeBatchImageWorkerOptions supplies defaults for nonpositive queue
// timeouts, lease durations, delays and batch limits. Positive values and the
// observation callback remain as supplied for the constructed worker.
func NormalizeBatchImageWorkerOptions(opts BatchImageWorkerOptions) BatchImageWorkerOptions {
	if opts.ReserveBlockTimeout <= 0 {
		opts.ReserveBlockTimeout = DefaultBatchImageWorkerReserveBlockTimeout
	}
	if opts.JobLockTTL <= 0 {
		opts.JobLockTTL = DefaultBatchImageWorkerLockTTL
	}
	if opts.LockConflictDelay <= 0 {
		opts.LockConflictDelay = DefaultBatchImageWorkerLockConflictDelay
	}
	if opts.DefaultRequeueDelay <= 0 {
		opts.DefaultRequeueDelay = DefaultBatchImageWorkerRequeueDelay
	}
	if opts.ErrorRetryDelay <= 0 {
		opts.ErrorRetryDelay = DefaultBatchImageWorkerErrorRetryDelay
	}
	if opts.ErrorBackoff <= 0 {
		opts.ErrorBackoff = DefaultBatchImageWorkerErrorBackoff
	}
	if opts.DelayedPollInterval <= 0 {
		opts.DelayedPollInterval = DefaultBatchImageWorkerDelayedPollInterval
	}
	if opts.RecoveryInterval <= 0 {
		opts.RecoveryInterval = DefaultBatchImageWorkerRecoveryInterval
	}
	if opts.StaleActiveAfter <= 0 {
		opts.StaleActiveAfter = DefaultBatchImageWorkerStaleActiveAfter
	}
	if opts.DelayedMoveLimit <= 0 {
		opts.DelayedMoveLimit = DefaultBatchImageWorkerDelayedMoveLimit
	}
	if opts.RecoverLimit <= 0 {
		opts.RecoverLimit = DefaultBatchImageWorkerRecoverLimit
	}
	return opts
}

// Run repeatedly consumes one queue visit until its context is cancelled.
// Failed visits wait for ErrorBackoff while the context remains active; a nil
// worker returns immediately to its runtime owner.
func (w *BatchImageWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			// A failed reservation or queue mutation can recur immediately; pause the
			// consumer loop while preserving prompt shutdown through the same context.
			SleepOrDone(ctx, w.opts.ErrorBackoff)
		}
	}
}

// RunOnce reserves one queued batch and advances it while holding its job
// lease. An empty queue or absent dependencies completes the visit
// successfully. Failed acquisition asks for a tokenless delayed requeue,
// which the Redis adapter permits only when no lock currently owns the task.
//
// Processing runs with cancellation tied to heartbeat and optional renewal.
// The worker joins heartbeat before selecting its outcome; lease loss returns
// immediately and leaves successor queue state intact. Otherwise an error
// schedules ErrorRetryDelay, a terminal result acknowledges the job, and a
// positive requested delay or default schedules another visit. Queue cleanup
// and deferred lease release each use a detached two-second budget.
func (w *BatchImageWorker) RunOnce(ctx context.Context) error {
	if w == nil || w.queue == nil || w.processor == nil {
		return nil
	}

	reserved, err := w.queue.Reserve(ctx, w.opts.ReserveBlockTimeout)
	if errors.Is(err, ErrBatchImageQueueEmpty) {
		return nil
	}
	if err != nil {
		return err
	}

	lock, ok, err := w.queue.TryAcquireJobLock(ctx, reserved.BatchID, w.opts.JobLockTTL)
	if err != nil {
		// Acquisition failure leaves this reservation unprocessed. Ask for a later
		// visit, subject to the tokenless adapter rejecting any current lease owner.
		if requeueErr := w.queue.RequeueAfter(ctx, reserved.BatchID, w.opts.LockConflictDelay); requeueErr != nil {
			return requeueErr
		}
		return err
	}
	if !ok {
		// Lock contention leaves this reservation unprocessed. The tokenless requeue
		// succeeds only after the competing lock is absent; otherwise stale-active
		// recovery remains responsible for a later visit.
		return w.queue.RequeueAfter(ctx, reserved.BatchID, w.opts.LockConflictDelay)
	}
	// Processing may end through caller cancellation, so release uses a short
	// detached context to relinquish this token while the queue remains
	// reachable.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = lock.Release(cleanup)
	}()

	// Keep active timestamps fresh during processing so stale recovery does not
	// redeliver the job, and extend the lock TTL when the lock supports renewal.
	hbStop := make(chan struct{})
	hbDone := make(chan struct{})
	processCtx, cancelProcess := context.WithCancelCause(ctx)
	defer cancelProcess(nil)
	go w.runJobHeartbeat(processCtx, reserved.BatchID, lock, hbStop, hbDone, cancelProcess)

	result, err := w.processor.Process(processCtx, reserved.BatchID)
	close(hbStop)
	<-hbDone
	// Heartbeat can cancel processing even when the processor reports success;
	// the cancellation cause takes precedence before touching any queue outcome.
	if cause := context.Cause(processCtx); errors.Is(cause, ErrBatchImageLeaseLost) {
		return cause
	}
	// Queue outcome publication gets a bounded detached cleanup context so
	// cancellation during processing does not discard an otherwise owned visit.
	cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancelCleanup()
	if err != nil {
		w.warn("batch_image.worker_process_failed",
			"batch_id", reserved.BatchID,
			"error", err,
		)
		// A processor error leaves its persisted stage available for recovery;
		// delay another owned queue visit rather than acknowledging incomplete
		// work.
		return lock.RequeueAfter(cleanup, w.opts.ErrorRetryDelay)
	}
	if result.Terminal {
		// Terminal processing needs no scheduled polling visit; retire this token's
		// active, delayed and inflight queue markers before releasing its lease.
		return lock.Ack(cleanup)
	}
	delay := result.RequeueAfter
	if delay <= 0 {
		delay = w.opts.DefaultRequeueDelay
	}
	// Polling and settlement can need further visits; move this owned entry back
	// to the requested delay after its persistent progress is saved.
	return lock.RequeueAfter(cleanup, delay)
}

// heartbeatInterval selects one third of the smaller stale-active window and
// lock TTL. A one-second floor keeps the ticker valid for very short
// configured durations, which can reduce the renewal margin.
func (w *BatchImageWorker) heartbeatInterval() time.Duration {
	interval := min(w.opts.StaleActiveAfter, w.opts.JobLockTTL)
	// Divide the smaller window into three scheduled renewal opportunities.
	interval /= 3
	if interval < time.Second {
		// The one-second ticker floor bounds heartbeat frequency for very small
		// configured lease or stale windows.
		interval = time.Second
	}
	return interval
}

// runJobHeartbeat refreshes queue activity and renews a lease when its handle
// supports Refresh. It returns after the stop signal or context cancellation
// and closes done so RunOnce can join the goroutine. The caller supplies the
// processing cancellation function.
//
// Any heartbeat or renewal error while the processing context is active means
// ownership is unconfirmed. The loop wraps that failure as
// ErrBatchImageLeaseLost, cancels processing and returns before the old
// worker can acknowledge or requeue the task. Successful queue heartbeat
// precedes lock renewal on each tick.
func (w *BatchImageWorker) runJobHeartbeat(ctx context.Context, batchID string, lock BatchImageJobLock, stop <-chan struct{}, done chan<- struct{}, cancel context.CancelCauseFunc) {
	defer close(done)
	ticker := time.NewTicker(w.heartbeatInterval())
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := lock.Heartbeat(ctx); err != nil && ctx.Err() == nil {
				w.warn("batch_image.worker_heartbeat_failed",
					"batch_id", batchID,
					"error", err,
				)
				// An unsuccessful activity write leaves lease ownership unconfirmed;
				// cancel the processor before it can publish a queue outcome for a
				// possible successor.
				cancel(fmt.Errorf("%w: %v", ErrBatchImageLeaseLost, err))
				return
			}
			if refresher, ok := lock.(BatchImageJobLockRefresher); ok {
				if err := refresher.Refresh(ctx, w.opts.JobLockTTL); err != nil && ctx.Err() == nil {
					w.warn("batch_image.worker_lock_refresh_failed",
						"batch_id", batchID,
						"error", err,
					)
					// Renewal failure can mean a new token already owns the job; cancel
					// processing and preserve queue state for that owner.
					cancel(fmt.Errorf("%w: %v", ErrBatchImageLeaseLost, err))
					return
				}
			}
		}
	}
}

// MoveDueDelayedOnce moves at most the configured delayed batch limit into
// the ready queue. Nil workers or absent queues return zero, allowing the
// maintenance loop to wait for its next poll.
func (w *BatchImageWorker) MoveDueDelayedOnce(ctx context.Context) (int, error) {
	if w == nil || w.queue == nil {
		return 0, nil
	}
	return w.queue.MoveDueDelayedToReady(ctx, w.opts.DelayedMoveLimit)
}

// RunDelayedMover moves due delayed entries until context cancellation.
// Positive batches are drained immediately, while an empty batch or move
// error waits for DelayedPollInterval before the next scan.
func (w *BatchImageWorker) RunDelayedMover(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		// Move failures and empty scans both wait below; a later poll retries Redis
		// while cancellation still ends this maintenance loop.
		moved, _ := w.MoveDueDelayedOnce(ctx)
		if moved > 0 {
			continue
		}
		SleepOrDone(ctx, w.opts.DelayedPollInterval)
	}
}

// RecoverStaleActiveOnce requeues at most the configured recovery limit of
// entries older than StaleActiveAfter. Nil workers or absent queues return
// zero and leave the caller to schedule its next scan.
func (w *BatchImageWorker) RecoverStaleActiveOnce(ctx context.Context) (int, error) {
	if w == nil || w.queue == nil {
		return 0, nil
	}
	return w.queue.RecoverStaleActive(ctx, w.opts.StaleActiveAfter, w.opts.RecoverLimit)
}

// RunStaleActiveRecovery scans for old active queue entries until context
// cancellation. Each scan is followed by RecoveryInterval even on an error;
// persisted task state determines progress when a recovered entry is
// processed.
func (w *BatchImageWorker) RunStaleActiveRecovery(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		// Periodic recovery remains available after an individual Redis failure;
		// keep its interval so repeated failures do not produce a busy scan loop.
		_, _ = w.RecoverStaleActiveOnce(ctx)
		SleepOrDone(ctx, w.opts.RecoveryInterval)
	}
}

// SleepOrDone waits for the supplied duration or an earlier context
// cancellation. Nonpositive durations return immediately, and the function
// stops its timer when the wait finishes for either reason.
func SleepOrDone(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// Options returns the worker parameter values used by background recovery
// loops.
func (w *BatchImageWorker) Options() BatchImageWorkerOptions { return w.opts }

// warn sends a worker diagnostic through its configured observation callback.
func (w *BatchImageWorker) warn(event string, fields ...any) {
	if w.opts.Observe != nil {
		w.opts.Observe(event, fields...)
	}
}
