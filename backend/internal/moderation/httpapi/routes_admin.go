package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_admin.go registers administrator endpoints for moderation
// configuration and review.
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
		adminapi.Register(risk, ManagementOperations["risk_control.config.list"], endpoint.GetConfig)
		adminapi.Register(risk, ManagementOperations["risk_control.config.update"], endpoint.UpdateConfig)
		adminapi.Register(risk, ManagementOperations["risk_control.api_keys.test.create"], endpoint.TestAPIKeys)
		adminapi.Register(risk, ManagementOperations["risk_control.status.list"], endpoint.GetStatus)
		adminapi.Register(risk, ManagementOperations["risk_control.logs.list"], endpoint.ListLogs)
		adminapi.Register(risk, ManagementOperations["risk_control.logs.get"], endpoint.GetLog)
		adminapi.Register(risk, ManagementOperations["risk_control.cyber_warnings.list"], endpoint.ListCyberWarnings)
		adminapi.Register(risk, ManagementOperations["risk_control.cyber_warnings.get"], endpoint.GetCyberWarning)
		adminapi.Register(risk, ManagementOperations["risk_control.media.content.list"], endpoint.GetMediaContent)
		adminapi.Register(risk, ManagementOperations["risk_control.cyber_summary.list"], endpoint.GetCyberSummary)
		adminapi.Register(risk, ManagementOperations["risk_control.users.unban.create"], endpoint.UnbanUser)
		adminapi.Register(risk, ManagementOperations["risk_control.hashes.delete"], endpoint.DeleteFlaggedHash)
		adminapi.Register(risk, ManagementOperations["risk_control.hashes.all.delete"], endpoint.ClearFlaggedHashes)
	}
}
