package httpapi

import (
	"github.com/gin-gonic/gin"
)

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
		affiliates.GET("/users", endpoint.ListUsers)
		affiliates.GET("/users/lookup", endpoint.LookupUsers)
		affiliates.PUT("/users/:user_id", endpoint.UpdateUserSettings)
		affiliates.DELETE("/users/:user_id", endpoint.ClearUserSettings)
		affiliates.POST("/users/batch-rate", endpoint.BatchSetRate)
		affiliates.GET("/users/:user_id/overview", endpoint.GetUserOverview)
		affiliates.GET("/invites", endpoint.ListInviteRecords)
		affiliates.GET("/rebates", endpoint.ListRebateRecords)
		affiliates.GET("/transfers", endpoint.ListTransferRecords)
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
		promoCodes.GET("", endpoint.List)
		promoCodes.GET("/:id", endpoint.GetByID)
		promoCodes.POST("", endpoint.Create)
		promoCodes.PUT("/:id", endpoint.Update)
		promoCodes.DELETE("/:id", endpoint.Delete)
		promoCodes.GET("/:id/usages", endpoint.GetUsages)
	}
}
