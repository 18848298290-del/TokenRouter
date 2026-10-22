package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// jwt_validation_scenario_test.go exercises jwt_auth.go and admin_auth.go through
// their Gin middleware entrypoints. Ordered observations cover token validation,
// current-account reads, session_binding.go rejection effects, and publication
// before panel activity or protected handlers can consume the authenticated user.

// jwtValidationProbe records dependency calls and supplies account validation
// outcomes to the panel and administrator middleware scenario tests.
type jwtValidationProbe struct {
	t              *testing.T          // scenario assertion owner
	c              *gin.Context        // current middleware context
	claims         *identity.JWTClaims // returned signed claims
	tokenErr       error               // token validation error
	user           *identity.User      // returned current account
	userErr        error               // current account error
	bindingEnabled bool                // configured binding switch
	revokeErr      error               // family cleanup error
	events         []string            // ordered dependency calls
	noSettings     bool                // omit settings dependency
	noAudit        bool                // omit audit observer
	noActivity     bool                // omit activity recorder
	emptyBinding   bool                // inject empty fingerprint
}

// TestJWTValidationMiddlewareMatrix drives common account failures through all
// three JWT entrypoints. It protects error precedence, dependency read counts,
// binding cleanup, and identity publication before entrypoint-specific effects.
func TestJWTValidationMiddlewareMatrix(t *testing.T) {
	for _, entry := range []string{"panel", "admin_bearer", "admin_websocket"} {
		t.Run(entry, func(t *testing.T) {
			for _, tc := range []struct {
				name           string                    // validation scenario name
				configure      func(*jwtValidationProbe) // fixture outcome changes
				code           string                    // expected rejection code
				message        string                    // expected rejection message
				events         []string                  // expected dependency sequence
				panics         bool                      // dependency violates result assumptions
				adminForbidden bool                      // reject administrator role check
			}{
				{
					name: "invalid_nil_claims",
					configure: func(p *jwtValidationProbe) {
						p.claims = nil
						p.tokenErr = errors.New("invalid signature")
					},
					code: "INVALID_TOKEN", message: "Invalid token",
					events: []string{"validate"},
				},
				{
					name: "invalid_with_claims",
					configure: func(p *jwtValidationProbe) {
						p.tokenErr = errors.New("invalid signature")
					},
					code: "INVALID_TOKEN", message: "Invalid token",
					events: []string{"validate"},
				},
				{
					name: "wrapped_expiry_precedes_account_failure",
					configure: func(p *jwtValidationProbe) {
						p.tokenErr = fmt.Errorf("validation: %w", identity.ErrTokenExpired)
						p.userErr = errors.New("account unavailable")
					},
					code: "TOKEN_EXPIRED", message: "Token has expired",
					events: []string{"validate"},
				},
				{
					name: "missing_user",
					configure: func(p *jwtValidationProbe) {
						p.user = nil
						p.userErr = identity.ErrUserNotFound
					},
					code: "USER_NOT_FOUND", message: "User not found",
					events: []string{"validate", "user"},
				},
				{
					name: "user_error_precedes_returned_account",
					configure: func(p *jwtValidationProbe) {
						p.userErr = errors.New("account unavailable")
						p.user.Status = "disabled"
					},
					code: "USER_NOT_FOUND", message: "User not found",
					events: []string{"validate", "user"},
				},
				{
					name: "inactive_precedes_version_binding_and_role",
					configure: func(p *jwtValidationProbe) {
						p.user.Status = "disabled"
						p.user.TokenVersion++
						p.user.Role = identity.RoleUser
						p.claims.BindingHash = "other-network"
					},
					code: "USER_INACTIVE", message: "User account is not active",
					events: []string{"validate", "user"},
				},
				{
					name: "version_precedes_binding_and_role",
					configure: func(p *jwtValidationProbe) {
						p.user.TokenVersion++
						p.user.Role = identity.RoleUser
						p.claims.BindingHash = "other-network"
					},
					code: "TOKEN_REVOKED", message: "Token has been revoked (password changed)",
					events: []string{"validate", "user"},
				},
				{
					name: "binding_precedes_role",
					configure: func(p *jwtValidationProbe) {
						p.user.Role = identity.RoleUser
						p.claims.BindingHash = "other-network"
					},
					code: "SESSION_BINDING_MISMATCH", message: "Session network fingerprint changed, please login again",
					events: []string{"validate", "user", "binding", "revoke:session-7", "audit"},
				},
				{
					name: "binding_cleanup_error_and_empty_session",
					configure: func(p *jwtValidationProbe) {
						p.claims.BindingHash = "other-network"
						p.claims.SessionID = ""
						p.revokeErr = errors.New("cleanup unavailable")
					},
					code: "SESSION_BINDING_MISMATCH", message: "Session network fingerprint changed, please login again",
					events: []string{"validate", "user", "binding", "revoke:", "audit"},
				},
				{
					name: "binding_without_audit",
					configure: func(p *jwtValidationProbe) {
						p.claims.BindingHash = "other-network"
						p.noAudit = true
					},
					code: "SESSION_BINDING_MISMATCH", message: "Session network fingerprint changed, please login again",
					events: []string{"validate", "user", "binding", "revoke:session-7"},
				},
				{
					name: "current_nonadmin_role",
					configure: func(p *jwtValidationProbe) {
						p.claims.Role = identity.RoleAdmin
						p.user.Role = identity.RoleUser
					},
					events: []string{"validate", "user", "binding"}, adminForbidden: true,
				},
				{
					name:   "matching_binding_current_account",
					events: []string{"validate", "user", "binding"},
				},
				{
					name:      "legacy_empty_binding_hash",
					configure: func(p *jwtValidationProbe) { p.claims.BindingHash = "" },
					events:    []string{"validate", "user", "binding"},
				},
				{
					name: "disabled_binding",
					configure: func(p *jwtValidationProbe) {
						p.bindingEnabled = false
						p.claims.BindingHash = "other-network"
					},
					events: []string{"validate", "user", "binding"},
				},
				{
					name: "nil_binding_settings",
					configure: func(p *jwtValidationProbe) {
						p.noSettings = true
						p.claims.BindingHash = "other-network"
					},
					events: []string{"validate", "user"},
				},
				{
					name:      "empty_request_binding",
					configure: func(p *jwtValidationProbe) { p.emptyBinding = true },
					events:    []string{"validate", "user", "binding"},
				},
				{
					name:      "nil_activity_recorder",
					configure: func(p *jwtValidationProbe) { p.noActivity = true },
					events:    []string{"validate", "user", "binding"},
				},
				{
					name:      "nil_claims_without_error",
					configure: func(p *jwtValidationProbe) { p.claims = nil },
					events:    []string{"validate"}, panics: true,
				},
				{
					name:      "nil_user_without_error",
					configure: func(p *jwtValidationProbe) { p.user = nil },
					events:    []string{"validate", "user"}, panics: true,
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					binding := &identity.SessionBinding{IP: "192.0.2.10", UserAgent: "matrix-agent"}
					probe := &jwtValidationProbe{
						t: t,
						claims: &identity.JWTClaims{
							UserID: 7, Email: "signed@example.com", Role: identity.RoleUser,
							TokenVersion: 4, SessionID: "session-7", BindingHash: binding.Hash(),
						},
						user: &identity.User{
							ID: 7, Email: "current@example.com", Role: identity.RoleAdmin,
							Status: identity.StatusActive, TokenVersion: 4, Concurrency: 9,
						},
						bindingEnabled: true,
					}
					if tc.configure != nil {
						tc.configure(probe)
					}
					if probe.emptyBinding {
						binding = &identity.SessionBinding{}
					}
					var settings identityhttp.AuthSettings = probe
					if probe.noSettings {
						settings = nil
					}
					var audit identityhttp.AuthObserver = probe
					if probe.noAudit {
						audit = nil
					}
					var activity identityhttp.ActivityToucher = probe
					if probe.noActivity {
						activity = nil
					}

					router := gin.New()
					router.Use(func(c *gin.Context) {
						probe.c = c
						c.Request = c.Request.WithContext(identity.WithSessionBinding(c.Request.Context(), binding))
						c.Next()
					})
					if entry == "panel" {
						router.Use(identityhttp.JWTAuth(probe, probe, activity, settings, audit))
					} else {
						router.Use(identityhttp.AdminAuth(probe, probe, settings, audit))
					}
					router.GET("/protected/:id", func(c *gin.Context) {
						probe.assertPublished()
						probe.events = append(probe.events, "handler")
						c.Status(http.StatusNoContent)
					})
					req := httptest.NewRequest(http.MethodGet, "/protected/7", nil)
					req.Header.Set("User-Agent", "matrix-agent")
					if entry == "admin_websocket" {
						req.Header.Set("Upgrade", "websocket")
						req.Header.Set("Connection", "Upgrade")
						req.Header.Set("Sec-WebSocket-Protocol", "tokenrouter-admin, jwt. access-token ")
						req.Header.Set("x-api-key", "lower-priority-key")
						req.Header.Set("Authorization", "Bearer lower-priority-token")
					} else {
						req.Header.Set("Authorization", "bEaReR  access-token ")
					}
					response := httptest.NewRecorder()
					if tc.panics {
						require.Panics(t, func() { router.ServeHTTP(response, req) })
					} else {
						router.ServeHTTP(response, req)
					}

					code, message := tc.code, tc.message
					status := http.StatusUnauthorized
					if tc.adminForbidden && entry != "panel" {
						code, message = "FORBIDDEN", "Admin access required"
						status = http.StatusForbidden
					}
					wantEvents := append([]string(nil), tc.events...)
					if code == "" && !tc.panics {
						require.Equal(t, http.StatusNoContent, response.Code)
						require.False(t, probe.c.IsAborted())
						if entry == "panel" && !probe.noActivity {
							wantEvents = append(wantEvents, "touch")
						}
						wantEvents = append(wantEvents, "handler")
						method, present := probe.c.Get("auth_method")
						if entry == "panel" {
							require.False(t, present)
						} else {
							require.True(t, present)
							require.Equal(t, "jwt", method)
						}
					} else {
						probe.assertUnpublished()
						if !tc.panics {
							require.Equal(t, status, response.Code)
							require.True(t, probe.c.IsAborted())
							var body httpx.ErrorResponse
							require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
							require.Equal(t, code, body.Code)
							require.Equal(t, message, body.Message)
						}
					}
					require.Equal(t, wantEvents, probe.events)
				})
			}
		})
	}
}

// ValidateToken returns configured claims and errors while recording the supplied
// credential. Every call checks that identity publication has not preceded token
// validation, including fixtures returning both claims and an error.
func (p *jwtValidationProbe) ValidateToken(token string) (*identity.JWTClaims, error) {
	p.assertUnpublished()
	require.Equal(p.t, "access-token", token)
	p.events = append(p.events, "validate")
	return p.claims, p.tokenErr
}

// GetByID returns the configured current-account result and records its lookup.
// The supplied account ID must come from validated claims; nonnil errors can
// accompany either nil or nonnil account results.
func (p *jwtValidationProbe) GetByID(_ context.Context, id int64) (*identity.User, error) {
	p.assertUnpublished()
	require.Equal(p.t, p.claims.UserID, id)
	p.events = append(p.events, "user")
	return p.user, p.userErr
}

// GetFirstAdmin panics if a JWT scenario reaches management-key account
// selection. Each scenario supplies a JWT, including WebSocket requests with
// lower-priority key headers, so this call identifies unintended credential
// selection.
func (p *jwtValidationProbe) GetFirstAdmin(context.Context) (*identity.User, error) {
	panic("unexpected management-key account lookup")
}

// GetAdminAPIKey panics if JWT admission reads the management secret. WebSocket
// fixtures also send an x-api-key header; its presence must not trigger this
// read while a JWT subprotocol credential was selected.
func (p *jwtValidationProbe) GetAdminAPIKey(context.Context) (string, error) {
	panic("unexpected management-key settings lookup")
}

// IsSessionBindingEnabled records each settings read before returning the
// switch. It also checks that identity remains unpublished while authentication
// still needs to enforce binding and any administrator role requirement.
func (p *jwtValidationProbe) IsSessionBindingEnabled(context.Context) bool {
	p.assertUnpublished()
	p.events = append(p.events, "binding")
	return p.bindingEnabled
}

// RevokeSessionFamily records the rejected session and returns configured cleanup
// errors. Identity remains unpublished while binding enforcement requests family
// cleanup, including requests whose signed session identifier is empty.
func (p *jwtValidationProbe) RevokeSessionFamily(_ context.Context, sessionID string) error {
	p.assertUnpublished()
	p.events = append(p.events, "revoke:"+sessionID)
	return p.revokeErr
}

// RecordBindingMismatch checks the signed identity and registered route recorded
// for rejection. The current account can carry different email and role values;
// audit data continues to describe the rejected token.
func (p *jwtValidationProbe) RecordBindingMismatch(_ context.Context, event identityhttp.BindingMismatchEvent) {
	p.assertUnpublished()
	require.Equal(p.t, identityhttp.BindingMismatchEvent{
		UserID: p.claims.UserID, Email: p.claims.Email, Role: p.claims.Role,
		Method: http.MethodGet, Path: "/protected/:id", ClientIP: "192.0.2.10", UserAgent: "matrix-agent",
	}, event)
	p.events = append(p.events, "audit")
}

// TouchLastActiveForUser checks that publication precedes panel activity recording.
// It also requires the current account pointer returned by lookup, so middleware
// cannot substitute token fields or another account snapshot.
func (p *jwtValidationProbe) TouchLastActiveForUser(_ context.Context, user *identity.User) {
	p.assertPublished()
	require.Same(p.t, p.user, user)
	p.events = append(p.events, "touch")
}

// assertUnpublished checks that validation has not exposed principal or context
// values. It inspects compatibility fields as well as the private principal so
// rejected requests cannot leave partially published authentication data.
func (p *jwtValidationProbe) assertUnpublished() {
	p.t.Helper()
	_, present := authctx.GetPrincipal(p.c)
	require.False(p.t, present)
	for _, key := range []string{
		authctx.ContextKeyUser, authctx.ContextKeyUserRole,
		authctx.ContextKeyAuthEmail, authctx.ContextKeySessionID, "auth_method",
	} {
		_, present = p.c.Get(key)
		require.False(p.t, present, key)
	}
}

// assertPublished checks principal and compatibility values against the current
// account and signed session. Both panel activity and the protected handler call
// it to protect publication order and the origin of each field.
func (p *jwtValidationProbe) assertPublished() {
	p.t.Helper()
	principal, present := authctx.GetPrincipal(p.c)
	require.True(p.t, present)
	require.Equal(p.t, identity.Principal{
		UserID: p.user.ID, Role: p.user.Role, SessionID: p.claims.SessionID, CredentialKind: "jwt",
	}, principal)
	subject, present := authctx.GetAuthSubjectFromContext(p.c)
	require.True(p.t, present)
	require.Equal(p.t, authctx.AuthSubject{UserID: p.user.ID, Concurrency: p.user.Concurrency}, subject)
	role, present := authctx.GetUserRoleFromContext(p.c)
	require.True(p.t, present)
	require.Equal(p.t, p.user.Role, role)
	require.Equal(p.t, p.user.Email, p.c.GetString(authctx.ContextKeyAuthEmail))
	require.Equal(p.t, p.claims.SessionID, p.c.GetString(authctx.ContextKeySessionID))
}
