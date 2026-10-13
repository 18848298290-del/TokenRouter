package batchimage

import (
	"context"
	"errors"
	"time"
)

// A process can stop after reserving funds and before saving its upstream job
// name. BillingRecovery finds old created or uploading records with positive
// holds, then conditionally fails each still-unsubmitted task before
// returning its reservation. Active submission heartbeats and saved provider
// names can make a candidate ineligible between the scan and update.
//
// The conditional update exposes whether it applied even when its later audit
// append fails. An applied failure still proceeds to release because the task
// has left the stale scan. A failed release enqueues that failed job for the
// normal processor release path, while successful release invalidates the
// payer authentication cache. The scan continues through individual failures
// and reports its released count together with the last observed error.

// Stale-submission defaults define the age cutoff and bounded scan size used
// when recovery options do not provide positive values.
const (
	defaultBatchImageBillingRecoveryStaleAfter = 10 * time.Minute
	defaultBatchImageBillingRecoveryLimit      = 100
)

// BillingRecovery returns held funds from persisted submissions abandoned
// before an upstream job was saved.
type BillingRecovery struct {
	Now            func() time.Time             // injected current time
	Repo           BatchImageRepository         // persistent abandoned submission reader
	Funding        Funding                      // shared task refund adapter
	Queue          BatchImageQueue              // optional terminal release retry queue
	StaleAfter     time.Duration                // maximum silent submission age
	Limit          int                          // stale jobs per scan
	InvalidateAuth func(context.Context, int64) // optional payer cache invalidation
	Observe        func(string, ...any)         // optional recovery diagnostic callback
}

// warn sends a stale-submission diagnostic through the configured observation
// callback.
func (s *BillingRecovery) warn(event string, values ...any) {
	if s.Observe != nil {
		s.Observe(event, values...)
	}
}

// ReleaseStaleUnsubmittedOnce fails abandoned submissions and returns how
// many reservations were released. Missing repository or funding dependencies
// return zero; nonpositive age and scan limits use the recovery defaults.
// Cancellation returns the number already released.
//
// Every scanned task is rechecked by the conditional failure update before
// funding is released. An applied update followed by an audit error still
// permits release, while a false result skips the task. Failed release
// schedules a queue retry because failed tasks disappear from the stale scan;
// other candidates continue, and the final error is the last unhandled
// per-task failure.
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
		// Successful release changes the payer's available funds, so invalidate
		// cached admission information before reporting this reservation as
		// recovered.
		if billingUserID := BillingUserID(job); s.InvalidateAuth != nil && billingUserID > 0 {
			s.InvalidateAuth(ctx, billingUserID)
		}
		released++
	}
	return released, lastErr
}

// EnqueueReleaseRetry requests a normal processor visit for a failed task
// whose hold remains reserved. An absent queue returns immediately, and
// duplicate queue membership is accepted while other enqueue failures are
// observed.
func (s *BillingRecovery) EnqueueReleaseRetry(ctx context.Context, batchID string) {
	if s == nil || s.Queue == nil {
		return
	}
	// Duplicate membership already supplies a worker visit for refund recovery.
	// Other publication failures are observed while the failed job keeps its hold.
	if err := s.Queue.Enqueue(ctx, batchID); err != nil && !errors.Is(err, ErrBatchImageAlreadyQueued) {
		s.warn("batch_image.recovery_release_retry_enqueue_failed",
			"batch_id", batchID,
			"error", err,
		)
	}
}

// now reads the recovery clock when a callback was injected at construction.
// Otherwise it uses time.Now, so tests can fix the cutoff while production
// observes the system time.
func (s *BillingRecovery) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
