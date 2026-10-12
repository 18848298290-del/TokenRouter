package httpapi

import (
	"github.com/gin-gonic/gin"
)

// RegisterAdminAPIKeyRoutes registers admin endpoints after app installs authentication, rate limits, and auditing.
func RegisterAdminAPIKeyRoutes[G any](admin *gin.RouterGroup, endpoint *AdminAPIKeyHandler[G]) {
	apiKeys := admin.Group("/api-keys")
	{
		apiKeys.PUT("/:id", endpoint.UpdateGroup)
	}
}
