package batchimage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

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

type BatchImageProcessor interface {
	Process(ctx context.Context, batchID string) (BatchImageProcessResult, error)
}

type BatchImageProcessResult struct {
	RequeueAfter time.Duration
	Terminal     bool
}

type BatchImageWorkerOptions struct {
	Observe             func(string, ...any)
	ReserveBlockTimeout time.Duration
	JobLockTTL          time.Duration
	LockConflictDelay   time.Duration
	DefaultRequeueDelay time.Duration
	ErrorRetryDelay     time.Duration
	ErrorBackoff        time.Duration
	DelayedPollInterval time.Duration
	RecoveryInterval    time.Duration
	StaleActiveAfter    time.Duration
	DelayedMoveLimit    int
	RecoverLimit        int
}

type BatchImageWorker struct {
	queue     BatchImageQueue
	processor BatchImageProcessor
	opts      BatchImageWorkerOptions
}

func NewBatchImageWorker(queue BatchImageQueue, processor BatchImageProcessor, opts BatchImageWorkerOptions) *BatchImageWorker {
	return &BatchImageWorker{
		queue:     queue,
		processor: processor,
		opts:      NormalizeBatchImageWorkerOptions(opts),
	}
}

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

func (w *BatchImageWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			SleepOrDone(ctx, w.opts.ErrorBackoff)
		}
	}
}

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
		if requeueErr := w.queue.RequeueAfter(ctx, reserved.BatchID, w.opts.LockConflictDelay); requeueErr != nil {
			return requeueErr
		}
		return err
	}
	if !ok {
		// On lock contention, request a requeue after the conflict delay. Dropping the job
		// would leave it active until StaleActiveAfter recovery, delaying work for minutes.
		return w.queue.RequeueAfter(ctx, reserved.BatchID, w.opts.LockConflictDelay)
	}
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
	if cause := context.Cause(processCtx); errors.Is(cause, ErrBatchImageLeaseLost) {
		return cause
	}
	cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancelCleanup()
	if err != nil {
		w.warn("batch_image.worker_process_failed",
			"batch_id", reserved.BatchID,
			"error", err,
		)
		return lock.RequeueAfter(cleanup, w.opts.ErrorRetryDelay)
	}
	if result.Terminal {
		return lock.Ack(cleanup)
	}
	delay := result.RequeueAfter
	if delay <= 0 {
		delay = w.opts.DefaultRequeueDelay
	}
	return lock.RequeueAfter(cleanup, delay)
}

func (w *BatchImageWorker) heartbeatInterval() time.Duration {
	interval := min(w.opts.StaleActiveAfter, w.opts.JobLockTTL)
	interval /= 3
	if interval < time.Second {
		interval = time.Second
	}
	return interval
}

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
				cancel(fmt.Errorf("%w: %v", ErrBatchImageLeaseLost, err))
				return
			}
			if refresher, ok := lock.(BatchImageJobLockRefresher); ok {
				if err := refresher.Refresh(ctx, w.opts.JobLockTTL); err != nil && ctx.Err() == nil {
					w.warn("batch_image.worker_lock_refresh_failed",
						"batch_id", batchID,
						"error", err,
					)
					cancel(fmt.Errorf("%w: %v", ErrBatchImageLeaseLost, err))
					return
				}
			}
		}
	}
}

func (w *BatchImageWorker) MoveDueDelayedOnce(ctx context.Context) (int, error) {
	if w == nil || w.queue == nil {
		return 0, nil
	}
	return w.queue.MoveDueDelayedToReady(ctx, w.opts.DelayedMoveLimit)
}

func (w *BatchImageWorker) RunDelayedMover(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		moved, _ := w.MoveDueDelayedOnce(ctx)
		if moved > 0 {
			continue
		}
		SleepOrDone(ctx, w.opts.DelayedPollInterval)
	}
}

func (w *BatchImageWorker) RecoverStaleActiveOnce(ctx context.Context) (int, error) {
	if w == nil || w.queue == nil {
		return 0, nil
	}
	return w.queue.RecoverStaleActive(ctx, w.opts.StaleActiveAfter, w.opts.RecoverLimit)
}

func (w *BatchImageWorker) RunStaleActiveRecovery(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		_, _ = w.RecoverStaleActiveOnce(ctx)
		SleepOrDone(ctx, w.opts.RecoveryInterval)
	}
}

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

// Options returns a copy of runtime parameters, including intervals used by recovery loops.
func (w *BatchImageWorker) Options() BatchImageWorkerOptions { return w.opts }

func (w *BatchImageWorker) warn(event string, fields ...any) {
	if w.opts.Observe != nil {
		w.opts.Observe(event, fields...)
	}
}
