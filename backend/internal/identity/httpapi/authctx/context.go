package authctx

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// A Gin request can carry an authenticated caller and a different paying user.
// The private authentication record keeps those identities together with the
// role and concurrency already resolved by the middleware, so later
// compatibility-field writes cannot replace the record used by authentication
// readers.
//
// JWT middleware sets the principal and compatibility fields from the same
// current user. API Key middleware first sets the paying user and role, then
// records the caller principal alongside those values. Readers prefer a valid
// private record and use the public Gin fields only when that record is absent
// or has the wrong type.

// Context keys identify middleware values shared with Gin callers; the byte
// limit bounds User-Agent text persisted with sessions and audit events.
const (
	ContextKeyUser              = "user"               // compatibility user and concurrency
	ContextKeyUserRole          = "user_role"          // compatibility account role
	ContextKeyAuthEmail         = "auth_email"         // authenticated account email
	ContextKeySessionID         = "session_id"         // refresh token family
	principalKey                = "identity_principal" // private authentication record
	MaxPersistentUserAgentBytes = 512                  // bytes
)

// AuthSubject holds the account and concurrency allowance used by Gin
// consumers.
type AuthSubject struct {
	UserID      int64 // resolved account identifier
	Concurrency int   // account concurrency allowance
}

// authenticationRecord snapshots the principal and the compatibility values
// read by authentication helpers.
type authenticationRecord struct {
	// Principal identifies the caller. Subject and Role retain the middleware
	// account values; HasSubject and HasRole report successful typed reads, so a
	// present record with missing values suppresses compatibility fallback.
	Principal  identity.Principal // authenticated caller identity
	Subject    AuthSubject        // resolved account context
	HasSubject bool               // subject read success
	Role       string             // resolved account role
	HasRole    bool               // role read success
}

// SetPrincipal records a verified panel identity and publishes its user, role,
// email, and session values for Gin consumers. Concurrency comes from the
// current user loaded by authentication middleware.
//
// The private record and compatibility fields start with the same account
// values, while later reads retain the private record if other middleware
// changes compatibility fields. Request telemetry receives the principal user
// ID when a request is available; callers supply an initialized Gin context.
func SetPrincipal(c *gin.Context, p identity.Principal, concurrency int, email string) {
	if c.Request != nil {
		telemetry.UpdateRequest(c.Request.Context(), func(record *telemetry.RequestRecord) { record.UserID = p.UserID })
	}
	subject := AuthSubject{UserID: p.UserID, Concurrency: concurrency}
	c.Set(principalKey, authenticationRecord{Principal: p, Subject: subject, HasSubject: true, Role: p.Role, HasRole: true})
	c.Set(ContextKeyUser, subject)
	c.Set(ContextKeyUserRole, p.Role)
	c.Set(ContextKeyAuthEmail, email)
	c.Set(ContextKeySessionID, p.SessionID)
}

// recordFromContext reads the typed private authentication record from Gin.
// Missing values and values of another type return false, allowing
// compatibility readers to inspect their public context fields.
func recordFromContext(c *gin.Context) (authenticationRecord, bool) {
	v, ok := c.Get(principalKey)
	if !ok {
		return authenticationRecord{}, false
	}
	r, ok := v.(authenticationRecord)
	return r, ok
}

// GetPrincipal returns the caller identity stored in the private
// authentication record. The boolean reports whether that record exists with
// the expected type; compatibility user fields alone do not establish a
// principal.
func GetPrincipal(c *gin.Context) (identity.Principal, bool) {
	r, ok := recordFromContext(c)
	return r.Principal, ok
}

// GetAuthSubjectFromContext returns the account and concurrency resolved for
// Gin consumers. A valid private record takes precedence, including its
// HasSubject flag; otherwise the public user field is checked for the expected
// type.
func GetAuthSubjectFromContext(c *gin.Context) (AuthSubject, bool) {
	if r, ok := recordFromContext(c); ok {
		return r.Subject, r.HasSubject
	}
	// A usable private record was absent, so read the typed compatibility value
	// installed by older middleware callers.
	v, ok := c.Get(ContextKeyUser)
	if !ok {
		return AuthSubject{}, false
	}
	s, ok := v.(AuthSubject)
	return s, ok
}

// GetUserRoleFromContext returns the role resolved for Gin consumers and its
// availability. A valid private record takes precedence, including HasRole;
// otherwise the public role field is checked for a string.
func GetUserRoleFromContext(c *gin.Context) (string, bool) {
	if r, ok := recordFromContext(c); ok {
		return r.Role, r.HasRole
	}
	// A usable private record was absent, so read the typed compatibility value
	// installed by older middleware callers.
	v, ok := c.Get(ContextKeyUserRole)
	if !ok {
		return "", false
	}
	role, ok := v.(string)
	return role, ok
}

// SetAuthenticatedPrincipal records a verified API Key caller beside the
// account values already resolved by gateway middleware. Callers populate the
// public user and role fields before invoking it.
//
// The caller may differ from the payer retained in Subject, and Principal.Role
// may be empty while the payer has an administrator role. Typed-read flags
// capture whether those compatibility values were usable at this point. Later
// compatibility writes leave this snapshot available to authentication
// readers.
func SetAuthenticatedPrincipal(c *gin.Context, p identity.Principal) {
	if c.Request != nil {
		telemetry.UpdateRequest(c.Request.Context(), func(record *telemetry.RequestRecord) { record.UserID = p.UserID })
	}
	record := authenticationRecord{Principal: p}
	if v, ok := c.Get(ContextKeyUser); ok {
		record.Subject, record.HasSubject = v.(AuthSubject)
	}
	if v, ok := c.Get(ContextKeyUserRole); ok {
		record.Role, record.HasRole = v.(string)
	}
	c.Set(principalKey, record)
}
