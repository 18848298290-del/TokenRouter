package httpapi

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
)

// jwt_auth.go authenticates panel requests and validates current-account JWT
// state for panel and administrator entrypoints. Access claims are checked
// against current user status and credential version before optional session
// binding. Administrator entrypoints check the role before publishing identity;
// panel middleware records activity after publishing identity.

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

		claims, user, ok := validateCurrentJWT(c, tokenString, authService, userService, settingService, auditService)
		if !ok {
			return
		}

		authctx.SetPrincipal(c, identity.Principal{UserID: user.ID, Role: user.Role, SessionID: claims.SessionID, CredentialKind: "jwt"}, user.Concurrency, user.Email)
		if activityToucher != nil {
			activityToucher.TouchLastActiveForUser(c.Request.Context(), user)
		}

		c.Next()
	}
}

// validateCurrentJWT returns signed claims and the current active account after
// credential-version and session-binding checks. It aborts with the first
// authentication failure and returns false; callers apply any role check and
// publish identity after success. Authentication and user readers supply nonnil
// results when returning nil errors.
func validateCurrentJWT(
	c *gin.Context,
	token string,
	authService SessionAuth,
	userService UserReader,
	settingService BindingSettings,
	auditService AuthObserver,
) (*identity.JWTClaims, *identity.User, bool) {
	claims, err := authService.ValidateToken(token)
	if err != nil {
		if errors.Is(err, identity.ErrTokenExpired) {
			AbortWithError(c, 401, "TOKEN_EXPIRED", "Token has expired")
			return nil, nil, false
		}
		AbortWithError(c, 401, "INVALID_TOKEN", "Invalid token")
		return nil, nil, false
	}

	// Read current state so disabling a user takes effect before token expiry.
	user, err := userService.GetByID(c.Request.Context(), claims.UserID)
	if err != nil {
		AbortWithError(c, 401, "USER_NOT_FOUND", "User not found")
		return nil, nil, false
	}

	if !user.IsActive() {
		AbortWithError(c, 401, "USER_INACTIVE", "User account is not active")
		return nil, nil, false
	}

	// Credential changes update the current user fingerprint; comparing it with
	// the signed version prevents an earlier token from authenticating afterward.
	if claims.TokenVersion != user.TokenVersion {
		AbortWithError(c, 401, "TOKEN_REVOKED", "Token has been revoked (password changed)")
		return nil, nil, false
	}

	// Binding enforcement can revoke the refresh family and record an audit event.
	// Complete it before entrypoints check roles or publish authenticated values.
	if !EnforceSessionBinding(c, authService, settingService, auditService, claims) {
		return nil, nil, false
	}

	return claims, user, true
}
