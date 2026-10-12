package httpapi

import "github.com/gin-gonic/gin"

// RegisterPublicSettingsRoutes adds settings routes to the public, rate-limited group supplied by app.
func RegisterPublicSettingsRoutes(group *gin.RouterGroup, endpoint *PublicHandler) {
	group.GET("/public", endpoint.GetPublicSettings)
	group.GET("/legal/:id", endpoint.GetLegalDocument)
}
