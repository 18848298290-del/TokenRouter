package httpapi

import "github.com/gin-gonic/gin"

// routes_user.go registers authenticated user redemption and subscription HTTP
// endpoints.
// User billing routes select redemption and subscription actions under an
// authenticated group owned by app wiring. History, active/progress/summary
// queries and quota-conditioned self-revocation share that group, while caller
// middleware supplies authentication, request admission and auditing.

// RegisterUserRoutes binds redemption and subscription methods below the
// caller's authenticated group. The caller supplies initialized endpoint
// dependencies and the surrounding authentication, rate-limit and auditing
// policy; handlers obtain current user identity from that installed context.
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
