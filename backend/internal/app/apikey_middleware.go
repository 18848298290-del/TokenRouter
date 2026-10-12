package app

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
)

// newGatewayAuthorization uses the shared API key and subscription services for gateway authentication.
func newGatewayAuthorization(keys *apikey.APIKeyService, subscriptions *billing.SubscriptionService, cfg *config.Config, google bool) gin.HandlerFunc {
	options := gatewayhttp.APIKeyAuthorizationOptions{
		Authentication: keyhttp.AuthenticationOptions{
			Google: google, Context: func(c *gin.Context) context.Context { return c.Request.Context() },
			ClientIP: func(c *gin.Context) string {
				return clientip.GetSecurityClientIP(c, cfg.TrustForwardedIPForAPIKeyACL())
			},
			AbuseClientKey: middleware.InvalidAuthClientKey, NonConsuming: func(c *gin.Context) bool {
				return gatewayhttp.IsAPIKeyNonConsumingRequest(c.Request.Method, c.Request.URL.Path)
			},
			Rejected: func(c *gin.Context, reason string) {
				middleware.MarkIngressRejected(c, middleware.IngressRejectReason(reason))
			},
			BusinessLimited: func(c *gin.Context, reason string) {
				gatewayhttp.MarkOpsClientBusinessLimited(c, reason)
			},
			Loaded: func(c *gin.Context, key *apikey.APIKey) {
				keyhttp.SetOpsFallbackAPIKey(c, apikey.CopyAPIKey(key))
			},
		},
		BindLegacyKey: func(c *gin.Context, key *apikey.APIKey) {
			legacy := apikey.CopyAPIKey(key)
			c.Set(string(keyhttp.ContextKeyAPIKey), legacy)
			bindLegacyAuthorizationGroup(c, legacy.Group)
		},
	}
	var reader gatewayhttp.AuthorizationSubscriptions
	if subscriptions != nil {
		reader = subscriptions
	}
	nativeKeys := keys
	if google {
		return gatewayhttp.NewGoogleAPIKeyAuthorization(nativeKeys, reader, options)
	}
	return gatewayhttp.NewAPIKeyAuthorization(nativeKeys, reader, options)
}

// provideAPIKeyAuth constructs API key middleware with callbacks that convert request data.
func provideAPIKeyAuth(keys *apikey.APIKeyService, subscriptions *billing.SubscriptionService, cfg *config.Config) keyhttp.APIKeyAuthMiddleware {
	return keyhttp.APIKeyAuthMiddleware(newGatewayAuthorization(keys, subscriptions, cfg, false))
}

// bindLegacyAuthorizationGroup stores a valid group in request state unless the same valid group is already
// present.
func bindLegacyAuthorizationGroup(c *gin.Context, group *routing.Group) {
	if !routing.IsGroupContextValid(group) {
		return
	}
	if current, ok := requeststate.GroupFromContext(c.Request.Context()); ok && current != nil && current.ID == group.ID && routing.IsGroupContextValid(current) {
		return
	}
	c.Request = c.Request.WithContext(requeststate.WithGroup(c.Request.Context(), group))
}
