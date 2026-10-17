package completion

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// submission.go submits completion tasks and applies caller-selected inline
// policies when submission is rejected. Completion queue rejection can occur
// because capacity is exhausted or because the pool is stopping. Mandatory work
// retries either rejection inline, while an ordinary task follows the
// configured overflow policy unless its entrypoint opts into stopped-pool
// fallback. A missing pool also executes directly in the caller.
//
// Correlation values are captured before submission. Inline execution gets a
// ten-second context detached from source cancellation, optionally retaining all
// source values, and optional panic recovery reports the failure to the observer.
// Pool-accepted work uses the pool's own timeout and panic handling. The inline
// retry runs after the pool reports rejection and belongs to the submitting caller.

// SubmissionEvent describes synchronous fallback or a panic for the app logging callback.
type SubmissionEvent struct {
	Mandatory bool // mandatory submission flag
	// Panic holds a recovered inline-task value; nil identifies the earlier
	// fallback notice emitted after the pool rejects this submission.
	Panic any
}

// SubmissionOptions controls stopped-pool fallback, source values, and panic recovery.
// The supplied worker pool selects queued or synchronous overflow execution.
type SubmissionOptions struct {
	// FallbackWhenStopped executes ordinary stopped-pool rejections inline;
	// mandatory tasks take that path for either stopped or full-queue rejection.
	FallbackWhenStopped bool
	// PreserveSourceValues retains arbitrary source values during inline
	// execution; the normal wrapper already carries selected completion values.
	PreserveSourceValues bool
	// RecoverPanic consumes an inline task panic and reports it when Observe
	// is configured; pool execution has its own independent panic handling.
	RecoverPanic bool
	Observe      func(SubmissionEvent) // synchronous fallback observer
}

// SubmitTask captures request correlation and submits eligible completion work.
// Nil tasks are ignored. An accepted task runs according to the pool's policy,
// which can include synchronous overflow execution before this function returns.
//
// Mandatory rejected tasks and opted-in stopped-pool rejections run inline under
// a detached ten-second budget; with no pool, every task runs inline. The observer
// first receives a fallback notice after rejection and can later receive a
// recovered task panic. Callers snapshot mutable task inputs before submission
// and wait for their own inline work during shutdown.
func SubmitTask(pool *UsageRecordWorkerPool, source context.Context, task UsageRecordTask, mandatory bool, options SubmissionOptions) {
	if task == nil {
		return
	}
	original := task
	observeUsageWrite(source, "pending", "", nil)
	task = WrapTaskContext(source, func(ctx context.Context) {
		defer func() {
			if value := recover(); value != nil {
				p := timing.PerformanceFromContext(ctx)
				// A later task panic cannot invalidate a database acknowledgement.
				// Preserve a known write result and attach the task failure as diagnostic context.
				telemetry.UpdateRequest(ctx, func(record *telemetry.RequestRecord) {
					if record.UsageWriteState == "pending" || record.UsageWriteState == "" {
						record.UsageWriteState = "unknown"
						record.FailureTimings = p.FailureTimings()
					}
					record.UsageWriteError = "completion task panicked"
					record.UsageWriteUpdatedAt = time.Now().UTC()
				})
				panic(value)
			}
			p := timing.PerformanceFromContext(ctx)
			telemetry.UpdateRequest(ctx, func(record *telemetry.RequestRecord) {
				if record.UsageWriteState == "pending" {
					record.UsageWriteState = "skipped"
					record.UsageWriteError = "completion ended without a usage write"
					record.UsageWriteUpdatedAt = time.Now().UTC()
					record.FailureTimings = p.FailureTimings()
				}
			})
		}()
		original(ctx)
	})
	if pool != nil {
		mode := pool.Submit(task)
		// Distinguish shutdown from configured congestion loss: ordinary drop/sample
		// rejections stay rejected unless mandatory completion requires an inline retry.
		fallback := mandatory && mode.Dropped() || options.FallbackWhenStopped && mode == UsageRecordSubmitModeDroppedStopped
		if !fallback {
			if mode.Dropped() {
				observeUsageWrite(source, "not_persisted", "completion queue rejected task: "+mode.String(), nil)
			}
			return
		}
		if options.Observe != nil {
			options.Observe(SubmissionEvent{Mandatory: mandatory})
		}
	}
	// Inline work can follow a cancelled request, so give it a separate budget.
	// Optional source-value retention changes values while detaching cancellation.
	base := context.Background()
	if options.PreserveSourceValues && source != nil {
		base = context.WithoutCancel(source)
	}
	ctx, cancel := context.WithTimeout(base, 10*time.Second)
	defer cancel()
	// Inline panics have no pond worker to recover them; an enabled policy consumes
	// them here and emits the captured value when an observer is available.
	if options.RecoverPanic {
		defer func() {
			if value := recover(); value != nil && options.Observe != nil {
				options.Observe(SubmissionEvent{Mandatory: mandatory, Panic: value})
			}
		}()
	}
	task(ctx)
}
