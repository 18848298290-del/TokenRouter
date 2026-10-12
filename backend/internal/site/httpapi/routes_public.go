package httpapi

import "github.com/gin-gonic/gin"

// Public browser initialization reads display settings and legal copy through
// the settings route family. app supplies the shared public group with rate
// limiting already attached, and both reads inherit that policy when registered.

// RegisterPublicSettingsRoutes installs browser settings and individual legal-document reads under group.
// app supplies the public group with its rate limiting in place. These handler
// bindings inherit that middleware and the caller's chosen URL prefix.
func RegisterPublicSettingsRoutes(group *gin.RouterGroup, endpoint *PublicHandler) {
	group.GET("/public", endpoint.GetPublicSettings)
	group.GET("/legal/:id", endpoint.GetLegalDocument)
}
