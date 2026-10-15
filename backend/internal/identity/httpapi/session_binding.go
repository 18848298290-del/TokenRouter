package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	ip "github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// session_binding.go attaches request binding observations and enforces
// access-token binding at HTTP authentication.
// Access-token binding depends on the client address and User-Agent used
// during issuance. Request middleware resolves the security address once and
// normalizes the header before adding a SessionBinding, so later issuance,
// verification, and audit records can read the same request values.
//
// Forwarded-address settings come from the gateway IP-restriction
// configuration. Readers use the injected binding when present and the
// trusted-proxy address otherwise. Binding enforcement accepts tokens with no
// stored digest or no current digest, and rejects differing nonempty digests
// after attempting refresh-family revocation. Revocation failure does not turn
// that rejected request into an authenticated one.

// SessionBindingContext resolves client-address settings and injects
// normalized binding data before downstream handlers run. Supply a nonnil
// settings callback; it is evaluated for each request.
//
// The normalized User-Agent replaces the request header as well as entering
// the binding value, so issuance and audit consumers see the same bounded
// text. Security IP resolution uses configured forwarding headers when enabled
// and the trusted-proxy path otherwise. The request context carries these
// values through authentication and token generation.
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

// RequestSessionBinding returns the binding injected into this request when
// available. Otherwise it constructs a binding from the trusted-proxy address
// and normalized User-Agent; callers receive the injected pointer directly.
func RequestSessionBinding(c *gin.Context) *identity.SessionBinding {
	if binding := identity.SessionBindingFromContext(c.Request.Context()); binding != nil {
		return binding
	}
	// Direct handler calls have no captured forwarding policy. Use Gin's trusted
	// proxy resolution so an unconfigured raw forwarding header cannot supply
	// this binding's address.
	return &identity.SessionBinding{
		IP:        ip.GetTrustedClientIP(c),
		UserAgent: normalizePersistentText(c.Request.UserAgent(), authctx.MaxPersistentUserAgentBytes),
	}
}

// SecurityClientIP returns the address used by authentication and security
// records. It uses a nonblank injected binding address, then the trusted-proxy
// resolver when the binding is absent or its address is blank.
func SecurityClientIP(c *gin.Context) string {
	if binding := identity.SessionBindingFromContext(c.Request.Context()); binding != nil &&
		strings.TrimSpace(binding.IP) != "" {
		return binding.IP
	}
	// An absent or blank captured address cannot identify the request for security
	// records, so Gin's configured proxy resolution supplies the usable address.
	return ip.GetTrustedClientIP(c)
}

// EnforceSessionBinding reports whether an access token passes the enabled
// request-binding check. Disabled settings, missing claims or stored hashes,
// and an empty current hash pass; a mismatch writes a 401 response and returns
// false.
//
// On mismatch it attempts family revocation and optionally records the user
// and request details for audit. The caller stops processing when false is
// returned. Family-revocation errors are discarded, so the current request
// remains denied even when session cleanup fails.
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

	// Deny this request independently of refresh-family cleanup; an unavailable
	// revocation path leaves the mismatched access token rejected here.
	if authService != nil {
		_ = authService.RevokeSessionFamily(c.Request.Context(), claims.SessionID)
	}
	if auditService != nil {
		uid := claims.UserID
		path := c.FullPath()
		// Unmatched routes have no registered pattern, so retain the actual URL path
		// to keep the binding-mismatch audit attributable to this request.
		if path == "" {
			path = c.Request.URL.Path
		}
		auditService.RecordBindingMismatch(c.Request.Context(), BindingMismatchEvent{UserID: uid, Email: claims.Email, Role: claims.Role, Method: c.Request.Method, Path: path, ClientIP: binding.IP, UserAgent: normalizePersistentText(c.Request.UserAgent(), authctx.MaxPersistentUserAgentBytes)})
	}
	AbortWithError(c, 401, "SESSION_BINDING_MISMATCH", "Session network fingerprint changed, please login again")
	return false
}
