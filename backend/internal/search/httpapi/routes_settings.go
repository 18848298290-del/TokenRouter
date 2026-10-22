package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

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
	adminapi.Register(adminSettings, ManagementOperations["settings.web_search_emulation.list"], endpoint.GetWebSearchEmulationConfig)
	adminapi.Register(adminSettings, ManagementOperations["settings.web_search_emulation.update"], endpoint.UpdateWebSearchEmulationConfig)
	adminapi.Register(adminSettings, ManagementOperations["settings.web_search_emulation.test.create"], endpoint.TestWebSearchEmulation)
	adminapi.Register(adminSettings, ManagementOperations["settings.web_search_emulation.reset_usage.create"], endpoint.ResetWebSearchUsage)
}
