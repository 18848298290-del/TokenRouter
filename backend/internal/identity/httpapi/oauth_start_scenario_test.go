package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Anonymous GET starts reach the same CAPTCHA gate across email_oauth.go,
// linuxdo_oauth.go, oidc_oauth.go, wechat_oauth.go, and dingtalk_oauth.go. A
// bind intent in the query alone leaves the login-start path subject to that
// check before provider configuration or cookie writes.

// TestOAuthStartGetRejectsAnonymousLoginWhenTencentEnabledWithoutSideEffects
// sends anonymous GET login starts carrying a bind intent query to every
// provider handler. It checks the CAPTCHA rejection, absent redirects and
// cookies, and zero verifier calls.
func TestOAuthStartGetRejectsAnonymousLoginWhenTencentEnabledWithoutSideEffects(t *testing.T) {
	for provider, start := range oauthStartHandlers() {
		t.Run(provider, func(t *testing.T) {
			handler, verifier := newOAuthCaptchaTestHandler(true)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/"+provider+"/start?intent=bind_current_user", nil)

			start(handler, c)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "TENCENT_CAPTCHA_VERIFICATION_FAILED")
			require.Empty(t, recorder.Header().Get("Location"))
			require.Empty(t, recorder.Header().Values("Set-Cookie"))
			require.Zero(t, verifier.calls)
		})
	}
}
