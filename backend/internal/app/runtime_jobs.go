package app

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// runtime_jobs.go registers task cleanup, creative and batch workers, and CN
// monitoring lifecycle hooks. These services start at lifecycle order 980.
// Their common stop level is 20, so the manager invokes those stops
// concurrently after earlier request barriers; each service owns its draining
// behavior.

// jobsRuntimeReady marks registration of task-worker and CN monitoring
// lifecycle hooks.
type jobsRuntimeReady struct{}

// provideJobsRuntime registers task cleanup, batch and creative workers, and
// CN monitoring before manager startup or shutdown. Hooks remain registered
// when a service is nil; their callbacks skip that service call. StopContext
// errors are returned from their hooks, and services at this
// shared stop level may drain concurrently.
func provideJobsRuntime(
	batchImageCleanup *batchCleanupRuntime,
	batchImageWorker *batchimage.Runtime,
	creativeWorker *creative.CreativeWorkerRuntime,
	cnUsageMonitor *provider.CNUsageMonitor,
	manager *lifecycle.Manager,
) *jobsRuntimeReady {
	manager.Register(lifecycle.Hook{Name: "BatchImageCleanupService", StartOrder: 980, StopOrder: shutdownStageProducers, Start: func(ctx context.Context) error {
		if batchImageCleanup != nil {
			batchImageCleanup.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if batchImageCleanup != nil {
			return batchImageCleanup.StopContext(ctx)
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "BatchImageWorkerRuntime", StartOrder: 980, StopOrder: shutdownStageProducers, Start: func(ctx context.Context) error {
		if batchImageWorker != nil {
			batchImageWorker.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if batchImageWorker != nil {
			return batchImageWorker.StopContext(ctx)
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "CreativeWorkerRuntime", StartOrder: 980, StopOrder: shutdownStageProducers, Start: func(ctx context.Context) error {
		if creativeWorker != nil {
			creativeWorker.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if creativeWorker != nil {
			return creativeWorker.StopContext(ctx)
		}
		return nil
	}})

	manager.Register(lifecycle.Hook{Name: "CNProviderBalanceCheckService", StartOrder: 980, StopOrder: shutdownStageProducers, Start: func(ctx context.Context) error {
		if cnUsageMonitor != nil {
			return cnUsageMonitor.StartContext(ctx)
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if cnUsageMonitor != nil {
			return cnUsageMonitor.StopContext(ctx)
		}
		return nil
	}})
	return &jobsRuntimeReady{}
}
