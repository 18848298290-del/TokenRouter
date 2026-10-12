package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// CompletionSubmission captures completion work from the HTTP request goroutine.
type CompletionSubmission struct {
	pool    *completion.UsageRecordWorkerPool
	options completion.SubmissionOptions
}

// NewCompletionSubmission selects log sources for Messages or OpenAI endpoints.
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
func NewQoderCompletionSubmission(pool *completion.UsageRecordWorkerPool) CompletionSubmission {
	return CompletionSubmission{pool: pool, options: completion.SubmissionOptions{PreserveSourceValues: true}}
}

// CompletionContext snapshots correlation fields and the model mapping chain for completion work.
func CompletionContext(c *gin.Context) context.Context {
	return completion.SnapshotContext(completionSource(c))
}

func completionSource(c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return context.Background()
	}
	return c.Request.Context()
}

func (s CompletionSubmission) Submit(c *gin.Context, task completion.UsageRecordTask) {
	completion.SubmitTask(s.pool, completionSource(c), task, false, s.options)
}

func (s CompletionSubmission) SubmitMandatory(c *gin.Context, task completion.UsageRecordTask) {
	completion.SubmitTask(s.pool, completionSource(c), task, true, s.options)
}

// SubmitImages marks completion mandatory when a positive image count has been confirmed.
func (s CompletionSubmission) SubmitImages(c *gin.Context, images int, task completion.UsageRecordTask) {
	completion.SubmitTask(s.pool, completionSource(c), task, images > 0, s.options)
}
