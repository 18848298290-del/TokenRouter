package httpapi

import "github.com/gin-gonic/gin"

// RegisterUserRoutes registers billing endpoints on a group with authentication, rate limits, and auditing
// installed by the caller.
func RegisterUserRoutes(authenticated *gin.RouterGroup, redeemEndpoint *RedeemHandler, subscriptionEndpoint *SubscriptionHandler) {
	redeem := authenticated.Group("/redeem")
	{
		redeem.POST("", redeemEndpoint.Redeem)
		redeem.GET("/history", redeemEndpoint.GetHistory)
	}

	subscriptions := authenticated.Group("/subscriptions")
	{
		subscriptions.GET("", subscriptionEndpoint.List)
		subscriptions.GET("/active", subscriptionEndpoint.GetActive)
		subscriptions.GET("/progress", subscriptionEndpoint.GetProgress)
		subscriptions.GET("/summary", subscriptionEndpoint.GetSummary)
		subscriptions.POST("/:id/revoke", subscriptionEndpoint.Revoke)
	}
}
