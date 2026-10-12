package httpapi

import "github.com/gin-gonic/gin"

// SettingsSettingsEndpoints provides the combined settings HTTP operations.
type SettingsSettingsEndpoints interface {
	GetSettings(*gin.Context)
	UpdateSettings(*gin.Context)
}

// PreAggregationEndpoints provides the dedicated pre-aggregation operations.
type PreAggregationEndpoints interface {
	GetPreAggregationSettings(*gin.Context)
	UpdatePreAggregationSettings(*gin.Context)
	BackfillPreAggregation(*gin.Context)
}

// RegisterSettingsSettingsRoutes registers routes on an authenticated, audited admin settings group.
func RegisterSettingsSettingsRoutes(adminSettings *gin.RouterGroup, endpoint SettingsSettingsEndpoints, pre PreAggregationEndpoints) {
	adminSettings.GET("", endpoint.GetSettings)
	adminSettings.PUT("", endpoint.UpdateSettings)
	adminSettings.GET("/pre-aggregation", pre.GetPreAggregationSettings)
	adminSettings.PUT("/pre-aggregation", pre.UpdatePreAggregationSettings)
	adminSettings.POST("/pre-aggregation/backfill", pre.BackfillPreAggregation)
}
