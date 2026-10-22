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

// runtime_queues.go registers asynchronous consumer lifecycles and platform
// quota-operation shutdown hooks. Completion recording, billing-cache updates,
// moderation, deferred writes, email, and audit each have distinct stop levels.
// This keeps request-produced side effects ahead of the queues or stores they
// use; quota-operation shutdown is registered separately from those background
// consumer loops.

// queuesRuntimeReady marks registration of asynchronous consumers and platform
// quota-operation shutdown.
type queuesRuntimeReady struct{}

// provideQueuesRuntime registers queue consumers and quota stop callbacks
// before manager startup or shutdown. All hooks remain registered; optional
// background callbacks skip nil services, while quota callbacks are bound
// directly. Stop ordering separates
// completion, moderation, cache, deferred, email, and audit work while their
// implementations determine drain outcomes.
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
	manager.Register(lifecycle.Hook{Name: "EmailQueueService", StartOrder: 930, StopOrder: shutdownStageEmail, Start: func(ctx context.Context) error {
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
	manager.Register(lifecycle.Hook{Name: "BillingCacheService", StartOrder: 950, StopOrder: shutdownStageBillingCache, Start: func(ctx context.Context) error {
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
	manager.Register(lifecycle.Hook{Name: "UsageRecordWorkerPool", StartOrder: 960, StopOrder: shutdownStageUsageRecords, Start: func(ctx context.Context) error {
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

	manager.Register(lifecycle.Hook{Name: "OllamaCloudUsageService", StartOrder: 940, StopOrder: shutdownStageOllamaUsage, Start: func(ctx context.Context) error {
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
	manager.Register(lifecycle.Hook{Name: "AuditLogService", StartOrder: 925, StopOrder: shutdownStageAudit, Start: func(ctx context.Context) error {
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

	manager.Register(lifecycle.Hook{Name: "DeferredService", StartOrder: 935, StopOrder: shutdownStageDeferredWrites, Start: func(ctx context.Context) error {
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

	manager.Register(lifecycle.Hook{Name: "ContentModerationService", StartOrder: 955, StopOrder: shutdownStageModeration, Start: func(ctx context.Context) error {
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
	manager.Register(lifecycle.Hook{Name: "GrokQuotaProbes", StopOrder: shutdownStageProbesAndRefresh, Stop: grokQuota.StopContext})
	// Administrative and platform quota calls can retain shared dependencies
	// after request work ends. Cancel and wait for those operations before later
	// consumers and shared storage enter their own stop phases.
	manager.Register(lifecycle.Hook{Name: "OpenAIQuotaService", StopOrder: shutdownStageOpenAIQuota, Stop: openaiQuota.StopContext})
	return &queuesRuntimeReady{}
}
