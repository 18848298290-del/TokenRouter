package httpapi

import "github.com/gin-gonic/gin"

// Authenticated users can read their own invitation details and transfer
// their own available rebate quota. The application guards the parent group,
// and the mounted handlers obtain the affected account from the authenticated
// subject.

// RegisterUserRoutes mounts affiliate detail and transfer endpoints under the
// authenticated user group.
//
// Callers provide a usable handler and install application authentication,
// backend-user admission, panel limits, and audit middleware first.
func RegisterUserRoutes(authenticated *gin.RouterGroup, endpoint *UserHandler) {
	user := authenticated.Group("/user")
	user.GET("/aff", endpoint.GetAffiliate)
	user.POST("/aff/transfer", endpoint.TransferAffiliateQuota)
}
