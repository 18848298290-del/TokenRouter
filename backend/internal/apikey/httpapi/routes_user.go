package httpapi

import "github.com/gin-gonic/gin"

// routes_user.go registers console routes for Key configuration and funding
// choices. Registration binds list, detail, creation, update, rotation and
// deletion alongside available-group and rate queries, with middleware
// supplied by the caller before the handlers access their authenticated
// subject.

// RegisterUserRoutes binds Key administration and group-option queries below
// the supplied authenticated group. Callers install identity authentication,
// rate limits and auditing before registration; creation uses the handler's
// idempotency executor, and rotation returns the replacement secret directly.
func RegisterUserRoutes[G any](authenticated *gin.RouterGroup, endpoint *APIKeyHandler[G]) {
	keys := authenticated.Group("/keys")
	{
		keys.GET("", endpoint.List)
		keys.GET("/billing-options", endpoint.GetBillingOptions)
		keys.GET("/:id", endpoint.GetByID)
		keys.POST("", endpoint.Create)
		keys.PUT("/:id", endpoint.Update)
		keys.POST("/:id/rotate", endpoint.RotateCredential)
		keys.DELETE("/:id", endpoint.Delete)
	}
	groups := authenticated.Group("/groups")
	{
		groups.GET("/available", endpoint.GetAvailableGroups)
		groups.GET("/rates", endpoint.GetUserGroupRates)
	}
}
