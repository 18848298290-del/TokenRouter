package httpapi

import "github.com/gin-gonic/gin"

// RegisterUserRoutes registers user endpoints after app installs authentication, rate limits, and auditing.
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
