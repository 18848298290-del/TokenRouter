package httpapi

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
)

// JWTAuth validates a bearer JWT against current user state and session binding.
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

		// Reject tokens whose password-derived version is stale.
		if claims.TokenVersion != user.TokenVersion {
			AbortWithError(c, 401, "TOKEN_REVOKED", "Token has been revoked (password changed)")
			return
		}

		// Apply the configured IP and User-Agent binding check.
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
