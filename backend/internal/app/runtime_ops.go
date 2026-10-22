package app

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// runtime_ops.go registers operational producers, aggregation, and log-sink
// lifecycle callbacks. Operational producers and aggregation stop before their
// persistent log sink. The sink is installed after it starts and detached
// before its stop callback drains. Future sink lookups then find no sink; a log
// call that already captured the old sink can still reach WriteLogEvent, whose
// admission lock rejects writes after StopContext seals the queue. Producers
// with context-free Stop retain their own waiting behavior under the manager's
// outer budget.
//
// Ingress aggregation receives an additional post-stop check of pending batches
// and rows. A nonzero pending-batch count becomes a hook error instead of a
// successful drain event. Dashboard aggregation and runtime-settings refresh
// have separate stop levels, and the returned readiness marker indicates only
// registration; these callbacks run during later lifecycle startup and
// shutdown.

// opsRuntimeReady marks registration of operational producers, aggregation,
// and log-sink lifecycle hooks.
type opsRuntimeReady struct{}

// provideOpsRuntime registers operational monitoring, aggregation, reporting,
// and log-sink hooks before manager startup or shutdown. Hooks remain registered
// for nil services; callbacks skip those service calls. The returned marker
// reports registration, and successful hook events describe callback completion.
//
// The log sink is attached after Start and detached before StopContext. Detach
// changes subsequent sink lookups; StopContext closes queue admission against
// calls retaining the old sink. Ingress Stop is followed by a pending-batch
// health check; remaining batches return an error with counts. Context-free component Stop methods can continue after
// the outer manager deadline, while dashboard and sink callbacks return their
// own StopContext errors.
func provideOpsRuntime(
	opsMetricsCollector *ops.OpsMetricsCollector,
	opsAggregation *ops.OpsAggregationService,
	opsAlertEvaluator *ops.OpsAlertEvaluatorService,
	opsCleanup *ops.OpsCleanupService,
	opsScheduledReport *ops.OpsScheduledReportService,
	opsSystemLogSink *ops.OpsSystemLogSink,
	opsService *ops.OpsService,
	opsIngressReject *ops.OpsIngressRejectAggregator,
	manager *lifecycle.Manager,
	dashboardAggregation *usage.DashboardAggregationService,
) *opsRuntimeReady {
	manager.Register(lifecycle.Hook{Name: "OpsMetricsCollector", StartOrder: 980, StopOrder: shutdownStageProducers, Start: func(ctx context.Context) error {
		if opsMetricsCollector != nil {
			opsMetricsCollector.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if opsMetricsCollector != nil {
			opsMetricsCollector.Stop()
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "OpsAggregationService", StartOrder: 980, StopOrder: shutdownStageProducers, Start: func(ctx context.Context) error {
		if opsAggregation != nil {
			opsAggregation.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if opsAggregation != nil {
			opsAggregation.Stop()
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "OpsAlertEvaluatorService", StartOrder: 980, StopOrder: shutdownStageProducers, Start: func(ctx context.Context) error {
		if opsAlertEvaluator != nil {
			opsAlertEvaluator.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if opsAlertEvaluator != nil {
			opsAlertEvaluator.Stop()
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "OpsCleanupService", StartOrder: 980, StopOrder: shutdownStageProducers, Start: func(ctx context.Context) error {
		if opsCleanup != nil {
			opsCleanup.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if opsCleanup != nil {
			opsCleanup.Stop()
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "OpsScheduledReportService", StartOrder: 980, StopOrder: shutdownStageProducers, Start: func(ctx context.Context) error {
		if opsScheduledReport != nil {
			opsScheduledReport.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if opsScheduledReport != nil {
			opsScheduledReport.Stop()
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "OpsSystemLogSink", StartOrder: 210, StopOrder: shutdownStageRequestAndSystemLogs, Start: func(ctx context.Context) error {
		if opsSystemLogSink != nil {
			opsSystemLogSink.Start()
			// Publish the sink after its worker starts so lifecycle and request logs
			// are submitted to the running queue rather than an inactive sink.
			logger.SetSink(opsSystemLogSink)
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if opsSystemLogSink != nil {
			// Remove the sink from future logger lookups before draining. Calls that
			// already captured it meet StopContext's queue-admission lock before the writer stops.
			logger.SetSink(nil)
			return opsSystemLogSink.StopContext(ctx)
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "OpsService", StartOrder: 975, StopOrder: shutdownStageProbesAndRefresh, Start: func(ctx context.Context) error {
		if opsService != nil {
			// The refresh loop owns its cancellable lifetime after startup. Its
			// paired stop method cancels and waits instead of inheriting the start context.
			opsService.StartRuntimeSettingsRefresh(context.Background())
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if opsService != nil {
			opsService.StopRuntimeSettingsRefresh()
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "OpsIngressRejectAggregator", StartOrder: 980, StopOrder: shutdownStageProducers, Start: func(ctx context.Context) error {
		if opsIngressReject != nil {
			opsIngressReject.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if opsIngressReject != nil {
			opsIngressReject.Stop()
			// A completed Stop call can still leave pending aggregation batches.
			// Convert that remaining health state into an error instead of a successful drain event.
			if health := opsIngressReject.Health(); health.PendingBatches != 0 {
				return fmt.Errorf("ingress reject drain incomplete: %d batches, %d rows", health.PendingBatches, health.PendingRows)
			}
		}
		return nil
	}})

	manager.Register(lifecycle.Hook{Name: "DashboardAggregationService", StartOrder: 970, StopOrder: shutdownStageAggregatesSessionsAndSearch, Start: func(ctx context.Context) error {
		if dashboardAggregation != nil {
			dashboardAggregation.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if dashboardAggregation != nil {
			return dashboardAggregation.StopContext(ctx)
		}
		return nil
	}})
	return &opsRuntimeReady{}
}
