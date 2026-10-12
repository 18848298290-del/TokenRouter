package httpapi

// These tests cover login-start checks for email OAuth, LinuxDo, OIDC, WeChat, and DingTalk.
import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

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
