package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/server/adminapi"
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
		adminapi.Register(codes, ManagementOperations["redeem_codes.list"], endpoint.List)
		adminapi.Register(codes, ManagementOperations["redeem_codes.export.list"], endpoint.Export)
		adminapi.Register(codes, ManagementOperations["redeem_codes.get"], endpoint.GetByID)
		adminapi.Register(codes, ManagementOperations["redeem_codes.create_and_redeem.create"], endpoint.CreateAndRedeem)
		adminapi.Register(codes, ManagementOperations["redeem_codes.generate.create"], endpoint.Generate)
		adminapi.Register(codes, ManagementOperations["redeem_codes.update"], endpoint.Update)
		adminapi.Register(codes, ManagementOperations["redeem_codes.delete"], endpoint.Delete)
		adminapi.Register(codes, ManagementOperations["redeem_codes.batch_delete.create"], endpoint.BatchDelete)
		adminapi.Register(codes, ManagementOperations["redeem_codes.batch_update.create"], endpoint.BatchUpdate)
		adminapi.Register(codes, ManagementOperations["redeem_codes.expire.create"], endpoint.Expire)
	}
}

// RegisterSubscriptionRoutes binds subscription maintenance below the supplied
// administrator group, plus plan/user lookup paths. Revocation uses both POST
// and DELETE for compatibility; the caller installs authentication, request
// policy and auditing and supplies initialized endpoints.
func RegisterSubscriptionRoutes(admin *gin.RouterGroup, endpoint *AdminSubscriptionHandler) {
	subscriptions := admin.Group("/subscriptions")
	{
		adminapi.Register(subscriptions, ManagementOperations["subscriptions.list"], endpoint.List)
		adminapi.Register(subscriptions, ManagementOperations["subscriptions.get"], endpoint.GetByID)
		adminapi.Register(subscriptions, ManagementOperations["subscriptions.progress.list"], endpoint.GetProgress)
		adminapi.Register(subscriptions, ManagementOperations["subscriptions.assign.create"], endpoint.Assign)
		adminapi.Register(subscriptions, ManagementOperations["subscriptions.bulk_assign.create"], endpoint.BulkAssign)
		adminapi.Register(subscriptions, ManagementOperations["subscriptions.extend.create"], endpoint.Extend)
		adminapi.Register(subscriptions, ManagementOperations["subscriptions.reset_quota.create"], endpoint.ResetQuota)
		adminapi.Register(subscriptions, ManagementOperations["subscriptions.revoke.create"], endpoint.Revoke)
		adminapi.Register(subscriptions, ManagementOperations["subscriptions.restore.create"], endpoint.Restore)
		adminapi.Register(subscriptions, ManagementOperations["subscriptions.delete"], endpoint.Revoke)
	}

	adminapi.Register(admin, ManagementOperations["plans.subscriptions.list"], endpoint.ListByPlan)

	adminapi.Register(admin, ManagementOperations["users.subscriptions.list"], endpoint.ListByUser)
}
