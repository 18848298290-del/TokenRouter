package httpapi

import (
	"github.com/gin-gonic/gin"
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
		announcements.GET("", endpoint.List)
		announcements.POST("", endpoint.Create)
		announcements.GET("/:id", endpoint.GetByID)
		announcements.PUT("/:id", endpoint.Update)
		announcements.DELETE("/:id", endpoint.Delete)
		announcements.GET("/:id/read-status", endpoint.ListReadStatus)
	}
}
