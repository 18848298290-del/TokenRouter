package httpapi

import (
	"github.com/gin-gonic/gin"
)

// RegisterAuditLogRoutes registers admin routes after app installs authentication, rate limiting, and audit middleware.
func RegisterAuditLogRoutes(admin *gin.RouterGroup, endpoint *AuditLogHandler) {
	auditLogs := admin.Group("/audit-logs")
	{
		auditLogs.GET("", endpoint.List)
		auditLogs.GET("/:id", endpoint.Get)
		// The handler requires a fresh TOTP code even within an active step-up window.
		auditLogs.POST("/clear", endpoint.Clear)
	}
}
