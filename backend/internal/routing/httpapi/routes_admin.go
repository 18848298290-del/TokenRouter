package httpapi

import (
	"github.com/gin-gonic/gin"
)

// Price administration separates catalog reads and refresh from shared
// price-card writes. Group administration exposes product settings, current
// observations, linked keys, and per-user billing overrides through one admin
// router group, whose caller supplies authentication, rate limiting, and
// auditing middleware.

// RegisterPricingRoutes binds default catalog queries, manual refresh, and
// shared price configuration CRUD. The caller installs administrator
// authentication, rate limits, and auditing on admin before these routes
// serve requests.
func RegisterPricingRoutes(admin *gin.RouterGroup, endpoint *PricingHandler) {
	admin.GET("/pricing/defaults", endpoint.ListDefaultPricing)
	admin.GET("/pricing/defaults/model", endpoint.GetModelDefaultPricing)
	admin.POST("/pricing/defaults/update", endpoint.UpdateDefaultPricing)
	pricingConfigs := admin.Group("/pricing/configs")
	{
		pricingConfigs.GET("", endpoint.List)
		pricingConfigs.GET("/:id", endpoint.GetByID)
		pricingConfigs.POST("", endpoint.Create)
		pricingConfigs.PUT("/:id", endpoint.Update)
		pricingConfigs.DELETE("/:id", endpoint.Delete)
	}
}

// RegisterGroupRoutes binds group administration, duplication, summaries,
// linked keys, and per-user rate or RPM changes. The caller installs
// administrator authentication, rate limits, and auditing on admin before
// these routes serve requests.
func RegisterGroupRoutes(admin *gin.RouterGroup, endpoint *GroupHandler) {
	groups := admin.Group("/groups")
	{
		groups.GET("", endpoint.List)
		groups.GET("/all", endpoint.GetAll)
		groups.GET("/usage-summary", endpoint.GetUsageSummary)
		groups.GET("/capacity-summary", endpoint.GetCapacitySummary)
		groups.GET("/live-capability", endpoint.GetLiveCapability)
		groups.PUT("/sort-order", endpoint.UpdateSortOrder)
		groups.GET("/:id/models-list-candidates", endpoint.GetModelsListCandidates)
		groups.GET("/:id", endpoint.GetByID)
		groups.POST("", endpoint.Create)
		groups.POST("/:id/duplicate", endpoint.Duplicate)
		groups.PUT("/:id", endpoint.Update)
		groups.DELETE("/:id", endpoint.Delete)
		groups.GET("/:id/rate-multipliers", endpoint.GetGroupRateMultipliers)
		groups.PUT("/:id/rate-multipliers", endpoint.BatchSetGroupRateMultipliers)
		groups.DELETE("/:id/rate-multipliers", endpoint.ClearGroupRateMultipliers)
		groups.PUT("/:id/rpm-overrides", endpoint.BatchSetGroupRPMOverrides)
		groups.DELETE("/:id/rpm-overrides", endpoint.ClearGroupRPMOverrides)
		groups.GET("/:id/api-keys", endpoint.GetGroupAPIKeys)
	}
}
