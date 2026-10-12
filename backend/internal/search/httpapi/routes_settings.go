package httpapi

import "github.com/gin-gonic/gin"

// RegisterSettingsRoutes installs search administration routes under the settings group.
func RegisterSettingsRoutes(adminSettings *gin.RouterGroup, endpoint *Handler) {
	adminSettings.GET("/web-search-emulation", endpoint.GetWebSearchEmulationConfig)
	adminSettings.PUT("/web-search-emulation", endpoint.UpdateWebSearchEmulationConfig)
	adminSettings.POST("/web-search-emulation/test", endpoint.TestWebSearchEmulation)
	adminSettings.POST("/web-search-emulation/reset-usage", endpoint.ResetWebSearchUsage)
}
