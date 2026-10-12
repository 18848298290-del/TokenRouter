package httpapi

import "github.com/gin-gonic/gin"

// CreativeSettingsEndpoints supplies HTTP operations for creative settings.
type CreativeSettingsEndpoints interface {
	ListCreativeModelCandidates(*gin.Context)
	GetCreativeWorkerStatus(*gin.Context)
}

// RegisterCreativeSettingsRoutes registers endpoints in an authenticated, audited settings group.
func RegisterCreativeSettingsRoutes(adminSettings *gin.RouterGroup, endpoint CreativeSettingsEndpoints) {
	adminSettings.GET("/creative-model-candidates", endpoint.ListCreativeModelCandidates)
	adminSettings.GET("/creative-worker-status", endpoint.GetCreativeWorkerStatus)
}
