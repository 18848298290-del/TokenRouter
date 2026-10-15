package httpapi

import "github.com/gin-gonic/gin"

// routes_user.go registers authenticated profile, identity binding,
// notification email, and authentication-factor endpoints.
// User profile and authentication-factor operations share an authenticated
// panel group. Email and provider binding, notification recipients, TOTP, and
// Passkey endpoints all use that account context; public Passkey login is
// registered separately with the public auth routes.

// RegisterUserRoutes adds current-account profile, binding, notification,
// TOTP, and Passkey-management endpoints. The caller supplies initialized
// handlers and installs shared authentication, panel limits, auditing, and
// backend-mode checks on the group.
func RegisterUserRoutes(authenticated *gin.RouterGroup, endpoint *UserHandler, totpEndpoint *TotpHandler, passkeyEndpoint *PasskeyHandler) {
	user := authenticated.Group("/user")
	{
		user.GET("/profile", endpoint.GetProfile)
		user.PUT("/password", endpoint.ChangePassword)
		user.PUT("", endpoint.UpdateProfile)
		user.POST("/account-bindings/email/send-code", endpoint.SendEmailBindingCode)
		user.POST("/account-bindings/email", endpoint.BindEmailIdentity)
		user.DELETE("/account-bindings/:provider", endpoint.UnbindIdentity)
		user.POST("/auth-identities/bind/start", endpoint.StartIdentityBinding)

		// Notification email management.
		notifyEmail := user.Group("/notify-email")
		{
			notifyEmail.POST("/send-code", endpoint.SendNotifyEmailCode)
			notifyEmail.POST("/verify", endpoint.VerifyNotifyEmail)
			notifyEmail.PUT("/toggle", endpoint.ToggleNotifyEmail)
			notifyEmail.DELETE("", endpoint.RemoveNotifyEmail)
		}

		// TOTP two-factor authentication.
		totp := user.Group("/totp")
		{
			totp.GET("/status", totpEndpoint.GetStatus)
			totp.GET("/verification-method", totpEndpoint.GetVerificationMethod)
			totp.POST("/send-code", totpEndpoint.SendVerifyCode)
			totp.POST("/setup", totpEndpoint.InitiateSetup)
			totp.POST("/enable", totpEndpoint.Enable)
			totp.POST("/disable", totpEndpoint.Disable)
			// Grant the current session temporary permission for sensitive operations.
			totp.POST("/step-up", totpEndpoint.StepUp)
		}

		passkeys := user.Group("/passkeys")
		{
			passkeys.GET("", passkeyEndpoint.List)
			passkeys.POST("/register/begin", passkeyEndpoint.BeginRegistration)
			passkeys.POST("/register/finish", passkeyEndpoint.FinishRegistration)
			passkeys.PATCH("/:id", passkeyEndpoint.Rename)
			passkeys.DELETE("/:id", passkeyEndpoint.Delete)
		}
	}
}
