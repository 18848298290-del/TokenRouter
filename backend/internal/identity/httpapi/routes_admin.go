package httpapi

import (
	"github.com/gin-gonic/gin"
)

// Administrator attribute definitions and account operations share the
// caller-installed administrator route group. Fixed batch and reorder paths
// are registered with the same handler families as per-ID operations, while
// user attribute values remain under the selected account ID.

// RegisterUserAttributeRoutes adds administrator definition, reorder, and
// batch attribute endpoints. The caller supplies a group with administrator
// authentication, panel rate limits, and auditing already installed before
// exposing these handlers.
func RegisterUserAttributeRoutes(admin *gin.RouterGroup, endpoint *UserAttributeHandler) {
	attrs := admin.Group("/user-attributes")
	{
		attrs.GET("", endpoint.ListDefinitions)
		attrs.POST("", endpoint.CreateDefinition)
		attrs.POST("/batch", endpoint.GetBatchUserAttributes)
		attrs.PUT("/reorder", endpoint.ReorderDefinitions)
		attrs.PUT("/:id", endpoint.UpdateDefinition)
		attrs.DELETE("/:id", endpoint.DeleteDefinition)
	}
}

// RegisterUserManagementRoutes adds account administration and per-user
// attribute endpoints to the supplied group. The caller installs administrator
// authentication and common limits and auditing before invoking this
// registrar.
func RegisterUserManagementRoutes[K any](admin *gin.RouterGroup, endpoint *AdminUserHandler[K], attributes *UserAttributeHandler) {
	users := admin.Group("/users")
	{
		users.POST("/batch-concurrency", endpoint.BatchUpdateConcurrency)
		users.POST("/batch-limits", endpoint.BatchUpdateLimits)
		users.GET("", endpoint.List)
		users.GET("/:id", endpoint.GetByID)
		users.POST("/:id/auth-identities", endpoint.BindAuthIdentity)
		users.POST("", endpoint.Create)
		users.PUT("/:id", endpoint.Update)
		users.DELETE("/:id", endpoint.Delete)
		users.POST("/:id/balance", endpoint.UpdateBalance)
		users.GET("/:id/api-keys", endpoint.GetUserAPIKeys)
		users.GET("/:id/usage", endpoint.GetUserUsage)
		users.GET("/:id/balance-history", endpoint.GetBalanceHistory)
		users.POST("/:id/replace-group", endpoint.ReplaceGroup)
		users.GET("/:id/rpm-status", endpoint.GetUserRPMStatus)

		// User attribute values.
		users.GET("/:id/attributes", attributes.GetUserAttributes)
		users.PUT("/:id/attributes", attributes.UpdateUserAttributes)
	}
}
