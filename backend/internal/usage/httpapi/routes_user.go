package httpapi

import "github.com/gin-gonic/gin"

// Authenticated users query usage records, rankings, errors and dashboard
// reports under a shared usage group. Heavy-query middleware also covers the
// separate daily key-usage route, while the application's parent group
// provides authentication, rate limiting and audit handling.

// RegisterUserRoutes attaches the user usage endpoints and applies
// heavy-query middleware to their reads. The authenticated parent supplies
// shared authentication, rate limiting and audit handling; endpoint and heavy
// must be configured before registration.
func RegisterUserRoutes(authenticated *gin.RouterGroup, endpoint *UsageHandler, heavy gin.HandlerFunc) {
	usage := authenticated.Group("/usage")
	usage.Use(heavy)
	{
		usage.GET("", endpoint.List)
		usage.GET("/ranking", endpoint.Ranking)
		usage.GET("/errors", endpoint.ListErrors)
		usage.GET("/errors/:id", endpoint.GetErrorDetail)
		usage.GET("/stats", endpoint.Stats)
		// User dashboard routes.
		usage.GET("/dashboard/stats", endpoint.DashboardStats)
		usage.GET("/dashboard/trend", endpoint.DashboardTrend)
		usage.GET("/dashboard/models", endpoint.DashboardModels)
		usage.GET("/dashboard/snapshot-v2", endpoint.DashboardSnapshotV2)
		usage.POST("/dashboard/api-keys-usage", endpoint.DashboardAPIKeysUsage)
		usage.GET("/:id", endpoint.GetByID)
	}
	authenticated.GET("/user/api-keys/:id/usage/daily", heavy, endpoint.GetMyAPIKeyDailyUsage)
}
