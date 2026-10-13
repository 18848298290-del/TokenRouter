package app

import (
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/moderationflow"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// Cyber-policy handling uses the OpenAI recorder and shared session-block
// state. Ops reporting is attached when both its service and queue are
// available, and the moderation handler receives the application task tracker
// for its background completion work.

// cyberOpsWriter connects Cyber error records to the assembled Ops queue.
type cyberOpsWriter struct {
	service *ops.OpsService    // error persistence service
	queue   *ops.ErrorLogQueue // asynchronous error queue
}

// Enqueue submits the Cyber error record with its target Ops service.
func (w cyberOpsWriter) Enqueue(value *ops.OpsInsertErrorLogInput) { w.queue.Enqueue(w.service, value) }

// provideCyberHTTP binds Cyber session blocks, completion, and tracked
// background work. Moderation is attached when supplied; Ops reporting requires
// both service and queue, so partially supplied reporting dependencies leave
// that capability unset.
func provideCyberHTTP(core *session.CyberBlocks, records GatewayCompletionRecorders, tasks *lifecycle.Tasks, moderator *moderation.ContentModerationService, opsService *ops.OpsService, queue *ops.ErrorLogQueue) *gatewayhttp.CyberHandler {
	runtime := moderationflow.Runtime{Recorder: records.OpenAI, Blocks: core, Tasks: tasks}
	if opsService != nil && queue != nil {
		runtime.Ops = cyberOpsWriter{opsService, queue}
	}
	var port gatewayhttp.ModerationPort
	if moderator != nil {
		port = moderator
	}
	return gatewayhttp.NewBoundCyberHandler(core, port, runtime)
}

// provideCyberBlocks binds cached session blocks to runtime moderation settings
// and diagnostics.
func provideCyberBlocks(cache session.GatewayCache, settings *moderation.RuntimeSettings) *session.CyberBlocks {
	return session.NewCyberBlocks(session.AdaptCyberSessionBlockStore(cache), settings.GetCyberSessionBlockRuntime, func(format string, args ...any) { logging.LegacyPrintf("service.openai_gateway", format, args...) })
}
