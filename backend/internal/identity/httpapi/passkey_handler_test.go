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

// Anonymous Passkey begin requests check Tencent action proof before creating
// a ceremony. A disabled Passkey service distinguishes rejection at the
// CAPTCHA gate from successful proof verification that reaches the next
// service and receives its feature-disabled response.

// TestPasskeyBeginLoginRequiresTencentCaptchaBeforeCeremony submits empty
// proof with Tencent enabled and a disabled Passkey service. It checks the
// CAPTCHA error and zero verifier calls before ceremony handling.
func TestPasskeyBeginLoginRequiresTencentCaptchaBeforeCeremony(t *testing.T) {
	authHandler, verifier := newOAuthCaptchaTestHandler(true)
	passkeys := identity.NewPasskeyService(false, nil, nil, nil, nil)
	handler := NewPasskeyHandler(passkeys, authHandler.Session.authService, nil)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/passkey/login/begin", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.BeginLogin(c)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "TENCENT_CAPTCHA_VERIFICATION_FAILED")
	require.Zero(t, verifier.calls)
}

// TestPasskeyBeginLoginVerifiesTencentCaptchaBeforePasskeyService supplies
// valid Tencent proof with the Passkey service disabled. It checks the
// recorded proof and the subsequent PASSKEY_DISABLED response from that
// service.
func TestPasskeyBeginLoginVerifiesTencentCaptchaBeforePasskeyService(t *testing.T) {
	authHandler, verifier := newOAuthCaptchaTestHandler(true)
	passkeys := identity.NewPasskeyService(false, nil, nil, nil, nil)
	handler := NewPasskeyHandler(passkeys, authHandler.Session.authService, nil)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/passkey/login/begin",
		strings.NewReader(`{"tencent_captcha_ticket":"ticket-value","tencent_captcha_randstr":"@rand-value"}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")

	handler.BeginLogin(c)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "PASSKEY_DISABLED")
	require.Equal(t, 1, verifier.calls)
	require.Equal(t, identity.TencentCaptchaProof{Ticket: "ticket-value", Randstr: "@rand-value"}, verifier.proof)
}
