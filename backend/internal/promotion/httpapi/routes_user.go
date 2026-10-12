package httpapi

import "github.com/gin-gonic/gin"

// RegisterUserRoutes adds user routes to a group with app-installed authentication, rate limits, and audit
// middleware.
func RegisterUserRoutes(authenticated *gin.RouterGroup, endpoint *UserHandler) {
	user := authenticated.Group("/user")
	user.GET("/aff", endpoint.GetAffiliate)
	user.POST("/aff/transfer", endpoint.TransferAffiliateQuota)
}
