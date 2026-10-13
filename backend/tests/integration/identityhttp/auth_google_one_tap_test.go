package identityhttp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	identityprovider "github.com/TokenFlux/TokenRouter/internal/identity/provider"
)

// Google One Tap enters the shared authentication flow through a supplied
// credential and configured verifier. SQLite cases inspect pending
// registration, existing-account token responses, and rejection before state
// creation. Captured verifier inputs check credential and audience forwarding;
// configured claims and errors isolate handler decisions from Google token
// verification.

// googleIDTokenVerifierStub captures verifier inputs and returns supplied claim
// and error values.
type googleIDTokenVerifierStub struct {
	// claims retains caller-owned data and is returned alongside err, including a
	// nonnil pointer with an error. The handler decides whether those claims are
	// usable.
	claims *identityprovider.GoogleIDTokenClaims // borrowed Google claims
	err    error                                 // configured verifier error
	// calls, credential, and audience record each invocation before returning the
	// configured result. They hold unsynchronized sequential observations, with
	// the strings replaced by the latest call.
	calls      int    // recorded verifier invocation count
	credential string // most recently supplied credential
	audience   string // most recently supplied audience
}

// Verify records the credential, audience, and call count before returning
// claims and err together. The claims pointer is borrowed, context is ignored,
// and the unsynchronized observations require sequential fixture use.
func (s *googleIDTokenVerifierStub) Verify(_ context.Context, credential string, audience string) (*identityprovider.GoogleIDTokenClaims, error) {
	s.calls++
	s.credential = credential
	s.audience = audience
	return s.claims, s.err
}

// googleOneTapTestSettings returns a fresh settings map enabling One Tap with
// complete Google OAuth credentials and local callback configuration. Callers
// can mutate that map independently before constructing each handler fixture.
func googleOneTapTestSettings() map[string]string {
	return map[string]string{
		identity.SettingKeyGoogleOneTapEnabled:            "true",
		identity.SettingKeyGoogleOAuthEnabled:             "true",
		identity.SettingKeyGoogleOAuthClientID:            "google-web-client",
		identity.SettingKeyGoogleOAuthClientSecret:        "google-client-secret",
		identity.SettingKeyGoogleOAuthRedirectURL:         "https://app.example/api/v1/auth/oauth/google/callback",
		identity.SettingKeyGoogleOAuthFrontendRedirectURL: "/auth/oauth/callback",
	}
}

// performGoogleOneTapRequest invokes the fixture's One Tap handler with body as
// JSON and returns its recorder. It creates a direct Gin context, so handler
// behavior is exercised without router middleware.
func performGoogleOneTapRequest(t *testing.T, handler *authHTTPFixture, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/google/one-tap", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	handler.GoogleOneTap(c)
	return recorder
}

// TestGoogleOneTapCreatesPendingRegistrationForNewUser supplies verified Google
// claims for an unregistered email. The response requires registration,
// forwards credential and audience, and saves subject, referral, promotion, and
// browser pending state.
func TestGoogleOneTapCreatesPendingRegistrationForNewUser(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: googleOneTapTestSettings(),
	})
	verifier := &googleIDTokenVerifierStub{claims: &identityprovider.GoogleIDTokenClaims{
		Subject:       "google-new-user",
		Email:         "new-user@example.com",
		EmailVerified: true,
		Name:          "New User",
	}}
	handler.googleIDTokenVerifier = verifier

	recorder := performGoogleOneTapRequest(t, handler, `{"credential":"valid-token","redirect":"/dashboard","aff_code":"AFF123","promo_code":"PROMO123"}`)

	require.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeJSONResponseData(t, recorder)
	require.Equal(t, identityhttp.GoogleOneTapStatusRegistration, payload["status"])
	require.Equal(t, "/dashboard", payload["redirect"])
	require.Equal(t, "valid-token", verifier.credential)
	require.Equal(t, "google-web-client", verifier.audience)

	session, err := client.PendingAuthSession.Query().Only(context.Background())
	require.NoError(t, err)
	require.Equal(t, "google", session.ProviderType)
	require.Equal(t, "google-new-user", session.ProviderSubject)
	require.Equal(t, "new-user@example.com", session.ResolvedEmail)
	require.Equal(t, "AFF123", identityhttp.PendingSessionStringValue(session.UpstreamIdentityClaims, "aff_code"))
	require.Equal(t, "PROMO123", identityhttp.PendingSessionStringValue(session.LocalFlowState, identityhttp.OauthPromoCodeStateKey))
	require.NotNil(t, findCookie(recorder.Result().Cookies(), identityhttp.OauthPendingSessionCookieName))
	require.NotNil(t, findCookie(recorder.Result().Cookies(), identityhttp.OauthPendingBrowserCookieName))
}

// TestGoogleOneTapLogsInExistingUser supplies Google claims matching an active
// SQLite account. It receives a Bearer token pair with no-store response
// caching and records the Google identity on that user.
func TestGoogleOneTapLogsInExistingUser(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: googleOneTapTestSettings(),
	})
	ctx := context.Background()
	user, err := client.User.Create().
		SetEmail("existing@example.com").
		SetUsername("existing").
		SetPasswordHash("hash").
		SetRole(identity.RoleUser).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	handler.googleIDTokenVerifier = &googleIDTokenVerifierStub{claims: &identityprovider.GoogleIDTokenClaims{
		Subject:       "google-existing-user",
		Email:         "existing@example.com",
		EmailVerified: true,
	}}

	recorder := performGoogleOneTapRequest(t, handler, `{"credential":"valid-token","redirect":"/dashboard"}`)

	require.Equal(t, http.StatusOK, recorder.Code)
	payload := decodeJSONResponseData(t, recorder)
	require.Equal(t, identityhttp.GoogleOneTapStatusAuthenticated, payload["status"])
	require.NotEmpty(t, payload["access_token"])
	require.NotEmpty(t, payload["refresh_token"])
	require.Equal(t, "Bearer", payload["token_type"])
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))

	identityCount, err := client.AuthIdentity.Query().Where(
		authidentity.ProviderTypeEQ("google"),
		authidentity.ProviderSubjectEQ("google-existing-user"),
		authidentity.UserIDEQ(user.ID),
	).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, identityCount)
}

// TestGoogleOneTapRejectsDisabledExistingUser supplies Google claims for a
// disabled SQLite account. The handler returns HTTP 403, omits access-token
// text, and leaves no pending session.
func TestGoogleOneTapRejectsDisabledExistingUser(t *testing.T) {
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: googleOneTapTestSettings(),
	})
	ctx := context.Background()
	_, err := client.User.Create().
		SetEmail("disabled@example.com").
		SetUsername("disabled").
		SetPasswordHash("hash").
		SetRole(identity.RoleUser).
		SetStatus(billing.StatusDisabled).
		Save(ctx)
	require.NoError(t, err)
	handler.googleIDTokenVerifier = &googleIDTokenVerifierStub{claims: &identityprovider.GoogleIDTokenClaims{
		Subject:       "google-disabled-user",
		Email:         "disabled@example.com",
		EmailVerified: true,
	}}

	recorder := performGoogleOneTapRequest(t, handler, `{"credential":"valid-token"}`)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "access_token")
	count, err := client.PendingAuthSession.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

// TestGoogleOneTapFailsClosedBeforeIdentityLookup varies disabled or incomplete
// settings, verifier failure, unverified email, and captcha activation. Each
// request fails with no pending row or supplied sensitive text; the
// settings-gated cases invoke no verifier.
func TestGoogleOneTapFailsClosedBeforeIdentityLookup(t *testing.T) {
	tests := []struct {
		name string // credential rejection scenario
		// settingsMutate is invoked for each case's fresh settings map; every case
		// supplies a nonnil callback before constructing its authentication handler.
		settingsMutate func(map[string]string) // settings mutation callback
		// verifier is installed after handler construction; the fixture adapter reads
		// this pointer during Verify so the case can inspect its retained
		// observations.
		verifier *googleIDTokenVerifierStub // injected verifier fixture
	}{
		{
			name: "One Tap 已关闭",
			settingsMutate: func(settings map[string]string) {
				settings[identity.SettingKeyGoogleOneTapEnabled] = "false"
			},
			verifier: &googleIDTokenVerifierStub{claims: &identityprovider.GoogleIDTokenClaims{Subject: "sub", Email: "user@example.com", EmailVerified: true}},
		},
		{
			name: "Google OAuth 配置不完整",
			settingsMutate: func(settings map[string]string) {
				delete(settings, identity.SettingKeyGoogleOAuthClientSecret)
			},
			verifier: &googleIDTokenVerifierStub{claims: &identityprovider.GoogleIDTokenClaims{Subject: "sub", Email: "user@example.com", EmailVerified: true}},
		},
		{
			name:           "凭据验证失败",
			settingsMutate: func(map[string]string) {},
			verifier:       &googleIDTokenVerifierStub{err: errors.New("signature verification failed")},
		},
		{
			name:           "邮箱未验证",
			settingsMutate: func(map[string]string) {},
			verifier:       &googleIDTokenVerifierStub{claims: &identityprovider.GoogleIDTokenClaims{Subject: "sub", Email: "user@example.com"}},
		},
		{
			name: "动作验证码已开启",
			settingsMutate: func(settings map[string]string) {
				settings[identity.SettingKeyTencentCaptchaEnabled] = "true"
			},
			verifier: &googleIDTokenVerifierStub{claims: &identityprovider.GoogleIDTokenClaims{Subject: "sub", Email: "user@example.com", EmailVerified: true}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := googleOneTapTestSettings()
			tt.settingsMutate(settings)
			handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
				settingValues: settings,
			})
			handler.googleIDTokenVerifier = tt.verifier

			recorder := performGoogleOneTapRequest(t, handler, `{"credential":"untrusted-token"}`)

			require.GreaterOrEqual(t, recorder.Code, http.StatusBadRequest)
			require.NotContains(t, recorder.Body.String(), "untrusted-token")
			require.NotContains(t, recorder.Body.String(), "signature verification failed")
			count, err := client.PendingAuthSession.Query().Count(context.Background())
			require.NoError(t, err)
			require.Zero(t, count)
			if tt.name != "凭据验证失败" && tt.name != "邮箱未验证" {
				require.Zero(t, tt.verifier.calls)
			}
		})
	}
}

// TestGoogleOneTapRejectsNewUserWhenRegistrationDisabled supplies verified
// claims for a new email while registration is disabled. The handler returns
// HTTP 403 and creates no pending session for that profile.
func TestGoogleOneTapRejectsNewUserWhenRegistrationDisabled(t *testing.T) {
	settings := googleOneTapTestSettings()
	settings[identity.SettingKeyRegistrationEnabled] = "false"
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: settings,
	})
	handler.googleIDTokenVerifier = &googleIDTokenVerifierStub{claims: &identityprovider.GoogleIDTokenClaims{
		Subject:       "google-disabled-registration",
		Email:         "disabled-registration@example.com",
		EmailVerified: true,
	}}

	recorder := performGoogleOneTapRequest(t, handler, `{"credential":"valid-token"}`)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	count, err := client.PendingAuthSession.Query().Count(context.Background())
	require.NoError(t, err)
	require.Zero(t, count)
}

// TestGoogleOneTapRejectsOversizedCredential supplies one ASCII byte beyond the
// credential limit with absent authentication dependencies. The handler returns
// HTTP 400 and its invalid-credential message.
func TestGoogleOneTapRejectsOversizedCredential(t *testing.T) {
	handler := newAuthHTTPFixture(t, &authHTTPFixture{})
	// Cross the credential bound while keeping the request syntactically valid
	// JSON.
	recorder := performGoogleOneTapRequest(t, handler, `{"credential":"`+strings.Repeat("x", identityhttp.GoogleOneTapCredentialMaxBytes+1)+`"}`)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Google credential is invalid")
}

// TestGoogleOneTapRejectsOversizedRequestBeforeBinding pads otherwise valid
// JSON beyond the whole-request limit with absent dependencies. The handler
// returns HTTP 400 with Invalid request and omits AUTH_SERVICE_NOT_READY.
func TestGoogleOneTapRejectsOversizedRequestBeforeBinding(t *testing.T) {
	handler := newAuthHTTPFixture(t, &authHTTPFixture{})
	// Keep the credential small while padding exceeds the whole JSON request
	// limit.
	body := `{"credential":"valid-token","padding":"` + strings.Repeat("x", identityhttp.GoogleOneTapRequestMaxBytes) + `"}`

	recorder := performGoogleOneTapRequest(t, handler, body)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Invalid request")
	require.NotContains(t, recorder.Body.String(), "AUTH_SERVICE_NOT_READY")
}
