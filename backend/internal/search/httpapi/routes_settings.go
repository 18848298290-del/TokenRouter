package httpapi

import "github.com/gin-gonic/gin"

// routes_settings.go registers search configuration, test, and quota-reset
// endpoints on the administrator settings group.
// Search administration shares the settings route group for configuration
// reads, saves, test execution, and quota reset. That group supplies the
// application's authentication and other middleware, so these handlers inherit
// access policy from their mounted administrator settings routes.

// RegisterSettingsRoutes mounts search configuration, test, and quota reset
// endpoints on adminSettings. Callers provide the authenticated administrator
// group and a handler bound to the shared search runtime before serving
// requests.
func RegisterSettingsRoutes(adminSettings *gin.RouterGroup, endpoint *Handler) {
	adminSettings.GET("/web-search-emulation", endpoint.GetWebSearchEmulationConfig)
	adminSettings.PUT("/web-search-emulation", endpoint.UpdateWebSearchEmulationConfig)
	adminSettings.POST("/web-search-emulation/test", endpoint.TestWebSearchEmulation)
	adminSettings.POST("/web-search-emulation/reset-usage", endpoint.ResetWebSearchUsage)
}
