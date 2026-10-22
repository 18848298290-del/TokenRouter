package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_admin.go registers administrator Ops and system-maintenance HTTP
// routes on the supplied Gin group.
// Ops and system routes inherit the supplied Gin admin group middleware.
// Production app installs administrator authentication, panel-limiter, and
// audit middleware before calling these registrars; endpoints perform request
// and command checks. The historical OpenAI token-statistics path binds the
// same general token handler as the current path.

// RegisterOpsRoutes binds monitoring, runtime settings, diagnostic, and
// dashboard handlers below the supplied admin group. Callers install
// authentication, rate limits, and auditing on that group and provide a usable
// endpoint before registration. Register once for its prefix before serving
// requests.
func RegisterOpsRoutes(admin *gin.RouterGroup, endpoint *OpsHandler) {
	ops := admin.Group("/ops")
	{
		// Realtime operational signals.
		adminapi.Register(ops, ManagementOperations["ops.concurrency.list"], endpoint.GetConcurrencyStats)
		adminapi.Register(ops, ManagementOperations["ops.user_concurrency.list"], endpoint.GetUserConcurrencyStats)
		adminapi.Register(ops, ManagementOperations["ops.provider_availability.list"], endpoint.GetProviderAvailability)
		adminapi.Register(ops, ManagementOperations["ops.realtime_traffic.list"], endpoint.GetRealtimeTrafficSummary)

		// Alert rules and events.
		adminapi.Register(ops, ManagementOperations["ops.alert_rules.list"], endpoint.ListAlertRules)
		adminapi.Register(ops, ManagementOperations["ops.alert_rules.create"], endpoint.CreateAlertRule)
		adminapi.Register(ops, ManagementOperations["ops.alert_rules.update"], endpoint.UpdateAlertRule)
		adminapi.Register(ops, ManagementOperations["ops.alert_rules.delete"], endpoint.DeleteAlertRule)
		adminapi.Register(ops, ManagementOperations["ops.alert_events.list"], endpoint.ListAlertEvents)
		adminapi.Register(ops, ManagementOperations["ops.alert_events.get"], endpoint.GetAlertEvent)
		adminapi.Register(ops, ManagementOperations["ops.alert_events.status.update"], endpoint.UpdateAlertEventStatus)
		adminapi.Register(ops, ManagementOperations["ops.alert_silences.create"], endpoint.CreateAlertSilence)

		// Persisted email notification configuration.
		adminapi.Register(ops, ManagementOperations["ops.email_notification.config.list"], endpoint.GetEmailNotificationConfig)
		adminapi.Register(ops, ManagementOperations["ops.email_notification.config.update"], endpoint.UpdateEmailNotificationConfig)

		// Persisted runtime settings.
		runtime := ops.Group("/runtime")
		{
			adminapi.Register(runtime, ManagementOperations["ops.runtime.alert.list"], endpoint.GetAlertRuntimeSettings)
			adminapi.Register(runtime, ManagementOperations["ops.runtime.alert.update"], endpoint.UpdateAlertRuntimeSettings)
			adminapi.Register(runtime, ManagementOperations["ops.runtime.logging.list"], endpoint.GetRuntimeLogConfig)
			adminapi.Register(runtime, ManagementOperations["ops.runtime.logging.update"], endpoint.UpdateRuntimeLogConfig)
			adminapi.Register(runtime, ManagementOperations["ops.runtime.logging.reset.create"], endpoint.ResetRuntimeLogConfig)
		}

		// Persisted advanced settings.
		adminapi.Register(ops, ManagementOperations["ops.advanced_settings.list"], endpoint.GetAdvancedSettings)
		adminapi.Register(ops, ManagementOperations["ops.advanced_settings.update"], endpoint.UpdateAdvancedSettings)

		// Persisted metric threshold settings.
		settings := ops.Group("/settings")
		{
			adminapi.Register(settings, ManagementOperations["ops.settings.metric_thresholds.list"], endpoint.GetMetricThresholds)
			adminapi.Register(settings, ManagementOperations["ops.settings.metric_thresholds.update"], endpoint.UpdateMetricThresholds)
		}

		// Realtime QPS/TPS over WebSocket.
		ws := ops.Group("/ws")
		{
			adminapi.Register(ws, ManagementOperations["ops.ws.qps.list"], endpoint.QPSWSHandler)
		}

		// Legacy error log routes.
		adminapi.Register(ops, ManagementOperations["ops.errors.list"], endpoint.GetErrorLogs)
		adminapi.Register(ops, ManagementOperations["ops.errors.get"], endpoint.GetErrorLogByID)
		adminapi.Register(ops, ManagementOperations["ops.errors.resolve.update"], endpoint.UpdateErrorResolution)

		// Request-error list, shared detail, and resolution aliases.
		adminapi.Register(ops, ManagementOperations["ops.request_errors.list"], endpoint.ListRequestErrors)
		adminapi.Register(ops, ManagementOperations["ops.request_errors.get"], endpoint.GetRequestError)
		adminapi.Register(ops, ManagementOperations["ops.request_errors.upstream_errors.list"], endpoint.ListRequestErrorUpstreamErrors)
		adminapi.Register(ops, ManagementOperations["ops.request_errors.resolve.update"], endpoint.ResolveRequestError)

		// Bounded ingress rejection aggregates.
		adminapi.Register(ops, ManagementOperations["ops.ingress_rejections.list"], endpoint.ListIngressRejects)
		adminapi.Register(ops, ManagementOperations["ops.ingress_rejections.health.list"], endpoint.GetIngressRejectHealth)
		adminapi.Register(ops, ManagementOperations["ops.auth_cache_invalidation.health.list"], endpoint.GetAuthCacheInvalidationHealth)

		// Independent upstream failures.
		adminapi.Register(ops, ManagementOperations["ops.upstream_errors.list"], endpoint.ListUpstreamErrors)
		adminapi.Register(ops, ManagementOperations["ops.upstream_errors.get"], endpoint.GetUpstreamError)
		adminapi.Register(ops, ManagementOperations["ops.upstream_errors.resolve.update"], endpoint.ResolveUpstreamError)

		// Successful and failed request details.
		adminapi.Register(ops, ManagementOperations["ops.requests.list"], endpoint.ListRequestDetails)

		// Indexed system logs.
		adminapi.Register(ops, ManagementOperations["ops.system_logs.list"], endpoint.ListSystemLogs)
		adminapi.Register(ops, ManagementOperations["ops.system_logs.cleanup.create"], endpoint.CleanupSystemLogs)
		adminapi.Register(ops, ManagementOperations["ops.system_logs.health.list"], endpoint.GetSystemLogIngestionHealth)

		// Dashboard snapshots and historical statistics.
		adminapi.Register(ops, ManagementOperations["ops.dashboard.snapshot_v2.list"], endpoint.GetDashboardSnapshotV2)
		adminapi.Register(ops, ManagementOperations["ops.dashboard.overview.list"], endpoint.GetDashboardOverview)
		adminapi.Register(ops, ManagementOperations["ops.dashboard.throughput_trend.list"], endpoint.GetDashboardThroughputTrend)
		adminapi.Register(ops, ManagementOperations["ops.dashboard.latency_histogram.list"], endpoint.GetDashboardLatencyHistogram)
		adminapi.Register(ops, ManagementOperations["ops.dashboard.error_trend.list"], endpoint.GetDashboardErrorTrend)
		adminapi.Register(ops, ManagementOperations["ops.dashboard.error_distribution.list"], endpoint.GetDashboardErrorDistribution)
		adminapi.Register(ops, ManagementOperations["ops.dashboard.token_stats.list"], endpoint.GetDashboardTokenStats)
		// The historical OpenAI token-stats URL uses the same general token handler.
		// Its filters and response shape follow that endpoint, preserving clients
		// that still request the older route.
		adminapi.Register(ops, ManagementOperations["ops.dashboard.openai_token_stats.list"], endpoint.GetDashboardTokenStats)
	}
}

// RegisterSystemRoutes binds version reads and update, rollback, and restart
// handlers below the supplied admin group. Callers provide its protected
// middleware and usable SystemHandler before registration; command checks
// remain with that handler. Register once for the prefix before serving.
func RegisterSystemRoutes(admin *gin.RouterGroup, endpoint *SystemHandler) {
	system := admin.Group("/system")
	{
		adminapi.Register(system, ManagementOperations["system.version.list"], endpoint.GetVersion)
		adminapi.Register(system, ManagementOperations["system.check_updates.list"], endpoint.CheckUpdates)
		adminapi.Register(system, ManagementOperations["system.rollback_versions.list"], endpoint.GetRollbackVersions)
		adminapi.Register(system, ManagementOperations["system.update.create"], endpoint.PerformUpdate)
		adminapi.Register(system, ManagementOperations["system.rollback.create"], endpoint.Rollback)
		adminapi.Register(system, ManagementOperations["system.restart.create"], endpoint.RestartService)
	}
}
