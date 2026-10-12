package completion

import (
	"context"
	"time"
)

// SubmissionEvent describes synchronous fallback or a panic for the app logging callback.
type SubmissionEvent struct {
	Mandatory bool
	Panic     any
}

// SubmissionOptions controls stopped-pool fallback, source values, and panic recovery.
// The supplied worker pool executes asynchronous tasks.
type SubmissionOptions struct {
	FallbackWhenStopped  bool
	PreserveSourceValues bool
	RecoverPanic         bool
	Observe              func(SubmissionEvent)
}

// SubmitTask snapshots request correlation data and submits the task.
// Mandatory dropped tasks run synchronously; ordinary overflow follows the configured drop or sample policy.
func SubmitTask(pool *UsageRecordWorkerPool, source context.Context, task UsageRecordTask, mandatory bool, options SubmissionOptions) {
	if task == nil {
		return
	}
	task = WrapTaskContext(source, task)
	if pool != nil {
		mode := pool.Submit(task)
		fallback := mandatory && mode.Dropped() || options.FallbackWhenStopped && mode == UsageRecordSubmitModeDroppedStopped
		if !fallback {
			return
		}
		if options.Observe != nil {
			options.Observe(SubmissionEvent{Mandatory: mandatory})
		}
	}
	base := context.Background()
	if options.PreserveSourceValues && source != nil {
		base = context.WithoutCancel(source)
	}
	ctx, cancel := context.WithTimeout(base, 10*time.Second)
	defer cancel()
	if options.RecoverPanic {
		defer func() {
			if value := recover(); value != nil && options.Observe != nil {
				options.Observe(SubmissionEvent{Mandatory: mandatory, Panic: value})
			}
		}()
	}
	task(ctx)
}
