package app

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/audit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	moderationcore "github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

type queuesRuntimeReady struct{}

func provideQueuesRuntime(
	emailQueue *notification.EmailQueueService,
	billingCache *billing.Eligibility,
	usageRecordWorkerPool *completion.UsageRecordWorkerPool,
	ollamaCloudUsage *provider.OllamaCloudUsageService,
	auditLog *audit.AuditLogService,
	grokQuota *provider.GrokQuotaService,
	openaiQuota *provider.OpenAIQuotaService,
	manager *lifecycle.Manager,
	deferred *provider.DeferredService,
	contentModeration *moderationcore.ContentModerationService,
) *queuesRuntimeReady {
	manager.Register(lifecycle.Hook{Name: "EmailQueueService", StartOrder: 930, StopOrder: 70, Start: func(ctx context.Context) error {
		if emailQueue != nil {
			emailQueue.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if emailQueue != nil {
			return emailQueue.StopContext(ctx)
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "BillingCacheService", StartOrder: 950, StopOrder: 50, Start: func(ctx context.Context) error {
		if billingCache != nil {
			billingCache.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if billingCache != nil {
			billingCache.Stop()
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "UsageRecordWorkerPool", StartOrder: 960, StopOrder: 40, Start: func(ctx context.Context) error {
		if usageRecordWorkerPool != nil {
			usageRecordWorkerPool.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if usageRecordWorkerPool != nil {
			return usageRecordWorkerPool.StopContext(ctx)
		}
		return nil
	}})

	manager.Register(lifecycle.Hook{Name: "OllamaCloudUsageService", StartOrder: 940, StopOrder: 60, Start: func(ctx context.Context) error {
		if ollamaCloudUsage != nil {
			return ollamaCloudUsage.StartContext(ctx)
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if ollamaCloudUsage != nil {
			return ollamaCloudUsage.StopContext(ctx)
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "AuditLogService", StartOrder: 925, StopOrder: 75, Start: func(ctx context.Context) error {
		if auditLog != nil {
			auditLog.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if auditLog != nil {
			return auditLog.StopContext(ctx)
		}
		return nil
	}})

	manager.Register(lifecycle.Hook{Name: "DeferredService", StartOrder: 935, StopOrder: 65, Start: func(ctx context.Context) error {
		if deferred != nil {
			deferred.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if deferred != nil {
			return deferred.StopContext(ctx)
		}
		return nil
	}})

	manager.Register(lifecycle.Hook{Name: "ContentModerationService", StartOrder: 955, StopOrder: 45, Start: func(ctx context.Context) error {
		if contentModeration != nil {
			contentModeration.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if contentModeration != nil {
			return contentModeration.StopContext(ctx)
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "GrokQuotaProbes", StopOrder: 25, Stop: grokQuota.StopContext})
	// Cancel and wait for admin queries and platform quota operations before shared dependencies close.
	manager.Register(lifecycle.Hook{Name: "OpenAIQuotaService", StopOrder: 35, Stop: openaiQuota.StopContext})
	return &queuesRuntimeReady{}
}
