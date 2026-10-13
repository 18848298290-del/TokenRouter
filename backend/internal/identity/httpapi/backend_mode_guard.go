package httpapi

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Backend mode keeps administrator login continuations available while
// disabling user self-service. Auth-route filtering permits selected login and
// pending paths; user-route filtering relies on JWT middleware having already
// resolved the role, and handlers enforce the remaining account-specific
// rules.

// BackendModeReader supplies the runtime switch controlling panel self-service
// access.
type BackendModeReader interface {
	// IsBackendModeEnabled reports whether the runtime panel restricts user
	// self-service.
	IsBackendModeEnabled(context.Context) bool
}

// BackendModeUserGuard rejects self-service requests from nonadministrators
// while backend mode is enabled. Register it after JWT authentication; a nil
// settings reader permits requests, and an available administrator role
// bypasses the restriction.
func BackendModeUserGuard(settingService BackendModeReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		if settingService == nil || !settingService.IsBackendModeEnabled(c.Request.Context()) {
			c.Next()
			return
		}
		role, _ := authctx.GetUserRoleFromContext(c)
		if role == "admin" {
			c.Next()
			return
		}
		response.Forbidden(c, "Backend mode is active. User self-service is disabled.")
		c.Abort()
	}
}

// backendModeAllowsAuthPath reports whether a normalized URL path can continue
// authentication in backend mode. Matching uses suffixes and a pending-path
// substring, so callers remain responsible for registering the actual
// supported endpoints.
func backendModeAllowsAuthPath(path string) bool {
	// Compare route suffixes independently of a mounting prefix; normalized case
	// and whitespace keep the guard comparison independent of the exact URL spelling.
	path = strings.ToLower(strings.TrimSpace(path))
	for _, suffix := range []string{
		"/auth/login",
		"/auth/login/2fa",
		"/auth/passkey/login/begin",
		"/auth/passkey/login/finish",
		"/auth/logout",
		"/auth/refresh",
	} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}

	for _, suffix := range []string{
		"/auth/oauth/linuxdo/callback",
		"/auth/oauth/wechat/callback",
		"/auth/oauth/wechat/payment/callback",
		"/auth/oauth/oidc/callback",
		"/auth/oauth/github/callback",
		"/auth/oauth/google/callback",
		"/auth/oauth/dingtalk/callback",
		"/auth/oauth/linuxdo/complete-registration",
		"/auth/oauth/wechat/complete-registration",
		"/auth/oauth/oidc/complete-registration",
		"/auth/oauth/github/complete-registration",
		"/auth/oauth/google/complete-registration",
		"/auth/oauth/dingtalk/complete-registration",
		"/auth/oauth/linuxdo/create-account",
		"/auth/oauth/wechat/create-account",
		"/auth/oauth/oidc/create-account",
		"/auth/oauth/dingtalk/create-account",
		"/auth/oauth/linuxdo/bind-login",
		"/auth/oauth/wechat/bind-login",
		"/auth/oauth/oidc/bind-login",
		"/auth/oauth/dingtalk/bind-login",
	} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}

	return strings.Contains(path, "/auth/oauth/pending/")
}

// BackendModeAuthGuard restricts public authentication routes to the
// backend-mode continuation allowlist. A nil settings reader permits requests;
// accepted callbacks and pending paths still rely on their handlers to reject
// nonadministrator login and self-service registration.
func BackendModeAuthGuard(settingService BackendModeReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		if settingService == nil || !settingService.IsBackendModeEnabled(c.Request.Context()) {
			c.Next()
			return
		}
		if backendModeAllowsAuthPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		response.Forbidden(c, "Backend mode is active. Registration and self-service auth flows are disabled.")
		c.Abort()
	}
}
