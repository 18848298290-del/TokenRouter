package httpapi

import (
	"github.com/gin-gonic/gin"
)

// Risk-control management shares the protected administrator route group
// assembled by app. Full input and image retrieval, key tests, unban commands,
// and hash mutations inherit that group middleware when these endpoints are
// mounted beneath /risk-control.

// RegisterContentModerationRoutes mounts configuration, review, key-test,
// unban, and hash-management handlers below /risk-control. Supply a group
// already protected by administrator authentication, panel limiting, and audit
// middleware; app passes its /api/v1/admin group here.
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
