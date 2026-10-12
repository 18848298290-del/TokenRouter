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

// stepUpEnabled enables the feature for step-up tests.
var stepUpEnabled = stubStepUpSettingReader{enabled: true}

type stubStepUpGrantChecker struct {
	granted bool
	err     error
}

type stubStepUpUserReader struct {
	user *identity.User
	err  error
}

type stubStepUpSettingReader struct {
	enabled bool
}

type principalStepUpEnabled struct{}

func TestEnforceStepUpRejectsAdminAPIKey(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set("auth_method", audit.AuditAuthMethodAdminAPIKey)

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &identity.User{TotpEnabled: true}}, stepUpEnabled)

	require.False(t, ok)
	require.True(t, c.IsAborted())
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_ADMIN_API_KEY_FORBIDDEN")
}

func TestEnforceStepUpRequiresAuthSubject(t *testing.T) {
	c, rec := newStepUpTestContext(t)

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &identity.User{TotpEnabled: true}}, stepUpEnabled)

	require.False(t, ok)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestEnforceStepUpRequiresTotpEnabled(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &identity.User{ID: 1, TotpEnabled: false}}, stepUpEnabled)

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_TOTP_NOT_ENABLED")
}

func TestEnforceStepUpFailsClosedOnNilUser(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{}, stepUpEnabled)

	require.False(t, ok)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestEnforceStepUpFailsClosedOnGrantError(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{err: errors.New("redis down")}, stubStepUpUserReader{user: &identity.User{ID: 1, TotpEnabled: true}}, stepUpEnabled)

	require.False(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_UNAVAILABLE")
}

func TestEnforceStepUpRequiresGrant(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: false}, stubStepUpUserReader{user: &identity.User{ID: 1, TotpEnabled: true}}, stepUpEnabled)

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_REQUIRED")
}

func TestEnforceStepUpPassesWithGrant(t *testing.T) {
	c, _ := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &identity.User{ID: 1, TotpEnabled: true}}, stepUpEnabled)

	require.True(t, ok)
	require.False(t, c.IsAborted())
}

func TestStepUpSessionKeyUsesSessionID(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Set(authctx.ContextKeySessionID, "family-1")
	require.Equal(t, "family-1", StepUpSessionKey(c, 42))
}

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

func TestStepUpSessionKeyFallsBackWithoutCredential(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	require.Equal(t, "u42", StepUpSessionKey(c, 42))
}

// TestEnforceStepUpDisabledSkipsAllChecks checks that a disabled switch bypasses credential and grant checks.
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

// TestEnforceStepUpNilSettingsFailsClosed checks that nil settings leave enforcement enabled.
func TestEnforceStepUpNilSettingsFailsClosed(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 1})

	ok := EnforceStepUp(c, stubStepUpGrantChecker{granted: false}, stubStepUpUserReader{user: &identity.User{ID: 1, TotpEnabled: true}}, nil)

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_REQUIRED")
}

// TestEnforceStepUpTypedNilSettingServiceFailsClosed checks that a typed nil settings reader still rejects unauthenticated requests.
func TestEnforceStepUpTypedNilSettingServiceFailsClosed(t *testing.T) {
	c, rec := newStepUpTestContext(t)

	ok := EnforceStepUp(c, nil, nil, nil)

	require.False(t, ok)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestStepUpUsesVerifiedPrincipal checks that the verified API key principal causes a 403 response.
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

// TestStepUpUsesCanonicalJWTSession checks that grants use the verified JWT session ID.
func TestStepUpUsesCanonicalJWTSession(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin", nil)
	authctx.SetPrincipal(c, identity.Principal{UserID: 1, Role: "admin", CredentialKind: "jwt", SessionID: "verified"}, 1, "")
	c.Set(authctx.ContextKeySessionID, "display-only")
	require.Equal(t, "verified", StepUpSessionKey(c, 1))
}

func (s stubStepUpGrantChecker) HasStepUpGrant(ctx context.Context, userID int64, sessionKey string) (bool, error) {
	return s.granted, s.err
}

func (s stubStepUpUserReader) GetByID(ctx context.Context, id int64) (*identity.User, error) {
	return s.user, s.err
}

func (s stubStepUpSettingReader) IsStepUpEnabled(ctx context.Context) bool {
	return s.enabled
}

func newStepUpTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/sensitive", nil)
	return c, rec
}

func (principalStepUpEnabled) IsStepUpEnabled(context.Context) bool { return true }
