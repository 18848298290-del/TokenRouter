package httpapi

import (
	"github.com/gin-gonic/gin"
)

// RegisterContentModerationRoutes installs risk-control routes on a group already configured with
// authentication, rate limiting, and audit middleware.
func RegisterContentModerationRoutes(admin *gin.RouterGroup, endpoint *ContentModerationHandler) {
	risk := admin.Group("/risk-control")
	{
		risk.GET("/config", endpoint.GetConfig)
		risk.PUT("/config", endpoint.UpdateConfig)
		risk.POST("/api-keys/test", endpoint.TestAPIKeys)
		risk.GET("/status", endpoint.GetStatus)
		risk.GET("/logs", endpoint.ListLogs)
		risk.GET("/logs/:id", endpoint.GetLog)
		risk.GET("/cyber-warnings", endpoint.ListCyberWarnings)
		risk.GET("/cyber-warnings/:id", endpoint.GetCyberWarning)
		risk.GET("/media/:id/content", endpoint.GetMediaContent)
		risk.GET("/cyber-summary", endpoint.GetCyberSummary)
		risk.POST("/users/:user_id/unban", endpoint.UnbanUser)
		risk.DELETE("/hashes", endpoint.DeleteFlaggedHash)
		risk.DELETE("/hashes/all", endpoint.ClearFlaggedHashes)
	}
}
