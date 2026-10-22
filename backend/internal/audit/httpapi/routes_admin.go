package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_admin.go registers audit-history listing, detail, and clearing
// endpoints under the administrator router.
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
		adminapi.Register(auditLogs, ManagementOperations["audit_logs.list"], endpoint.List)
		adminapi.Register(auditLogs, ManagementOperations["audit_logs.get"], endpoint.Get)
		// The handler requires a fresh TOTP code even within an active step-up window.
		adminapi.Register(auditLogs, ManagementOperations["audit_logs.clear.create"], endpoint.Clear)
	}
}
