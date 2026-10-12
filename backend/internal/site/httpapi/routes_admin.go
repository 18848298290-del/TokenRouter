package httpapi

import (
	"github.com/gin-gonic/gin"
)

// RegisterAnnouncementRoutes adds announcement management routes to a group whose
// authentication, rate limiting, and auditing middleware is installed by app.
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
