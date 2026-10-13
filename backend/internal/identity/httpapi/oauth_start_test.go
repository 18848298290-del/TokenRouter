package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// OAuth start admission and response writing are tested independently of
// provider authorization clients. Enabled Tencent verification requires proof
// in POST JSON, while disabled verification permits redirect responses and
// current-user bind paths bypass the anonymous proof.

// TestOAuthStartPostReturnsAuthorizeURLAfterTencentVerification supplies
// Tencent proof for each provider path and then writes the authorize response.
// It checks HTTP 200, the JSON URL, and the exact proof recorded once.
func TestOAuthStartPostReturnsAuthorizeURLAfterTencentVerification(t *testing.T) {
	for provider := range oauthStartHandlers() {
		t.Run(provider, func(t *testing.T) {
			handler, verifier := newOAuthCaptchaTestHandler(true)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(
				http.MethodPost,
				"/api/v1/auth/oauth/"+provider+"/start",
				strings.NewReader(`{"tencent_captcha_ticket":"ticket-value","tencent_captcha_randstr":"@rand-value"}`),
			)
			c.Request.Header.Set("Content-Type", "application/json")

			require.True(t, handler.Session.RequireActionCaptchaForOAuthLoginStart(c))
			RespondOAuthStart(c, "https://provider.example/authorize")

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"authorize_url":"https://provider.example/authorize"`)
			require.Equal(t, 1, verifier.calls)
			require.Equal(t, identity.TencentCaptchaProof{Ticket: "ticket-value", Randstr: "@rand-value"}, verifier.proof)
		})
	}
}

// TestOAuthStartPostRequiresTencentProofWhenEnabled submits empty POST proof
// for each provider path. It checks rejection with the CAPTCHA error before
// the verifier receives any ticket.
func TestOAuthStartPostRequiresTencentProofWhenEnabled(t *testing.T) {
	for provider := range oauthStartHandlers() {
		t.Run(provider, func(t *testing.T) {
			handler, verifier := newOAuthCaptchaTestHandler(true)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/"+provider+"/start", strings.NewReader(`{}`))
			c.Request.Header.Set("Content-Type", "application/json")

			require.False(t, handler.Session.RequireActionCaptchaForOAuthLoginStart(c))
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "TENCENT_CAPTCHA_VERIFICATION_FAILED")
			require.Zero(t, verifier.calls)
		})
	}
}

// TestOAuthBindingPathRemainsOutsideTencentGate uses an OIDC bind-start URL
// with empty session dependencies. It checks that the admission helper accepts
// the path without needing anonymous CAPTCHA services.
func TestOAuthBindingPathRemainsOutsideTencentGate(t *testing.T) {
	handler := &AuthenticationHandler{Session: &SessionHandler{}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/oidc/bind/start", nil)

	require.True(t, handler.Session.RequireActionCaptchaForOAuthLoginStart(c))
	require.Equal(t, http.StatusOK, recorder.Code)
}

// TestOAuthStartGetRemainsCompatibleWhenTencentDisabled sends a GitHub GET
// start to the shared helper. It checks the 302 Location response and that
// Tencent verification was never invoked.
func TestOAuthStartGetRemainsCompatibleWhenTencentDisabled(t *testing.T) {
	handler, verifier := newOAuthCaptchaTestHandler(false)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/github/start", nil)

	require.True(t, handler.Session.RequireActionCaptchaForOAuthLoginStart(c))
	RespondOAuthStart(c, "https://provider.example/authorize")

	require.Equal(t, http.StatusFound, recorder.Code)
	require.Equal(t, "https://provider.example/authorize", recorder.Header().Get("Location"))
	require.Zero(t, verifier.calls)
}
