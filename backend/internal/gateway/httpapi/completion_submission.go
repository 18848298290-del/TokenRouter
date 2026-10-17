package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// completion_submission.go submits HTTP completion tasks with entry-specific
// overflow and panic observation policies.
// Usage completion can outlive the HTTP request that produced its observations.
// The submission layer passes request values to completion's snapshot wrapper,
// then lets the configured pool choose ordinary overflow behavior. Constructors
// select synchronous fallback and panic observation policies for each entry family.

// CompletionSubmission captures completion work from the HTTP request goroutine.
type CompletionSubmission struct {
	// pool is the externally managed worker pool. Nil selects SubmitTask's inline
	// path; this adapter submits work but does not start or stop the pool.
	pool    *completion.UsageRecordWorkerPool
	options completion.SubmissionOptions // synchronous fallback and observation policy
}

// NewCompletionSubmission selects log sources for Messages or OpenAI endpoints.
// It enables inline execution after a stopped-pool rejection and recovers inline
// task panics through the observation callback. Ordinary queue overflow retains
// the pool's configured policy unless submission is mandatory.
func NewCompletionSubmission(pool *completion.UsageRecordWorkerPool, openAI bool) CompletionSubmission {
	prefix, component := "gateway", "handler.gateway.messages"
	if openAI {
		prefix, component = "openai", "handler.openai_gateway.responses"
	}
	return CompletionSubmission{pool: pool, options: completion.SubmissionOptions{
		FallbackWhenStopped: true,
		RecoverPanic:        true,
		Observe: func(event completion.SubmissionEvent) {
			name, source := prefix+".usage_record_task_stopped_sync_fallback", component
			if event.Mandatory {
				name, source = prefix+".usage_record_task_mandatory_sync_fallback", "handler.gateway.usage"
			}
			if event.Mandatory && openAI {
				source = "handler.openai_gateway.usage"
			}
			logger := logging.L().With(zap.String("component", source))
			if event.Panic != nil {
				logger.With(zap.Any("panic", event.Panic)).Error(prefix + ".usage_record_task_panic_recovered")
				return
			}
			logger.Warn(name)
		},
	}}
}

// NewQoderCompletionSubmission does not retry ordinary pool-rejected tasks inline.
// With no pool, it runs tasks with source values in a context detached from cancellation.
// The constructor enables PreserveSourceValues while leaving stopped-pool
// fallback and inline panic recovery disabled. Mandatory submission still uses
// SubmitTask's mandatory-overflow rule regardless of this constructor's ordinary policy.
func NewQoderCompletionSubmission(pool *completion.UsageRecordWorkerPool) CompletionSubmission {
	return CompletionSubmission{pool: pool, options: completion.SubmissionOptions{PreserveSourceValues: true}}
}

// CompletionContext snapshots correlation fields and the model mapping chain for completion work.
// Missing HTTP request data supplies a background source. The delegated snapshot
// holds completion correlation values rather than the mutable Gin context, and
// callers pass the returned context to completion capture consumers.
func CompletionContext(c *gin.Context) context.Context {
	return completion.SnapshotContext(completionSource(c))
}

// completionSource returns the current request context used by completion submission.
// Nil HTTP context or request yields a background context. Snapshotting and
// cancellation detachment happen in completion consumers, so this helper returns
// the live request source when available.
func completionSource(c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return context.Background()
	}
	return c.Request.Context()
}

// Submit sends an ordinary completion task through the configured submission policy.
// Nil tasks are ignored by SubmitTask. A pool may execute asynchronously or
// apply its overflow policy; configured stopped-pool fallback and nil-pool submission run inline.
func (s CompletionSubmission) Submit(c *gin.Context, task completion.UsageRecordTask) {
	completion.SubmitTask(s.pool, completionSource(c), task, false, s.options)
}

// SubmitMandatory requires dropped completion work to execute through inline fallback.
// Accepted work remains under the worker pool's execution policy. The caller
// captures request data before submitting the task, whose correlation context is
// provided by completion's wrapper.
func (s CompletionSubmission) SubmitMandatory(c *gin.Context, task completion.UsageRecordTask) {
	completion.SubmitTask(s.pool, completionSource(c), task, true, s.options)
}

// SubmitImages marks completion mandatory when a positive image count has been confirmed.
// Nonpositive counts use ordinary submission policy. Positive counts enable
// SubmitTask's fallback for a dropped task. Source identifies the completed
// request or socket turn independently of any later Gin request context changes.
func (s CompletionSubmission) SubmitImages(source context.Context, images int, task completion.UsageRecordTask) {
	completion.SubmitTask(s.pool, source, task, images > 0, s.options)
}
