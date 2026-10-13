package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/audit"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
)

// Sensitive-operation admission varies with credential kind, current TOTP
// state, and the grant associated with a session key. These fixtures return
// selected settings and lookup results, and the assertions inspect rejection
// responses plus principal precedence over compatibility Gin values.

// stepUpEnabled supplies an active runtime step-up switch shared by the
// admission cases that exercise credential, account, and grant checks.
var stepUpEnabled = stubStepUpSettingReader{enabled: true}

// stubStepUpGrantChecker returns the selected grant availability and lookup
// failure.
type stubStepUpGrantChecker struct {
	granted bool  // configured grant availability flag
	err     error // configured grant lookup failure
}

// stubStepUpUserReader returns the selected current account and lookup
// failure.
type stubStepUpUserReader struct {
	user *identity.User // configured current account pointer
	err  error          // configured account lookup failure
}

// stubStepUpSettingReader supplies the selected runtime switch for
// sensitive-operation checks.
type stubStepUpSettingReader struct {
	enabled bool // configured step-up switch value
}

// principalStepUpEnabled supplies enabled settings for verified-principal
// precedence tests.
type principalStepUpEnabled struct{}

// TestEnforceStepUpRejectsAdminAPIKey supplies an administrator API-key
// credential with otherwise valid TOTP and grant fixtures. It checks an
// aborted HTTP 403 request and the credential-specific rejection code.
func TestEnforceStepUpRejectsAdminAPIKey(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set("auth_method", audit.AuditAuthMethodAdminAPIKey)

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &identity.User{TotpEnabled: true}}, stepUpEnabled)

	require.False(t, ok)
	require.True(t, c.IsAborted())
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_ADMIN_API_KEY_FORBIDDEN")
}

// TestEnforceStepUpRequiresAuthSubject supplies enabled step-up and valid
// lookup fixtures while omitting the Gin user subject. It checks false
// admission and HTTP 401 for the unauthenticated request.
func TestEnforceStepUpRequiresAuthSubject(t *testing.T) {
	c, rec := newStepUpTestContext(t)

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &identity.User{TotpEnabled: true}}, stepUpEnabled)

	require.False(t, ok)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestEnforceStepUpRequiresTotpEnabled supplies an authenticated subject whose
// current account has TOTP disabled. It checks HTTP 403 and
// STEP_UP_TOTP_NOT_ENABLED even when the grant fixture reports success.
func TestEnforceStepUpRequiresTotpEnabled(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &identity.User{ID: 1, TotpEnabled: false}}, stepUpEnabled)

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_TOTP_NOT_ENABLED")
}

// TestEnforceStepUpFailsClosedOnNilUser supplies an authenticated subject with
// no current account returned by the lookup fixture. It checks false admission
// and an internal-error HTTP response.
func TestEnforceStepUpFailsClosedOnNilUser(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{}, stepUpEnabled)

	require.False(t, ok)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

// TestEnforceStepUpFailsClosedOnGrantError supplies an authenticated TOTP
// account while grant lookup reports a Redis-like failure. It checks HTTP 503
// and STEP_UP_UNAVAILABLE instead of accepting the operation.
func TestEnforceStepUpFailsClosedOnGrantError(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{err: errors.New("redis down")}, stubStepUpUserReader{user: &identity.User{ID: 1, TotpEnabled: true}}, stepUpEnabled)

	require.False(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_UNAVAILABLE")
}

// TestEnforceStepUpRequiresGrant supplies an authenticated TOTP account and a
// missing session grant. It checks HTTP 403 and STEP_UP_REQUIRED for the
// sensitive operation request.
func TestEnforceStepUpRequiresGrant(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: false}, stubStepUpUserReader{user: &identity.User{ID: 1, TotpEnabled: true}}, stepUpEnabled)

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_REQUIRED")
}

// TestEnforceStepUpPassesWithGrant supplies an authenticated TOTP account with
// an available session grant. It checks true admission and that Gin processing
// remains un-aborted for the request.
func TestEnforceStepUpPassesWithGrant(t *testing.T) {
	c, _ := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &identity.User{ID: 1, TotpEnabled: true}}, stepUpEnabled)

	require.True(t, ok)
	require.False(t, c.IsAborted())
}

// TestStepUpSessionKeyUsesSessionID supplies a compatibility refresh-family ID
// with no private principal. It checks that this value is selected directly as
// the session key.
func TestStepUpSessionKeyUsesSessionID(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Set(authctx.ContextKeySessionID, "family-1")
	require.Equal(t, "family-1", StepUpSessionKey(c, 42))
}

// TestStepUpSessionKeySeparatesLegacyTokens supplies two different legacy
// Bearer credentials for one user. It checks stable keys for repeated
// credentials and different keys for the two token texts.
func TestStepUpSessionKeySeparatesLegacyTokens(t *testing.T) {
	keyFor := func(token string) string {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.Header.Set("Authorization", "Bearer "+token)
		return StepUpSessionKey(c, 42)
	}

	first := keyFor("legacy-token-a")
	require.Equal(t, first, keyFor("legacy-token-a"))
	require.NotEqual(t, first, keyFor("legacy-token-b"))
}

// TestStepUpSessionKeyFallsBackWithoutCredential supplies no session ID or
// Authorization credential for the user. It checks that the returned key uses
// the supplied user identifier.
func TestStepUpSessionKeyFallsBackWithoutCredential(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	require.Equal(t, "u42", StepUpSessionKey(c, 42))
}

// TestEnforceStepUpDisabledSkipsAllChecks disables the runtime switch for a
// user lacking TOTP and for an administrator API key. It checks successful
// un-aborted admission in both cases.
func TestEnforceStepUpDisabledSkipsAllChecks(t *testing.T) {
	disabled := stubStepUpSettingReader{enabled: false}

	t.Run("no totp, no grant", func(t *testing.T) {
		c, _ := newStepUpTestContext(t)
		c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

		ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: false}, stubStepUpUserReader{user: &identity.User{ID: 1, TotpEnabled: false}}, disabled)

		require.True(t, ok)
		require.False(t, c.IsAborted())
	})

	t.Run("admin api key", func(t *testing.T) {
		c, _ := newStepUpTestContext(t)
		c.Set("auth_method", audit.AuditAuthMethodAdminAPIKey)

		ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: false}, stubStepUpUserReader{user: nil, err: errors.New("should not be called")}, disabled)

		require.True(t, ok)
		require.False(t, c.IsAborted())
	})
}

// TestEnforceStepUpNilSettingsFailsClosed supplies no settings reader for an
// authenticated TOTP account with no grant. It checks that grant enforcement
// remains active and returns STEP_UP_REQUIRED.
func TestEnforceStepUpNilSettingsFailsClosed(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: false}, stubStepUpUserReader{user: &identity.User{ID: 1, TotpEnabled: true}}, nil)

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_REQUIRED")
}

// TestEnforceStepUpTypedNilSettingServiceFailsClosed supplies literal nil
// settings and no authenticated subject to enabled-by-default enforcement. It
// checks HTTP 401; this fixture does not exercise a typed-nil reader
// implementation.
func TestEnforceStepUpTypedNilSettingServiceFailsClosed(t *testing.T) {
	c, rec := newStepUpTestContext(t)

	ok := EnforceStepUp(c, nil, nil, nil)

	require.False(t, ok)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestStepUpUsesVerifiedPrincipal overwrites compatibility credential and
// session values after recording an API-key principal. It checks credential
// rejection and that the forged compatibility session is not selected.
func TestStepUpUsesVerifiedPrincipal(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin", nil)
	authctx.SetPrincipal(c, identity.Principal{UserID: 1, Role: "admin", CredentialKind: "admin_api_key"}, 1, "admin@example.invalid")
	c.Set("auth_method", "jwt")
	c.Set(authctx.ContextKeySessionID, "forged-display-session")
	require.False(t, EnforceStepUp(c, nil, nil, principalStepUpEnabled{}))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "STEP_UP_ADMIN_API_KEY_FORBIDDEN")
	require.NotEqual(t, "forged-display-session", StepUpSessionKey(c, 1))
}

// TestStepUpUsesCanonicalJWTSession overwrites the compatibility session after
// recording a JWT principal with its verified family. It checks that
// StepUpSessionKey returns the principal session value.
func TestStepUpUsesCanonicalJWTSession(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin", nil)
	authctx.SetPrincipal(c, identity.Principal{UserID: 1, Role: "admin", CredentialKind: "jwt", SessionID: "verified"}, 1, "")
	c.Set(authctx.ContextKeySessionID, "display-only")
	require.Equal(t, "verified", StepUpSessionKey(c, 1))
}

// HasStepUpGrant returns the fixture grant flag and configured lookup error.
// Inputs are ignored so each test controls whether the surrounding admission
// path handles success, absence, or failure.
func (s stubStepUpGrantChecker) HasStepUpGrant(ctx context.Context, userID int64, sessionKey string) (bool, error) {
	return s.granted, s.err
}

// GetByID returns the fixture account pointer and configured lookup error.
// Inputs are ignored so tests can expose missing accounts and TOTP state
// independently of repository lookup behavior.
func (s stubStepUpUserReader) GetByID(ctx context.Context, id int64) (*identity.User, error) {
	return s.user, s.err
}

// IsStepUpEnabled returns the runtime switch configured for this fixture.
func (s stubStepUpSettingReader) IsStepUpEnabled(ctx context.Context) bool {
	return s.enabled
}

// newStepUpTestContext creates a POST Gin context and response recorder for
// sensitive-operation tests.
func newStepUpTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/sensitive", nil)
	return c, rec
}

// IsStepUpEnabled enables verification checks for the verified-principal
// fixture cases.
func (principalStepUpEnabled) IsStepUpEnabled(context.Context) bool { return true }
