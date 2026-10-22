package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_admin.go registers group and pricing administration endpoints on the
// supplied router.
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
	adminapi.Register(admin, ManagementOperations["pricing.defaults.list"], endpoint.ListDefaultPricing)
	adminapi.Register(admin, ManagementOperations["pricing.defaults.model.list"], endpoint.GetModelDefaultPricing)
	adminapi.Register(admin, ManagementOperations["pricing.defaults.update.create"], endpoint.UpdateDefaultPricing)
	pricingConfigs := admin.Group("/pricing/configs")
	{
		adminapi.Register(pricingConfigs, ManagementOperations["pricing.configs.list"], endpoint.List)
		adminapi.Register(pricingConfigs, ManagementOperations["pricing.configs.get"], endpoint.GetByID)
		adminapi.Register(pricingConfigs, ManagementOperations["pricing.configs.create"], endpoint.Create)
		adminapi.Register(pricingConfigs, ManagementOperations["pricing.configs.update"], endpoint.Update)
		adminapi.Register(pricingConfigs, ManagementOperations["pricing.configs.delete"], endpoint.Delete)
	}
}

// RegisterGroupRoutes binds group administration, duplication, summaries,
// linked keys, and per-user rate or RPM changes. The caller installs
// administrator authentication, rate limits, and auditing on admin before
// these routes serve requests.
func RegisterGroupRoutes(admin *gin.RouterGroup, endpoint *GroupHandler) {
	groups := admin.Group("/groups")
	{
		adminapi.Register(groups, ManagementOperations["groups.list"], endpoint.List)
		adminapi.Register(groups, ManagementOperations["groups.all.list"], endpoint.GetAll)
		adminapi.Register(groups, ManagementOperations["groups.usage_summary.list"], endpoint.GetUsageSummary)
		adminapi.Register(groups, ManagementOperations["groups.capacity_summary.list"], endpoint.GetCapacitySummary)
		adminapi.Register(groups, ManagementOperations["groups.live_capability.list"], endpoint.GetLiveCapability)
		adminapi.Register(groups, ManagementOperations["groups.sort_order.update"], endpoint.UpdateSortOrder)
		adminapi.Register(groups, ManagementOperations["groups.models_list_candidates.list"], endpoint.GetModelsListCandidates)
		adminapi.Register(groups, ManagementOperations["groups.get"], endpoint.GetByID)
		adminapi.Register(groups, ManagementOperations["groups.create"], endpoint.Create)
		adminapi.Register(groups, ManagementOperations["groups.duplicate.create"], endpoint.Duplicate)
		adminapi.Register(groups, ManagementOperations["groups.update"], endpoint.Update)
		adminapi.Register(groups, ManagementOperations["groups.delete"], endpoint.Delete)
		adminapi.Register(groups, ManagementOperations["groups.rate_multipliers.list"], endpoint.GetGroupRateMultipliers)
		adminapi.Register(groups, ManagementOperations["groups.rate_multipliers.update"], endpoint.BatchSetGroupRateMultipliers)
		adminapi.Register(groups, ManagementOperations["groups.rate_multipliers.delete"], endpoint.ClearGroupRateMultipliers)
		adminapi.Register(groups, ManagementOperations["groups.rpm_overrides.update"], endpoint.BatchSetGroupRPMOverrides)
		adminapi.Register(groups, ManagementOperations["groups.rpm_overrides.delete"], endpoint.ClearGroupRPMOverrides)
		adminapi.Register(groups, ManagementOperations["groups.api_keys.list"], endpoint.GetGroupAPIKeys)
	}
}
