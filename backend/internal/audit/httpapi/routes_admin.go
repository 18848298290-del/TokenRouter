package httpapi

import (
	"github.com/gin-gonic/gin"
)

// Audit history is exposed through the administrator router's existing protection.
// Listing and detail routes use the shared group middleware, while clearing also
// verifies fresh TOTP inside the handler so its authorization is checked on each
// destructive history request.

// RegisterAuditLogRoutes installs audit listing, detail, and clearing handlers
// under the administrator group. App supplies authentication, rate limiting, and
// audit middleware before registration; the clear handler checks a fresh TOTP code.
func RegisterAuditLogRoutes(admin *gin.RouterGroup, endpoint *AuditLogHandler) {
	auditLogs := admin.Group("/audit-logs")
	{
		auditLogs.GET("", endpoint.List)
		auditLogs.GET("/:id", endpoint.Get)
		// The handler requires a fresh TOTP code even within an active step-up window.
		auditLogs.POST("/clear", endpoint.Clear)
	}
}
