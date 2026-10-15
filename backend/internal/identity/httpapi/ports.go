package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ports.go defines the account, settings, session, and observation interfaces
// consumed by authentication middleware.
// Authentication middleware reads current account and runtime settings through
// small caller interfaces. Binding-mismatch events carry the verified user
// details together with the observed request, while distinct Gin middleware
// types let application assembly select JWT, administrator, and step-up
// checks.

// UserReader loads the current account consulted by authentication middleware.
type UserReader interface {
	// GetByID returns current user state for the supplied account ID.
	// Authentication callers interpret lookup errors as rejection and compare the
	// loaded status and credential version with token claims.
	GetByID(context.Context, int64) (*identity.User, error)
}

// AdminUserReader also resolves the administrator represented by a configured
// management key.
type AdminUserReader interface {
	UserReader // current account lookup
	// GetFirstAdmin returns the account represented by the configured
	// administrator key. Callers treat lookup errors as an unavailable
	// administrator and use the returned role and concurrency for Gin
	// authentication.
	GetFirstAdmin(context.Context) (*identity.User, error)
}

// ActivityToucher records successful panel authentication for account activity
// summaries.
type ActivityToucher interface {
	// TouchLastActiveForUser records panel activity after the request has passed
	// authentication checks. Middleware calls it with the loaded user before
	// allowing downstream handlers to run.
	TouchLastActiveForUser(context.Context, *identity.User)
}

// SessionAuth validates access tokens and requests refresh-family revocation
// for authentication.
type SessionAuth interface {
	// ValidateToken returns signed access claims or an error for an unusable
	// token. HTTP callers distinguish expiry errors, then load the current
	// account before admitting the request.
	ValidateToken(string) (*identity.JWTClaims, error)
	// RevokeSessionFamily requests removal of refresh tokens belonging to the
	// supplied session family. Binding-mismatch callers discard cleanup errors
	// and still reject the current access-token request.
	RevokeSessionFamily(context.Context, string) error
}
type (
	// BindingSettings supplies the runtime switch for access-token network
	// binding.
	BindingSettings interface {
		// IsSessionBindingEnabled reports whether access-token request fingerprints
		// are checked.
		IsSessionBindingEnabled(context.Context) bool
	}
	// AuthSettings combines binding configuration with the stored administrator
	// management credential.
	AuthSettings interface {
		BindingSettings // network binding switch
		// GetAdminAPIKey returns the configured administrator credential for exact
		// comparison. Authentication treats an empty value as a mismatch and a read
		// error as an internal failure.
		GetAdminAPIKey(context.Context) (string, error)
	}
)

type (
	// StepUpSettingReader supplies the runtime switch for sensitive-operation
	// TOTP checks.
	StepUpSettingReader interface {
		// IsStepUpEnabled reports whether sensitive operations require recent TOTP
		// verification.
		IsStepUpEnabled(context.Context) bool
	}
	// StepUpGrantChecker reads recent TOTP verification for one user and session.
	StepUpGrantChecker interface {
		// HasStepUpGrant reports whether recent TOTP verification covers this user
		// and session key. Sensitive-operation middleware denies missing grants and
		// returns service unavailable when this read fails.
		HasStepUpGrant(context.Context, int64, string) (bool, error)
	}
)

// BindingMismatchEvent contains the verified identity and observed request
// rejected by binding.
type BindingMismatchEvent struct {
	UserID int64 // verified account identifier
	// Email is the token email, and Role is its role. Method names the HTTP
	// method, Path is the registered route or URL path, ClientIP is the binding
	// address, and UserAgent is the normalized request header recorded at
	// rejection.
	Email, Role, Method, Path, ClientIP, UserAgent string
}

// AuthObserver records request-binding failures using the supplied verified
// identity details.
type AuthObserver interface {
	// RecordBindingMismatch records the supplied identity and request after a
	// token binding mismatch. Middleware invokes it when configured and denies
	// the request independently of audit delivery.
	RecordBindingMismatch(context.Context, BindingMismatchEvent)
}

// ForwardedIPSettings selects the forwarding sources used for request security
// addresses.
type ForwardedIPSettings struct {
	TrustForwardedIP bool // forwarding-header trust switch
	// Headers selects custom forwarding headers for security IP resolution.
	// SessionBindingContext captures these request settings before authentication
	// consumers run.
	Headers []string
}
type (
	// JWTAuthMiddleware identifies Gin middleware validating panel JWT
	// authentication.
	JWTAuthMiddleware gin.HandlerFunc
	// AdminAuthMiddleware identifies Gin middleware validating administrator
	// credentials and current roles.
	AdminAuthMiddleware gin.HandlerFunc
	// StepUpAuthMiddleware identifies Gin middleware requiring recent
	// sensitive-operation verification.
	StepUpAuthMiddleware gin.HandlerFunc
)

// AbortWithError writes the panel error envelope and aborts Gin processing.
// The supplied status, code, and message pass through to the shared HTTP
// helper for authentication failures.
func AbortWithError(c *gin.Context, status int, code, message string) {
	httpx.AbortWithError(c, status, code, message)
}

// normalizePersistentText returns trimmed valid UTF-8 capped by n bytes.
// Nonpositive n disables the cap; a positive cap backs off partial runes and
// can expose whitespace at the shortened tail.
func normalizePersistentText(v string, n int) string { return httpx.NormalizePersistentText(v, n) }
