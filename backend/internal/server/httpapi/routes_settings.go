package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_settings.go registers panel rate-limit read and update endpoints on
// the administrator settings group.
// Panel rate limits have a dedicated administrator endpoint for reading and
// replacing their settings. App supplies the authenticated settings group and
// handlers, while registration binds the GET and PUT paths to that group's
// existing authentication and audit middleware.

// ServerSettingsEndpoints provides administrator handlers for reading and replacing panel limits.
type ServerSettingsEndpoints interface {
	// GetPanelRateLimitSettings writes the current panel limits as a success
	// envelope, or an error response when storage reading fails. The route's
	// administrator middleware supplies authorization before this handler runs.
	GetPanelRateLimitSettings(*gin.Context)
	// UpdatePanelRateLimitSettings binds and saves the supplied panel limits,
	// then reads the values returned in its response. A later read failure can
	// produce an error after persistence has succeeded.
	UpdatePanelRateLimitSettings(*gin.Context)
}

// RegisterPanelSettingsRoutes binds GET and PUT operations for panel rate limits.
// Callers provide nonnil endpoints and an administrator settings group whose
// middleware already performs authentication and audit before these handlers execute.
func RegisterPanelSettingsRoutes(adminSettings *gin.RouterGroup, endpoint ServerSettingsEndpoints) {
	adminapi.Register(adminSettings, ManagementOperations["settings.panel_rate_limit.list"], endpoint.GetPanelRateLimitSettings)
	adminapi.Register(adminSettings, ManagementOperations["settings.panel_rate_limit.update"], endpoint.UpdatePanelRateLimitSettings)
}
