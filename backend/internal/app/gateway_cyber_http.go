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

type cyberOpsWriter struct {
	service *ops.OpsService
	queue   *ops.ErrorLogQueue
}

func (w cyberOpsWriter) Enqueue(value *ops.OpsInsertErrorLogInput) { w.queue.Enqueue(w.service, value) }

// provideCyberHTTP shares background task tracking, session blocks, completion recording, and Ops reporting.
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

func provideCyberBlocks(cache session.GatewayCache, settings *moderation.RuntimeSettings) *session.CyberBlocks {
	return session.NewCyberBlocks(session.AdaptCyberSessionBlockStore(cache), settings.GetCyberSessionBlockRuntime, func(format string, args ...any) { logging.LegacyPrintf("service.openai_gateway", format, args...) })
}
