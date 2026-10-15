package httpapi

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
)

// jwt_auth.go authenticates panel JWT requests against current account and
// session-binding state.
// Panel JWTs are checked against a current user before a request receives
// authenticated Gin values. A valid signature alone is insufficient when the
// account is inactive, the credential fingerprint changed, or the configured
// session binding rejects the request.

// JWTAuth builds middleware that validates a Bearer token, loads its current
// user, and applies session binding before recording a JWT principal. Supply
// initialized authentication and user readers; optional activity recording
// runs after successful authentication and before downstream handlers.
func JWTAuth(
	authService SessionAuth,
	userService UserReader,
	activityToucher ActivityToucher,
	settingService BindingSettings,
	auditService AuthObserver,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			AbortWithError(c, 401, "UNAUTHORIZED", "Authorization header is required")
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			AbortWithError(c, 401, "INVALID_AUTH_HEADER", "Authorization header format must be 'Bearer {token}'")
			return
		}

		tokenString := strings.TrimSpace(parts[1])
		if tokenString == "" {
			AbortWithError(c, 401, "EMPTY_TOKEN", "Token cannot be empty")
			return
		}

		claims, err := authService.ValidateToken(tokenString)
		if err != nil {
			if errors.Is(err, identity.ErrTokenExpired) {
				AbortWithError(c, 401, "TOKEN_EXPIRED", "Token has expired")
				return
			}
			AbortWithError(c, 401, "INVALID_TOKEN", "Invalid token")
			return
		}

		// Read current state so disabling a user takes effect before token expiry.
		user, err := userService.GetByID(c.Request.Context(), claims.UserID)
		if err != nil {
			AbortWithError(c, 401, "USER_NOT_FOUND", "User not found")
			return
		}

		if !user.IsActive() {
			AbortWithError(c, 401, "USER_INACTIVE", "User account is not active")
			return
		}

		// Credential changes update the current user fingerprint; comparing it with
		// the signed version prevents an earlier token from authenticating
		// afterward.
		if claims.TokenVersion != user.TokenVersion {
			AbortWithError(c, 401, "TOKEN_REVOKED", "Token has been revoked (password changed)")
			return
		}

		// Binding enforcement can revoke the refresh family and abort this request;
		// publish authenticated Gin values only after that check accepts the token.
		if !EnforceSessionBinding(c, authService, settingService, auditService, claims) {
			return
		}

		authctx.SetPrincipal(c, identity.Principal{UserID: user.ID, Role: user.Role, SessionID: claims.SessionID, CredentialKind: "jwt"}, user.Concurrency, user.Email)
		if activityToucher != nil {
			activityToucher.TouchLastActiveForUser(c.Request.Context(), user)
		}

		c.Next()
	}
}
