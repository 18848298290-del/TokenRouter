package httpapi

import "github.com/gin-gonic/gin"

// ServerSettingsEndpoints exposes HTTP operations implemented by the settings owner.
type ServerSettingsEndpoints interface {
	GetPanelRateLimitSettings(*gin.Context)
	UpdatePanelRateLimitSettings(*gin.Context)
}

// RegisterPanelSettingsRoutes requires an authenticated, audited admin settings group.
func RegisterPanelSettingsRoutes(adminSettings *gin.RouterGroup, endpoint ServerSettingsEndpoints) {
	adminSettings.GET("/panel-rate-limit", endpoint.GetPanelRateLimitSettings)
	adminSettings.PUT("/panel-rate-limit", endpoint.UpdatePanelRateLimitSettings)
}
