package httpapi

import "github.com/gin-gonic/gin"

// RegisterUserRoutes adds announcement routes to a group whose authentication,
// rate limiting, and auditing middleware is installed by app.
func RegisterUserRoutes(authenticated *gin.RouterGroup, endpoint *AnnouncementHandler) {
	announcements := authenticated.Group("/announcements")
	{
		announcements.GET("", endpoint.List)
		announcements.POST("/:id/read", endpoint.MarkRead)
	}
}
