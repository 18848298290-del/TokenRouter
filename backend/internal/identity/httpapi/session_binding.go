package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	ip "github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// SessionBindingContext stores the resolved client IP and normalized User-Agent
// in request context for token issuance, binding checks, and audit records.
// It shares forwarding settings with API key IP restrictions.
// When forwarding is enabled, configured headers supply the address;
// otherwise the Gin trusted-proxy chain resolves it.
func SessionBindingContext(settings func() ForwardedIPSettings) gin.HandlerFunc {
	return func(c *gin.Context) {
		forwardedIPSettings := settings()
		ip.SetForwardedIPSettings(c, forwardedIPSettings.TrustForwardedIP, forwardedIPSettings.Headers)
		userAgent := normalizePersistentText(c.Request.UserAgent(), authctx.MaxPersistentUserAgentBytes)
		c.Request.Header.Set("User-Agent", userAgent)
		binding := &identity.SessionBinding{
			IP:        ip.GetSecurityClientIP(c, forwardedIPSettings.TrustForwardedIP),
			UserAgent: userAgent,
		}
		c.Request = c.Request.WithContext(identity.WithSessionBinding(c.Request.Context(), binding))
		c.Next()
	}
}

// RequestSessionBinding returns injected binding data so checks match token issuance.
// Without injected data, it uses the trusted-proxy address and normalized User-Agent.
func RequestSessionBinding(c *gin.Context) *identity.SessionBinding {
	if binding := identity.SessionBindingFromContext(c.Request.Context()); binding != nil {
		return binding
	}
	return &identity.SessionBinding{
		IP:        ip.GetTrustedClientIP(c),
		UserAgent: normalizePersistentText(c.Request.UserAgent(), authctx.MaxPersistentUserAgentBytes),
	}
}

// SecurityClientIP returns the address used for security-sensitive records.
// It prefers injected session-binding data, then uses the trusted-proxy chain.
func SecurityClientIP(c *gin.Context) string {
	if binding := identity.SessionBindingFromContext(c.Request.Context()); binding != nil &&
		strings.TrimSpace(binding.IP) != "" {
		return binding.IP
	}
	return ip.GetTrustedClientIP(c)
}

// EnforceSessionBinding checks an access token's IP/User-Agent fingerprint when enabled.
// A mismatch attempts family revocation, records an audit event when configured, and returns 401.
// False means the request was aborted.
//
// Tokens with no binding hash are accepted; a later refresh can add the binding.
// An empty current fingerprint also bypasses the comparison.
func EnforceSessionBinding(
	c *gin.Context,
	authService SessionAuth,
	settingService BindingSettings,
	auditService AuthObserver,
	claims *identity.JWTClaims,
) bool {
	if settingService == nil || !settingService.IsSessionBindingEnabled(c.Request.Context()) {
		return true
	}
	if claims == nil || claims.BindingHash == "" {
		return true
	}
	binding := RequestSessionBinding(c)
	current := binding.Hash()
	if current == "" || current == claims.BindingHash {
		return true
	}

	if authService != nil {
		_ = authService.RevokeSessionFamily(c.Request.Context(), claims.SessionID)
	}
	if auditService != nil {
		uid := claims.UserID
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		auditService.RecordBindingMismatch(c.Request.Context(), BindingMismatchEvent{UserID: uid, Email: claims.Email, Role: claims.Role, Method: c.Request.Method, Path: path, ClientIP: binding.IP, UserAgent: normalizePersistentText(c.Request.UserAgent(), authctx.MaxPersistentUserAgentBytes)})
	}
	AbortWithError(c, 401, "SESSION_BINDING_MISMATCH", "Session network fingerprint changed, please login again")
	return false
}
