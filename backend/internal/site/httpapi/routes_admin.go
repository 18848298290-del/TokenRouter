package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_admin.go registers administrator announcement management and
// read-status endpoints.
// Administrator announcement management uses a dedicated route group for writes,
// publication queries, and reader inspection. The caller supplies its middleware
// before registration, so every child route inherits the same administrator
// authentication, rate limits, and auditing.

// RegisterAnnouncementRoutes installs announcement management and reader-inspection endpoints under admin.
// app supplies a group with administrator authentication, rate limiting, and
// auditing already attached; this registrar binds handlers beneath that configured group.
func RegisterAnnouncementRoutes(admin *gin.RouterGroup, endpoint *AdminAnnouncementHandler) {
	announcements := admin.Group("/announcements")
	{
		adminapi.Register(announcements, ManagementOperations["announcements.list"], endpoint.List)
		adminapi.Register(announcements, ManagementOperations["announcements.create"], endpoint.Create)
		adminapi.Register(announcements, ManagementOperations["announcements.get"], endpoint.GetByID)
		adminapi.Register(announcements, ManagementOperations["announcements.update"], endpoint.Update)
		adminapi.Register(announcements, ManagementOperations["announcements.delete"], endpoint.Delete)
		adminapi.Register(announcements, ManagementOperations["announcements.read_status.list"], endpoint.ListReadStatus)
	}
}
