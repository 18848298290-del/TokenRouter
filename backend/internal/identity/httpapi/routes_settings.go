package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_settings.go registers administrator endpoints for reading,
// regenerating, and deleting the management API key.
// The stored management credential has separate status, regeneration, and
// deletion endpoints under administrator settings. Route registration binds
// those operations to one endpoint interface and relies on the settings group
// to apply its authentication and audit middleware.

// IdentitySettingsEndpoints defines administrator HTTP operations for the
// stored management credential.
type IdentitySettingsEndpoints interface {
	// GetAdminAPIKey writes credential existence and a masked display value to
	// the panel response. The caller installs administrator authentication before
	// allowing a request to reach it.
	GetAdminAPIKey(*gin.Context)
	// RegenerateAdminAPIKey saves a replacement management credential and returns
	// its full value once generated. The caller restricts this route to
	// authenticated administrator requests before handler dispatch.
	RegenerateAdminAPIKey(*gin.Context)
	// DeleteAdminAPIKey removes the configured management credential and reports
	// completion through the panel envelope. The caller restricts this route to
	// authenticated administrator requests before handler dispatch.
	DeleteAdminAPIKey(*gin.Context)
}

// RegisterIdentitySettingsRoutes adds management-key status, regeneration, and
// deletion endpoints to administrator settings. The caller installs
// authentication and auditing on the supplied group before exposing the
// credential operations.
func RegisterIdentitySettingsRoutes(adminSettings *gin.RouterGroup, endpoint IdentitySettingsEndpoints) {
	adminapi.Register(adminSettings, ManagementOperations["settings.admin_api_key.list"], endpoint.GetAdminAPIKey)
	adminapi.Register(adminSettings, ManagementOperations["settings.admin_api_key.regenerate.create"], endpoint.RegenerateAdminAPIKey)
	adminapi.Register(adminSettings, ManagementOperations["settings.admin_api_key.delete"], endpoint.DeleteAdminAPIKey)
}
