package httpapi

import (
	"crypto/subtle"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
)

// admin_auth.go authenticates administrator requests using management keys or
// current-account JWTs.
// Administrator requests can authenticate with a configured management key or
// a JWT tied to a current administrator account. Browser WebSocket upgrades
// first read a jwt. subprotocol entry; other requests prefer x-api-key over
// Authorization, and a rejected supplied credential stops that request.
//
// JWT admission reloads user status and credential version, applies optional
// session binding, and then checks the administrator role. Management-key
// admission compares the configured secret and resolves the first
// administrator account for Gin context. The recorded credential kind lets
// sensitive-operation step-up refuse management keys, whose principal has no
// refresh-family session ID.

// AdminAuth builds middleware for administrator credentials in WebSocket
// subprotocols, x-api-key, or Bearer headers. Supply authentication,
// current-user, and settings readers before serving requests.
//
// A nonempty JWT subprotocol entry on an upgrade takes precedence over both
// headers. Otherwise a supplied management key takes precedence over the
// Bearer token; rejection does not try the lower-priority credential. Accepted
// validators populate Gin authentication data before downstream handlers run.
// Audit recording is optional and participates in JWT binding rejection.
func AdminAuth(
	authService SessionAuth,
	userService AdminUserReader,
	settingService AuthSettings,
	auditService AuthObserver,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Browser WebSockets carry the administrator JWT in a separate subprotocol
		// item. For example: Sec-WebSocket-Protocol: tokenrouter-admin, jwt.<token>.
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

// IsWebSocketUpgradeRequest reports whether headers resemble a WebSocket
// upgrade request. Nil contexts or requests return false; Upgrade is trimmed
// and compared case-insensitively, and Connection is checked for an upgrade
// substring.
func IsWebSocketUpgradeRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	// Browser upgrade requests use the same administrator credential validator.
	// These header checks choose the subprotocol credential path before ordinary
	// key and Bearer-header selection.
	upgrade := strings.ToLower(strings.TrimSpace(c.GetHeader("Upgrade")))
	if upgrade != "websocket" {
		return false
	}
	connection := strings.ToLower(c.GetHeader("Connection"))
	return strings.Contains(connection, "upgrade")
}

// ExtractJWTFromWebSocketSubprotocol returns the first nonempty credential in
// a case-sensitive jwt. subprotocol entry. It trims comma-separated entries
// and their credentials; missing headers or a nil context return an empty
// string.
func ExtractJWTFromWebSocketSubprotocol(c *gin.Context) string {
	if c == nil {
		return ""
	}
	raw := strings.TrimSpace(c.GetHeader("Sec-WebSocket-Protocol"))
	if raw == "" {
		return ""
	}

	// Browsers can supply credentials through WebSocket subprotocol entries. Read
	// the separate jwt. entry so the application protocol name can be negotiated
	// independently of the token.
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

// ValidateAdminAPIKey compares the supplied credential with runtime settings
// and records the first administrator as an API-key principal. False means an
// error response was written and Gin aborted; this path leaves the session ID
// empty and performs no separate user-status or binding check.
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

	// An absent configured secret and an unequal supplied credential share the
	// same rejection, so key admission requires a stored matching value.
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

// ValidateJWTForAdmin verifies access claims against the current active
// administrator and configured session binding. False means the request was
// aborted with its authentication error; success records a JWT principal and
// auth_method before the caller runs downstream handlers.
func ValidateJWTForAdmin(
	c *gin.Context,
	token string,
	authService SessionAuth,
	userService AdminUserReader,
	settingService AuthSettings,
	auditService AuthObserver,
) bool {
	claims, user, ok := validateCurrentJWT(c, token, authService, userService, settingService, auditService)
	if !ok {
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
