package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
)

// routes_admin.go registers administrator routes for user management and
// custom account attributes.
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
		adminapi.Register(attrs, ManagementOperations["user_attributes.list"], endpoint.ListDefinitions)
		adminapi.Register(attrs, ManagementOperations["user_attributes.create"], endpoint.CreateDefinition)
		adminapi.Register(attrs, ManagementOperations["user_attributes.batch.create"], endpoint.GetBatchUserAttributes)
		adminapi.Register(attrs, ManagementOperations["user_attributes.reorder.update"], endpoint.ReorderDefinitions)
		adminapi.Register(attrs, ManagementOperations["user_attributes.update"], endpoint.UpdateDefinition)
		adminapi.Register(attrs, ManagementOperations["user_attributes.delete"], endpoint.DeleteDefinition)
	}
}

// RegisterUserManagementRoutes adds account administration and per-user
// attribute endpoints to the supplied group. The caller installs administrator
// authentication and common limits and auditing before invoking this
// registrar.
func RegisterUserManagementRoutes[K any](admin *gin.RouterGroup, endpoint *AdminUserHandler[K], attributes *UserAttributeHandler) {
	users := admin.Group("/users")
	{
		adminapi.Register(users, ManagementOperations["users.batch_concurrency.create"], endpoint.BatchUpdateConcurrency)
		adminapi.Register(users, ManagementOperations["users.batch_limits.create"], endpoint.BatchUpdateLimits)
		adminapi.Register(users, ManagementOperations["users.list"], endpoint.List)
		adminapi.Register(users, ManagementOperations["users.get"], endpoint.GetByID)
		adminapi.Register(users, ManagementOperations["users.auth_identities.create"], endpoint.BindAuthIdentity)
		adminapi.Register(users, ManagementOperations["users.create"], endpoint.Create)
		adminapi.Register(users, ManagementOperations["users.update"], endpoint.Update)
		adminapi.Register(users, ManagementOperations["users.delete"], endpoint.Delete)
		adminapi.Register(users, ManagementOperations["users.balance.create"], endpoint.UpdateBalance)
		adminapi.Register(users, ManagementOperations["users.api_keys.list"], endpoint.GetUserAPIKeys)
		adminapi.Register(users, ManagementOperations["users.usage.list"], endpoint.GetUserUsage)
		adminapi.Register(users, ManagementOperations["users.balance_history.list"], endpoint.GetBalanceHistory)
		adminapi.Register(users, ManagementOperations["users.replace_group.create"], endpoint.ReplaceGroup)
		adminapi.Register(users, ManagementOperations["users.rpm_status.list"], endpoint.GetUserRPMStatus)

		// User attribute values.
		adminapi.Register(users, ManagementOperations["users.attributes.list"], attributes.GetUserAttributes)
		adminapi.Register(users, ManagementOperations["users.attributes.update"], attributes.UpdateUserAttributes)
	}
}
