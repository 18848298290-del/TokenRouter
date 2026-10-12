package httpapi

import "github.com/gin-gonic/gin"

// Logged-in users query announcements and acknowledge visible records through
// the same authenticated route family. app prepares authentication, rate limiting,
// and auditing on the supplied parent group before these handlers are bound,
// keeping read acknowledgements subject to that shared request policy.

// RegisterUserRoutes installs announcement listing and read acknowledgement under authenticated.
// app supplies user authentication, rate limiting, and auditing on the group.
// The child announcement routes inherit those middleware and the configured URL prefix.
func RegisterUserRoutes(authenticated *gin.RouterGroup, endpoint *AnnouncementHandler) {
	announcements := authenticated.Group("/announcements")
	{
		announcements.GET("", endpoint.List)
		announcements.POST("/:id/read", endpoint.MarkRead)
	}
}
