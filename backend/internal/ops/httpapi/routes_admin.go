package httpapi

import (
	"github.com/gin-gonic/gin"
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
		ops.GET("/concurrency", endpoint.GetConcurrencyStats)
		ops.GET("/user-concurrency", endpoint.GetUserConcurrencyStats)
		ops.GET("/provider-availability", endpoint.GetProviderAvailability)
		ops.GET("/realtime-traffic", endpoint.GetRealtimeTrafficSummary)

		// Alert rules and events.
		ops.GET("/alert-rules", endpoint.ListAlertRules)
		ops.POST("/alert-rules", endpoint.CreateAlertRule)
		ops.PUT("/alert-rules/:id", endpoint.UpdateAlertRule)
		ops.DELETE("/alert-rules/:id", endpoint.DeleteAlertRule)
		ops.GET("/alert-events", endpoint.ListAlertEvents)
		ops.GET("/alert-events/:id", endpoint.GetAlertEvent)
		ops.PUT("/alert-events/:id/status", endpoint.UpdateAlertEventStatus)
		ops.POST("/alert-silences", endpoint.CreateAlertSilence)

		// Persisted email notification configuration.
		ops.GET("/email-notification/config", endpoint.GetEmailNotificationConfig)
		ops.PUT("/email-notification/config", endpoint.UpdateEmailNotificationConfig)

		// Persisted runtime settings.
		runtime := ops.Group("/runtime")
		{
			runtime.GET("/alert", endpoint.GetAlertRuntimeSettings)
			runtime.PUT("/alert", endpoint.UpdateAlertRuntimeSettings)
			runtime.GET("/logging", endpoint.GetRuntimeLogConfig)
			runtime.PUT("/logging", endpoint.UpdateRuntimeLogConfig)
			runtime.POST("/logging/reset", endpoint.ResetRuntimeLogConfig)
		}

		// Persisted advanced settings.
		ops.GET("/advanced-settings", endpoint.GetAdvancedSettings)
		ops.PUT("/advanced-settings", endpoint.UpdateAdvancedSettings)

		// Persisted metric threshold settings.
		settings := ops.Group("/settings")
		{
			settings.GET("/metric-thresholds", endpoint.GetMetricThresholds)
			settings.PUT("/metric-thresholds", endpoint.UpdateMetricThresholds)
		}

		// Realtime QPS/TPS over WebSocket.
		ws := ops.Group("/ws")
		{
			ws.GET("/qps", endpoint.QPSWSHandler)
		}

		// Legacy error log routes.
		ops.GET("/errors", endpoint.GetErrorLogs)
		ops.GET("/errors/:id", endpoint.GetErrorLogByID)
		ops.PUT("/errors/:id/resolve", endpoint.UpdateErrorResolution)

		// Request-error list, shared detail, and resolution aliases.
		ops.GET("/request-errors", endpoint.ListRequestErrors)
		ops.GET("/request-errors/:id", endpoint.GetRequestError)
		ops.GET("/request-errors/:id/upstream-errors", endpoint.ListRequestErrorUpstreamErrors)
		ops.PUT("/request-errors/:id/resolve", endpoint.ResolveRequestError)

		// Bounded ingress rejection aggregates.
		ops.GET("/ingress-rejections", endpoint.ListIngressRejects)
		ops.GET("/ingress-rejections/health", endpoint.GetIngressRejectHealth)
		ops.GET("/auth-cache-invalidation/health", endpoint.GetAuthCacheInvalidationHealth)

		// Independent upstream failures.
		ops.GET("/upstream-errors", endpoint.ListUpstreamErrors)
		ops.GET("/upstream-errors/:id", endpoint.GetUpstreamError)
		ops.PUT("/upstream-errors/:id/resolve", endpoint.ResolveUpstreamError)

		// Successful and failed request details.
		ops.GET("/requests", endpoint.ListRequestDetails)

		// Indexed system logs.
		ops.GET("/system-logs", endpoint.ListSystemLogs)
		ops.POST("/system-logs/cleanup", endpoint.CleanupSystemLogs)
		ops.GET("/system-logs/health", endpoint.GetSystemLogIngestionHealth)

		// Dashboard snapshots and historical statistics.
		ops.GET("/dashboard/snapshot-v2", endpoint.GetDashboardSnapshotV2)
		ops.GET("/dashboard/overview", endpoint.GetDashboardOverview)
		ops.GET("/dashboard/throughput-trend", endpoint.GetDashboardThroughputTrend)
		ops.GET("/dashboard/latency-histogram", endpoint.GetDashboardLatencyHistogram)
		ops.GET("/dashboard/error-trend", endpoint.GetDashboardErrorTrend)
		ops.GET("/dashboard/error-distribution", endpoint.GetDashboardErrorDistribution)
		ops.GET("/dashboard/token-stats", endpoint.GetDashboardTokenStats)
		// The historical OpenAI token-stats URL uses the same general token handler.
		// Its filters and response shape follow that endpoint, preserving clients
		// that still request the older route.
		ops.GET("/dashboard/openai-token-stats", endpoint.GetDashboardTokenStats)
	}
}

// RegisterSystemRoutes binds version reads and update, rollback, and restart
// handlers below the supplied admin group. Callers provide its protected
// middleware and usable SystemHandler before registration; command checks
// remain with that handler. Register once for the prefix before serving.
func RegisterSystemRoutes(admin *gin.RouterGroup, endpoint *SystemHandler) {
	system := admin.Group("/system")
	{
		system.GET("/version", endpoint.GetVersion)
		system.GET("/check-updates", endpoint.CheckUpdates)
		system.GET("/rollback-versions", endpoint.GetRollbackVersions)
		system.POST("/update", endpoint.PerformUpdate)
		system.POST("/rollback", endpoint.Rollback)
		system.POST("/restart", endpoint.RestartService)
	}
}
