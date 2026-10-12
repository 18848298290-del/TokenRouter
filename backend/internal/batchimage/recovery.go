package batchimage

import (
	"context"
	"errors"
	"time"
)

const (
	defaultBatchImageBillingRecoveryStaleAfter = 10 * time.Minute
	defaultBatchImageBillingRecoveryLimit      = 100
)

type BillingRecovery struct {
	Now            func() time.Time
	Repo           BatchImageRepository
	Funding        Funding
	Queue          BatchImageQueue
	StaleAfter     time.Duration
	Limit          int
	InvalidateAuth func(context.Context, int64)
	Observe        func(string, ...any)
}

func (s *BillingRecovery) warn(event string, values ...any) {
	if s.Observe != nil {
		s.Observe(event, values...)
	}
}

func (s *BillingRecovery) ReleaseStaleUnsubmittedOnce(ctx context.Context) (int, error) {
	if s == nil || s.Repo == nil || s.Funding.Store == nil {
		return 0, nil
	}
	staleAfter := s.StaleAfter
	if staleAfter <= 0 {
		staleAfter = defaultBatchImageBillingRecoveryStaleAfter
	}
	limit := s.Limit
	if limit <= 0 {
		limit = defaultBatchImageBillingRecoveryLimit
	}
	cutoff := s.now().Add(-staleAfter)
	jobs, err := s.Repo.ListStaleUnsubmittedBatchImageJobs(ctx, cutoff, limit)
	if err != nil {
		return 0, err
	}
	released := 0
	var lastErr error
	for _, job := range jobs {
		if job == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return released, err
		}
		msg := "batch image submission did not reach platform before recovery cutoff"
		// Recheck staleness in the state update: submission may have refreshed its heartbeat
		// or saved provider_job_name since the list query. Releasing funds in that case
		// would refund a job that is still incurring upstream costs.
		applied, err := s.Repo.FailStaleUnsubmittedBatchImageJob(ctx, job.BatchID, cutoff, "SUBMIT_STALE_BEFORE_PROVIDER", msg)
		if err != nil {
			// With applied=true the state update committed and only the audit write failed.
			// Continue releasing funds because failed jobs disappear from the stale list.
			if !applied {
				lastErr = err
				continue
			}
			s.warn("batch_image.recovery_fail_event_append_failed",
				"batch_id", job.BatchID,
				"error", err,
			)
		}
		if !applied {
			continue
		}
		job.Status = BatchImageJobStatusFailed
		if err := s.Funding.Release(ctx, job, BatchImageDerefString(job.RequestHash)); err != nil {
			// Failed jobs disappear from the stale list. Enqueue failed releases so the
			// worker can retry through ReleaseTerminalHold; otherwise these reservations
			// would remain held indefinitely.
			s.warn("batch_image.recovery_release_failed",
				"batch_id", job.BatchID,
				"error", err,
			)
			s.EnqueueReleaseRetry(ctx, job.BatchID)
			lastErr = err
			continue
		}
		if billingUserID := BillingUserID(job); s.InvalidateAuth != nil && billingUserID > 0 {
			s.InvalidateAuth(ctx, billingUserID)
		}
		released++
	}
	return released, lastErr
}

func (s *BillingRecovery) EnqueueReleaseRetry(ctx context.Context, batchID string) {
	if s == nil || s.Queue == nil {
		return
	}
	if err := s.Queue.Enqueue(ctx, batchID); err != nil && !errors.Is(err, ErrBatchImageAlreadyQueued) {
		s.warn("batch_image.recovery_release_retry_enqueue_failed",
			"batch_id", batchID,
			"error", err,
		)
	}
}

// now uses the injected clock when configured, otherwise the system clock.
func (s *BillingRecovery) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
