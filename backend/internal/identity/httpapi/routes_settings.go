package httpapi

import "github.com/gin-gonic/gin"

// IdentitySettingsEndpoints lists administrator credential-setting handlers.
type IdentitySettingsEndpoints interface {
	GetAdminAPIKey(*gin.Context)
	RegenerateAdminAPIKey(*gin.Context)
	DeleteAdminAPIKey(*gin.Context)
}

// RegisterIdentitySettingsRoutes requires a group with authentication and auditing installed.
func RegisterIdentitySettingsRoutes(adminSettings *gin.RouterGroup, endpoint IdentitySettingsEndpoints) {
	adminSettings.GET("/admin-api-key", endpoint.GetAdminAPIKey)
	adminSettings.POST("/admin-api-key/regenerate", endpoint.RegenerateAdminAPIKey)
	adminSettings.DELETE("/admin-api-key", endpoint.DeleteAdminAPIKey)
}
