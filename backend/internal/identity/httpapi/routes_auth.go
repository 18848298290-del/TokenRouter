package httpapi

import (
	"time"

	"github.com/gin-gonic/gin"
)

// routes_auth.go registers public authentication endpoints and protected panel
// session endpoints with their request guards.
// Public login routes and protected session routes use different guard
// sequences. Registration installs backend-mode and audit middleware on the
// public group, then adds per-action rate limits; authenticated account
// operations first validate JWT identity before backend-user and panel checks.
//
// OAuth providers share anonymous start, callback, and pending-continuation
// routes. Current-user bind starts set their intent before entering the same
// provider start handler, which performs target authentication. Payment OAuth
// is registered by a supplied callback on the public auth group. The caller
// supplies initialized handlers and middleware, including the rate-limit
// factory, before the router serves requests.

// AuthRouteMiddleware supplies the guards and rate-limit factory used during
// identity route registration.
type AuthRouteMiddleware struct {
	// JWT validates panel tokens; Audit records authentication actions;
	// BackendAuth filters public auth paths; BackendUser checks authenticated
	// self-service; Panel applies panel request limits. RegisterSessionRoutes
	// installs JWT before BackendUser and Panel, while public authentication
	// installs BackendAuth before Audit.
	JWT, Audit, BackendAuth, BackendUser, Panel gin.HandlerFunc
	// Limit constructs middleware for a named per-action count and duration.
	// Supply a nonnil factory; the production Redis limiter rejects requests when
	// its store is unavailable.
	Limit func(string, int, time.Duration) gin.HandlerFunc
}

// RegisterAuthenticationRoutes installs public identity routes and invokes the
// supplied payment OAuth registrar on the auth group. Supply initialized
// endpoint and Passkey handlers, guards, and a nonnil payment registrar before
// serving requests.
//
// Backend-mode filtering and auditing run before endpoint-specific rate
// limits. Both GET and POST provider starts reach the same handlers, which
// select redirect or JSON responses and enforce enabled action CAPTCHA.
// Current-user bind-start callbacks set bind_current_user in the query before
// calling provider starts, so those handlers resolve the authenticated target
// instead of beginning anonymous login.
func RegisterAuthenticationRoutes(v1 *gin.RouterGroup, endpoints AuthEndpoints, passkeys *PasskeyHandler, guards AuthRouteMiddleware, registerPaymentAuth func(*gin.RouterGroup)) {
	// Public authentication endpoints.
	auth := v1.Group("/auth")
	auth.Use(guards.BackendAuth)
	// Audit login, registration, two-factor authentication, and token-refresh
	// events.
	auth.Use(guards.Audit)
	{
		// Authentication, refresh, verification-mail, code-preview, reset, Passkey,
		// and pending-continuation requests each use a named per-action minute window.
		// The production limiter rejects excess requests or unavailable Redis reads
		// before the endpoint changes credentials or continuation state.
		auth.POST("/register", guards.Limit("auth-register", 5, time.Minute), endpoints.Register)
		auth.POST("/login", guards.Limit("auth-login", 20, time.Minute), endpoints.Login)
		auth.POST("/login/2fa", guards.Limit("auth-login-2fa", 20, time.Minute), endpoints.Login2FA)
		auth.POST("/passkey/login/begin", guards.Limit("passkey-login-begin", 20, time.Minute), passkeys.BeginLogin)
		auth.POST("/passkey/login/finish", guards.Limit("passkey-login-finish", 20, time.Minute), passkeys.FinishLogin)
		auth.POST("/send-verify-code", guards.Limit("auth-send-verify-code", 5, time.Minute), endpoints.SendVerifyCode)
		auth.POST("/refresh", guards.Limit("refresh-token", 30, time.Minute), endpoints.RefreshToken)
		// Logout is public so clients can revoke a refresh token without an access
		// token.
		auth.POST("/logout", endpoints.Logout)
		auth.POST("/validate-promo-code", guards.Limit("validate-promo", 10, time.Minute), endpoints.ValidatePromoCode)
		auth.POST("/validate-invitation-code", guards.Limit("validate-invitation", 10, time.Minute), endpoints.ValidateInvitationCode)
		auth.POST("/forgot-password", guards.Limit("forgot-password", 5, time.Minute), endpoints.ForgotPassword)
		auth.POST("/reset-password", guards.Limit("reset-password", 10, time.Minute), endpoints.ResetPassword)
		auth.GET("/oauth/linuxdo/start", endpoints.LinuxDoOAuthStart)
		auth.POST("/oauth/linuxdo/start", guards.Limit("oauth-linuxdo-start", 20, time.Minute), endpoints.LinuxDoOAuthStart)
		auth.GET("/oauth/linuxdo/bind/start", func(c *gin.Context) {
			// The LinuxDo bind-start route selects current-user intent before provider
			// dispatch, so its start handler authenticates the binding target and skips
			// anonymous-login CAPTCHA.
			query := c.Request.URL.Query()
			query.Set("intent", "bind_current_user")
			c.Request.URL.RawQuery = query.Encode()
			endpoints.LinuxDoOAuthStart(c)
		})
		auth.GET("/oauth/linuxdo/callback", endpoints.LinuxDoOAuthCallback)
		auth.GET("/oauth/wechat/start", endpoints.WeChatOAuthStart)
		auth.POST("/oauth/wechat/start", guards.Limit("oauth-wechat-start", 20, time.Minute), endpoints.WeChatOAuthStart)
		auth.GET("/oauth/wechat/bind/start", func(c *gin.Context) {
			// The WeChat bind-start route selects current-user intent before provider
			// dispatch, so its start handler authenticates the binding target and skips
			// anonymous-login CAPTCHA.
			query := c.Request.URL.Query()
			query.Set("intent", "bind_current_user")
			c.Request.URL.RawQuery = query.Encode()
			endpoints.WeChatOAuthStart(c)
		})
		auth.GET("/oauth/wechat/callback", endpoints.WeChatOAuthCallback)
		registerPaymentAuth(auth)
		auth.POST("/oauth/pending/exchange",
			guards.Limit("oauth-pending-exchange", 20, time.Minute),
			endpoints.ExchangePendingOAuthCompletion,
		)
		auth.POST("/oauth/pending/send-verify-code",
			guards.Limit("oauth-pending-send-verify-code", 5, time.Minute),
			endpoints.SendPendingOAuthVerifyCode,
		)
		auth.POST("/oauth/pending/create-account",
			guards.Limit("oauth-pending-create-account", 10, time.Minute),
			endpoints.CreatePendingOAuthAccount,
		)
		auth.POST("/oauth/pending/bind-login",
			guards.Limit("oauth-pending-bind-login", 10, time.Minute),
			endpoints.BindPendingOAuthLogin,
		)
		auth.POST("/oauth/linuxdo/complete-registration",
			guards.Limit("oauth-linuxdo-complete", 10, time.Minute),
			endpoints.CompleteLinuxDoOAuthRegistration,
		)
		auth.POST("/oauth/linuxdo/bind-login",
			guards.Limit("oauth-linuxdo-bind-login", 20, time.Minute),
			endpoints.BindLinuxDoOAuthLogin,
		)
		auth.POST("/oauth/linuxdo/create-account",
			guards.Limit("oauth-linuxdo-create-account", 10, time.Minute),
			endpoints.CreateLinuxDoOAuthAccount,
		)
		auth.POST("/oauth/wechat/complete-registration",
			guards.Limit("oauth-wechat-complete", 10, time.Minute),
			endpoints.CompleteWeChatOAuthRegistration,
		)
		auth.POST("/oauth/wechat/bind-login",
			guards.Limit("oauth-wechat-bind-login", 20, time.Minute),
			endpoints.BindWeChatOAuthLogin,
		)
		auth.POST("/oauth/wechat/create-account",
			guards.Limit("oauth-wechat-create-account", 10, time.Minute),
			endpoints.CreateWeChatOAuthAccount,
		)
		auth.GET("/oauth/oidc/start", endpoints.OIDCOAuthStart)
		auth.POST("/oauth/oidc/start", guards.Limit("oauth-oidc-start", 20, time.Minute), endpoints.OIDCOAuthStart)
		auth.GET("/oauth/oidc/bind/start", func(c *gin.Context) {
			// The OIDC bind-start route selects current-user intent before provider
			// dispatch, so its start handler authenticates the binding target and skips
			// anonymous-login CAPTCHA.
			query := c.Request.URL.Query()
			query.Set("intent", "bind_current_user")
			c.Request.URL.RawQuery = query.Encode()
			endpoints.OIDCOAuthStart(c)
		})
		auth.GET("/oauth/oidc/callback", endpoints.OIDCOAuthCallback)
		auth.POST("/oauth/oidc/complete-registration",
			guards.Limit("oauth-oidc-complete", 10, time.Minute),
			endpoints.CompleteOIDCOAuthRegistration,
		)
		auth.GET("/oauth/github/start", endpoints.GitHubOAuthStart)
		auth.POST("/oauth/github/start", guards.Limit("oauth-github-start", 20, time.Minute), endpoints.GitHubOAuthStart)
		auth.GET("/oauth/github/callback", endpoints.GitHubOAuthCallback)
		auth.POST("/oauth/github/complete-registration",
			guards.Limit("oauth-github-complete", 10, time.Minute),
			endpoints.CompleteGitHubOAuthRegistration,
		)
		auth.GET("/oauth/google/start", endpoints.GoogleOAuthStart)
		auth.POST("/oauth/google/start", guards.Limit("oauth-google-start", 20, time.Minute), endpoints.GoogleOAuthStart)
		auth.GET("/oauth/google/callback", endpoints.GoogleOAuthCallback)
		auth.POST("/oauth/google/one-tap", guards.Limit("oauth-google-one-tap", 20, time.Minute), endpoints.GoogleOneTap)
		auth.POST("/oauth/google/complete-registration",
			guards.Limit("oauth-google-complete", 10, time.Minute),
			endpoints.CompleteGoogleOAuthRegistration,
		)
		auth.POST("/oauth/oidc/bind-login",
			guards.Limit("oauth-oidc-bind-login", 20, time.Minute),
			endpoints.BindOIDCOAuthLogin,
		)
		auth.POST("/oauth/oidc/create-account",
			guards.Limit("oauth-oidc-create-account", 10, time.Minute),
			endpoints.CreateOIDCOAuthAccount,
		)
		auth.GET("/oauth/dingtalk/start", endpoints.DingTalkOAuthStart)
		auth.POST("/oauth/dingtalk/start", guards.Limit("oauth-dingtalk-start", 20, time.Minute), endpoints.DingTalkOAuthStart)
		auth.GET("/oauth/dingtalk/bind/start", func(c *gin.Context) {
			// The DingTalk bind-start route selects current-user intent before provider
			// dispatch, so its start handler authenticates the binding target and skips
			// anonymous-login CAPTCHA.
			query := c.Request.URL.Query()
			query.Set("intent", "bind_current_user")
			c.Request.URL.RawQuery = query.Encode()
			endpoints.DingTalkOAuthStart(c)
		})
		auth.GET("/oauth/dingtalk/callback", endpoints.DingTalkOAuthCallback)
		auth.POST("/oauth/dingtalk/complete-registration",
			guards.Limit("oauth-dingtalk-complete", 10, time.Minute),
			endpoints.CompleteDingTalkOAuthRegistration,
		)
		auth.POST("/oauth/dingtalk/bind-login",
			guards.Limit("oauth-dingtalk-bind-login", 20, time.Minute),
			endpoints.BindDingTalkOAuthLogin,
		)
		auth.POST("/oauth/dingtalk/create-account",
			guards.Limit("oauth-dingtalk-create-account", 10, time.Minute),
			endpoints.CreateDingTalkOAuthAccount,
		)
	}
}

// RegisterSessionRoutes installs current-user, session-revocation, and OAuth
// bind-cookie endpoints behind authentication. JWT runs before backend-user
// and panel guards so those checks can read the resolved account and role.
func RegisterSessionRoutes(v1 *gin.RouterGroup, endpoints AuthEndpoints, guards AuthRouteMiddleware) {
	authenticated := v1.Group("")
	authenticated.Use(guards.JWT)
	authenticated.Use(guards.BackendUser)
	authenticated.Use(guards.Panel)
	authenticated.GET("/auth/me", endpoints.GetCurrentUser)
	authenticated.POST("/auth/revoke-all-sessions", endpoints.RevokeAllSessions)
	authenticated.POST("/auth/oauth/bind-token", endpoints.PrepareOAuthBindAccessTokenCookie)
}
