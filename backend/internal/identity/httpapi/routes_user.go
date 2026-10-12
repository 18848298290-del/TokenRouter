package httpapi

import "github.com/gin-gonic/gin"

// RegisterUserRoutes adds profile and authentication-factor endpoints to an authenticated group.
// The caller installs shared authentication, rate limits, and auditing.
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
