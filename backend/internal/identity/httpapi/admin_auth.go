package httpapi

import (
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
)

// AdminAuth accepts an administrator API key in x-api-key or an administrator JWT
// in Authorization: Bearer or a WebSocket subprotocol.
func AdminAuth(
	authService SessionAuth,
	userService AdminUserReader,
	settingService AuthSettings,
	auditService AuthObserver,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Browser WebSockets carry the administrator JWT in a separate subprotocol item.
		// For example: Sec-WebSocket-Protocol: tokenrouter-admin, jwt.<token>.
		if IsWebSocketUpgradeRequest(c) {
			if token := ExtractJWTFromWebSocketSubprotocol(c); token != "" {
				if !ValidateJWTForAdmin(c, token, authService, userService, settingService, auditService) {
					return
				}
				c.Next()
				return
			}
		}

		apiKey := c.GetHeader("x-api-key")
		if apiKey != "" {
			if !ValidateAdminAPIKey(c, apiKey, settingService, userService) {
				return
			}
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				token := strings.TrimSpace(parts[1])
				if token == "" {
					AbortWithError(c, 401, "UNAUTHORIZED", "Authorization required")
					return
				}
				if !ValidateJWTForAdmin(c, token, authService, userService, settingService, auditService) {
					return
				}
				c.Next()
				return
			}
		}

		AbortWithError(c, 401, "UNAUTHORIZED", "Authorization required")
	}
}

func IsWebSocketUpgradeRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	// RFC6455 handshake uses:
	//   Connection: Upgrade
	//   Upgrade: websocket
	upgrade := strings.ToLower(strings.TrimSpace(c.GetHeader("Upgrade")))
	if upgrade != "websocket" {
		return false
	}
	connection := strings.ToLower(c.GetHeader("Connection"))
	return strings.Contains(connection, "upgrade")
}

func ExtractJWTFromWebSocketSubprotocol(c *gin.Context) string {
	if c == nil {
		return ""
	}
	raw := strings.TrimSpace(c.GetHeader("Sec-WebSocket-Protocol"))
	if raw == "" {
		return ""
	}

	// The header is a comma-separated list of tokens. We reserve the prefix "jwt."
	// for carrying the admin JWT.
	for part := range strings.SplitSeq(raw, ",") {
		p := strings.TrimSpace(part)
		if after, ok := strings.CutPrefix(p, "jwt."); ok {
			token := strings.TrimSpace(after)
			if token != "" {
				return token
			}
		}
	}
	return ""
}

// ValidateAdminAPIKey checks the configured key and authenticates as the first administrator.
func ValidateAdminAPIKey(
	c *gin.Context,
	key string,
	settingService AuthSettings,
	userService AdminUserReader,
) bool {
	storedKey, err := settingService.GetAdminAPIKey(c.Request.Context())
	if err != nil {
		AbortWithError(c, 500, "INTERNAL_ERROR", "Internal server error")
		return false
	}

	// Use the same error for missing and mismatched configured keys.
	if storedKey == "" || subtle.ConstantTimeCompare([]byte(key), []byte(storedKey)) != 1 {
		AbortWithError(c, 401, "INVALID_ADMIN_KEY", "Invalid admin API key")
		return false
	}

	// Resolve a stored administrator account for the API key principal.
	admin, err := userService.GetFirstAdmin(c.Request.Context())
	if err != nil {
		AbortWithError(c, 500, "INTERNAL_ERROR", "No admin user found")
		return false
	}

	authctx.SetPrincipal(c, identity.Principal{UserID: admin.ID, Role: admin.Role, SessionID: "", CredentialKind: "admin_api_key"}, admin.Concurrency, admin.Email)
	c.Set("auth_method", "admin_api_key")
	return true
}

// ValidateJWTForAdmin validates the token, current user, session binding, and administrator role.
func ValidateJWTForAdmin(
	c *gin.Context,
	token string,
	authService SessionAuth,
	userService AdminUserReader,
	settingService AuthSettings,
	auditService AuthObserver,
) bool {
	claims, err := authService.ValidateToken(token)
	if err != nil {
		if errors.Is(err, identity.ErrTokenExpired) {
			AbortWithError(c, 401, "TOKEN_EXPIRED", "Token has expired")
			return false
		}
		AbortWithError(c, 401, "INVALID_TOKEN", "Invalid token")
		return false
	}

	// Read current user state before granting access.
	user, err := userService.GetByID(c.Request.Context(), claims.UserID)
	if err != nil {
		AbortWithError(c, 401, "USER_NOT_FOUND", "User not found")
		return false
	}

	if !user.IsActive() {
		AbortWithError(c, 401, "USER_INACTIVE", "User account is not active")
		return false
	}

	// Reject tokens whose password-derived version is stale.
	if claims.TokenVersion != user.TokenVersion {
		AbortWithError(c, 401, "TOKEN_REVOKED", "Token has been revoked (password changed)")
		return false
	}

	// Enforce configured IP and User-Agent binding before granting access.
	if !EnforceSessionBinding(c, authService, settingService, auditService, claims) {
		return false
	}

	if !user.IsAdmin() {
		AbortWithError(c, 403, "FORBIDDEN", "Admin access required")
		return false
	}

	authctx.SetPrincipal(c, identity.Principal{UserID: user.ID, Role: user.Role, SessionID: claims.SessionID, CredentialKind: "jwt"}, user.Concurrency, user.Email)
	c.Set("auth_method", "jwt")

	return true
}
