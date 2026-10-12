package httpapi

import (
	"github.com/gin-gonic/gin"
)

// PlanEndpoints contains the billing handlers used by payment pages.
type PlanEndpoints interface {
	GetPlans(*gin.Context)
	ListPlans(*gin.Context)
	CreatePlan(*gin.Context)
	UpdatePlan(*gin.Context)
	DeletePlan(*gin.Context)
}

// RouteMiddleware supplies authentication, rate limiting, and audit handlers for payment routes.
type RouteMiddleware struct {
	JWT, BackendMode, Panel, Admin, Audit gin.HandlerFunc
}

// WeChatAuthEndpoints starts and completes payment OAuth.
type WeChatAuthEndpoints interface {
	WeChatPaymentOAuthStart(*gin.Context)
	WeChatPaymentOAuthCallback(*gin.Context)
}

// RegisterRoutes registers user payments, public lookups, provider callbacks, and admin endpoints.
func RegisterRoutes(
	v1 *gin.RouterGroup,
	paymentHandler *PaymentHandler,
	webhookHandler *PaymentWebhookHandler,
	adminPaymentHandler *AdminHandler,
	planHandler PlanEndpoints,
	guards RouteMiddleware,
) {
	authenticated := v1.Group("/payment")
	authenticated.Use(guards.JWT)
	authenticated.Use(guards.BackendMode)
	// Apply the panel-wide per-user rate limit.
	authenticated.Use(guards.Panel)
	{
		authenticated.GET("/config", paymentHandler.GetPaymentConfig)
		authenticated.GET("/checkout-info", paymentHandler.GetCheckoutInfo)
		authenticated.GET("/plans", planHandler.GetPlans)
		authenticated.GET("/limits", paymentHandler.GetLimits)

		orders := authenticated.Group("/orders")
		{
			orders.POST("", paymentHandler.CreateOrder)
			orders.POST("/verify", paymentHandler.VerifyOrder)
			orders.GET("/my", paymentHandler.GetMyOrders)
			orders.GET("/:id", paymentHandler.GetOrder)
			orders.GET("/:id/invoice", paymentHandler.GetOrderInvoice)
			orders.POST("/:id/cancel", paymentHandler.CancelOrder)
			orders.POST("/:id/refund-request", paymentHandler.RequestRefund)
			orders.GET("/refund-eligible-providers", paymentHandler.GetRefundEligibleProviders)
		}
	}

	// Public recovery uses signed resume tokens; anonymous order-number lookup returns saved state for older clients.
	public := v1.Group("/payment/public")
	{
		public.POST("/orders/verify", paymentHandler.VerifyOrderPublic)
		public.POST("/orders/resolve", paymentHandler.ResolveOrderPublicByResumeToken)
	}

	// Provider signatures authenticate callbacks.
	webhook := v1.Group("/payment/webhook")
	{
		// EasyPay GET callbacks carry notification fields in the query string.
		webhook.GET("/easypay", webhookHandler.EasyPayNotify)
		webhook.POST("/easypay", webhookHandler.EasyPayNotify)
		webhook.POST("/alipay", webhookHandler.AlipayNotify)
		webhook.POST("/wxpay", webhookHandler.WxpayNotify)
		webhook.POST("/stripe", webhookHandler.StripeWebhook)
		webhook.POST("/airwallex", webhookHandler.AirwallexWebhook)
	}

	adminGroup := v1.Group("/admin/payment")
	adminGroup.Use(guards.Admin)
	// Payment admin routes are registered separately and need their own panel rate-limit middleware.
	adminGroup.Use(guards.Panel)
	adminGroup.Use(guards.Audit)
	{
		// Dashboard
		adminGroup.GET("/dashboard", adminPaymentHandler.GetDashboard)

		// Config
		adminGroup.GET("/config", adminPaymentHandler.GetConfig)
		adminGroup.PUT("/config", adminPaymentHandler.UpdateConfig)

		// Orders
		adminOrders := adminGroup.Group("/orders")
		{
			adminOrders.GET("", adminPaymentHandler.ListOrders)
			adminOrders.GET("/:id", adminPaymentHandler.GetOrderDetail)
			adminOrders.GET("/:id/invoice", adminPaymentHandler.GetOrderInvoice)
			adminOrders.POST("/:id/cancel", adminPaymentHandler.CancelOrder)
			adminOrders.POST("/:id/force-expire", adminPaymentHandler.ForceExpireOrder)
			adminOrders.POST("/:id/retry", adminPaymentHandler.RetryFulfillment)
			adminOrders.POST("/:id/refund", adminPaymentHandler.ProcessRefund)
			adminOrders.POST("/:id/refund/query", adminPaymentHandler.QueryAndFinalizeRefund)
		}

		// Subscription Plans
		plans := adminGroup.Group("/plans")
		{
			plans.GET("", planHandler.ListPlans)
			plans.POST("", planHandler.CreatePlan)
			plans.PUT("/:id", planHandler.UpdatePlan)
			plans.DELETE("/:id", planHandler.DeletePlan)
		}

		// Provider Instances
		providers := adminGroup.Group("/providers")
		{
			providers.GET("", adminPaymentHandler.ListProviders)
			providers.POST("", adminPaymentHandler.CreateProvider)
			providers.POST("/test", adminPaymentHandler.TestProvider)
			providers.PUT("/:id", adminPaymentHandler.UpdateProvider)
			providers.DELETE("/:id", adminPaymentHandler.DeleteProvider)
		}
	}
}

// RegisterWeChatAuthRoutes registers payment OAuth endpoints on the identity route group.
func RegisterWeChatAuthRoutes(auth *gin.RouterGroup, endpoint WeChatAuthEndpoints) {
	auth.GET("/oauth/wechat/payment/start", endpoint.WeChatPaymentOAuthStart)
	auth.GET("/oauth/wechat/payment/callback", endpoint.WeChatPaymentOAuthCallback)
}
