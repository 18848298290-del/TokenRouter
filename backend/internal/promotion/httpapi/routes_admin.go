package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_admin.go registers administrator affiliate and registration promotion
// endpoints.
// Affiliate administration exposes relationship, rebate, and transfer records
// together with per-user code and rate edits. Promo administration manages
// registration grant codes and their receipts; both route groups inherit
// security middleware installed by the application.

// RegisterAffiliateRoutes mounts affiliate administration and audit-record
// endpoints under admin's affiliates group.
//
// Callers supply initialized handlers and install the application's
// administrator authentication, rate limits, and audit middleware on the
// parent group.
func RegisterAffiliateRoutes(admin *gin.RouterGroup, endpoint *AffiliateHandler) {
	affiliates := admin.Group("/affiliates")
	{
		adminapi.Register(affiliates, ManagementOperations["affiliates.users.list"], endpoint.ListUsers)
		adminapi.Register(affiliates, ManagementOperations["affiliates.users.lookup.list"], endpoint.LookupUsers)
		adminapi.Register(affiliates, ManagementOperations["affiliates.users.update"], endpoint.UpdateUserSettings)
		adminapi.Register(affiliates, ManagementOperations["affiliates.users.delete"], endpoint.ClearUserSettings)
		adminapi.Register(affiliates, ManagementOperations["affiliates.users.batch_rate.create"], endpoint.BatchSetRate)
		adminapi.Register(affiliates, ManagementOperations["affiliates.users.overview.list"], endpoint.GetUserOverview)
		adminapi.Register(affiliates, ManagementOperations["affiliates.invites.list"], endpoint.ListInviteRecords)
		adminapi.Register(affiliates, ManagementOperations["affiliates.rebates.list"], endpoint.ListRebateRecords)
		adminapi.Register(affiliates, ManagementOperations["affiliates.transfers.list"], endpoint.ListTransferRecords)
	}
}

// RegisterPromoCodeRoutes mounts registration Promo code CRUD and usage-list
// endpoints under admin's promo-codes group.
//
// Callers supply initialized handlers and the application's administrator
// authentication, rate limits, and audit middleware.
func RegisterPromoCodeRoutes(admin *gin.RouterGroup, endpoint *PromoHandler) {
	promoCodes := admin.Group("/promo-codes")
	{
		adminapi.Register(promoCodes, ManagementOperations["promo_codes.list"], endpoint.List)
		adminapi.Register(promoCodes, ManagementOperations["promo_codes.get"], endpoint.GetByID)
		adminapi.Register(promoCodes, ManagementOperations["promo_codes.create"], endpoint.Create)
		adminapi.Register(promoCodes, ManagementOperations["promo_codes.update"], endpoint.Update)
		adminapi.Register(promoCodes, ManagementOperations["promo_codes.delete"], endpoint.Delete)
		adminapi.Register(promoCodes, ManagementOperations["promo_codes.usages.list"], endpoint.GetUsages)
	}
}
