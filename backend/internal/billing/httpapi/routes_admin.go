package httpapi

import (
	"github.com/gin-gonic/gin"
)

// routes_admin.go registers administrator redemption and subscription HTTP
// endpoints.
// Administrator billing routes expose credential management and subscription
// maintenance below the group supplied by app wiring. Subscription lookup also
// mounts plan/user-specific paths, and revocation retains both POST and DELETE
// spellings; caller middleware supplies authentication, request limits and
// auditing.

// RegisterRedeemCodeRoutes binds credential list/export, generation,
// adjustment and mutation methods below the supplied administrator group. The
// caller provides initialized dependencies and authentication, request limits
// and auditing middleware; selected write methods use the handler's own
// command executor.
func RegisterRedeemCodeRoutes(admin *gin.RouterGroup, endpoint *AdminRedeemHandler) {
	codes := admin.Group("/redeem-codes")
	{
		codes.GET("", endpoint.List)
		codes.GET("/export", endpoint.Export)
		codes.GET("/:id", endpoint.GetByID)
		codes.POST("/create-and-redeem", endpoint.CreateAndRedeem)
		codes.POST("/generate", endpoint.Generate)
		codes.PUT("/:id", endpoint.Update)
		codes.DELETE("/:id", endpoint.Delete)
		codes.POST("/batch-delete", endpoint.BatchDelete)
		codes.POST("/batch-update", endpoint.BatchUpdate)
		codes.POST("/:id/expire", endpoint.Expire)
	}
}

// RegisterSubscriptionRoutes binds subscription maintenance below the supplied
// administrator group, plus plan/user lookup paths. Revocation uses both POST
// and DELETE for compatibility; the caller installs authentication, request
// policy and auditing and supplies initialized endpoints.
func RegisterSubscriptionRoutes(admin *gin.RouterGroup, endpoint *AdminSubscriptionHandler) {
	subscriptions := admin.Group("/subscriptions")
	{
		subscriptions.GET("", endpoint.List)
		subscriptions.GET("/:id", endpoint.GetByID)
		subscriptions.GET("/:id/progress", endpoint.GetProgress)
		subscriptions.POST("/assign", endpoint.Assign)
		subscriptions.POST("/bulk-assign", endpoint.BulkAssign)
		subscriptions.POST("/:id/extend", endpoint.Extend)
		subscriptions.POST("/:id/reset-quota", endpoint.ResetQuota)
		subscriptions.POST("/:id/revoke", endpoint.Revoke)
		subscriptions.POST("/:id/restore", endpoint.Restore)
		subscriptions.DELETE("/:id", endpoint.Revoke)
	}

	admin.GET("/plans/:id/subscriptions", endpoint.ListByPlan)

	admin.GET("/users/:id/subscriptions", endpoint.ListByUser)
}
