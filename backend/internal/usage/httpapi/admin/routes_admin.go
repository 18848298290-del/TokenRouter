package admin

import (
	"github.com/gin-gonic/gin"
)

// routes_admin.go registers administrator dashboard, usage-query, and
// cleanup-task HTTP routes.
// Administrator dashboard and usage routes expose statistics, searches and
// persisted cleanup tasks through separate path groups. The application
// supplies their authentication, rate limiting and audit middleware on the
// parent group before these handlers are registered.

// RegisterDashboardRoutes attaches dashboard reports, chart snapshots and
// batch usage reads beneath admin. The caller supplies a nonnil handler and
// installs authentication, rate limiting and audit middleware on the parent
// group first.
func RegisterDashboardRoutes(admin *gin.RouterGroup, endpoint *DashboardHandler) {
	dashboard := admin.Group("/dashboard")
	{
		dashboard.GET("/snapshot-v2", endpoint.GetSnapshotV2)
		dashboard.GET("/stats", endpoint.GetStats)
		dashboard.GET("/realtime", endpoint.GetRealtimeMetrics)
		dashboard.GET("/trend", endpoint.GetUsageTrend)
		dashboard.GET("/models", endpoint.GetModelStats)
		dashboard.GET("/groups", endpoint.GetGroupStats)
		dashboard.GET("/api-keys-trend", endpoint.GetAPIKeyUsageTrend)
		dashboard.GET("/users-trend", endpoint.GetUserUsageTrend)
		dashboard.GET("/users-ranking", endpoint.GetUserSpendingRanking)
		dashboard.POST("/users-usage", endpoint.GetBatchUsersUsage)
		dashboard.POST("/api-keys-usage", endpoint.GetBatchAPIKeysUsage)
		dashboard.GET("/user-breakdown", endpoint.GetUserBreakdown)
	}
}

// RegisterUsageRoutes attaches usage records, statistics, searches and
// cleanup-task commands beneath admin. The caller supplies a nonnil handler
// and installs authentication, rate limiting and audit middleware on the
// parent group first.
func RegisterUsageRoutes(admin *gin.RouterGroup, endpoint *UsageHandler) {
	usage := admin.Group("/usage")
	{
		usage.GET("", endpoint.List)
		usage.GET("/stats", endpoint.Stats)
		usage.GET("/search-users", endpoint.SearchUsers)
		usage.GET("/search-api-keys", endpoint.SearchAPIKeys)
		usage.GET("/cleanup-tasks", endpoint.ListCleanupTasks)
		usage.POST("/cleanup-tasks", endpoint.CreateCleanupTask)
		usage.POST("/cleanup-tasks/:id/cancel", endpoint.CancelCleanupTask)
	}
}
