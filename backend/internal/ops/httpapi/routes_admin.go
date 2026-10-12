package httpapi

import (
	"github.com/gin-gonic/gin"
)

// RegisterOpsRoutes registers admin routes after app installs authentication, rate limits, and auditing.
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

		// Client-visible request failures.
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

		// Dashboard queries use the configured data source.
		ops.GET("/dashboard/snapshot-v2", endpoint.GetDashboardSnapshotV2)
		ops.GET("/dashboard/overview", endpoint.GetDashboardOverview)
		ops.GET("/dashboard/throughput-trend", endpoint.GetDashboardThroughputTrend)
		ops.GET("/dashboard/latency-histogram", endpoint.GetDashboardLatencyHistogram)
		ops.GET("/dashboard/error-trend", endpoint.GetDashboardErrorTrend)
		ops.GET("/dashboard/error-distribution", endpoint.GetDashboardErrorDistribution)
		ops.GET("/dashboard/token-stats", endpoint.GetDashboardTokenStats)
		// The legacy route is an alias with the same response shape.
		ops.GET("/dashboard/openai-token-stats", endpoint.GetDashboardTokenStats)
	}
}

// RegisterSystemRoutes registers admin routes after app installs authentication, rate limits, and auditing.
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
