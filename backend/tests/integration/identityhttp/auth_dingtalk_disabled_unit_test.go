package identityhttp_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Disabled DingTalk login returns an error through the frontend callback before
// authorization state is issued. The default authentication fixture leaves
// DingTalk disabled, and the start request checks its redirect path, error
// fragment, and absent Set-Cookie headers.

// TestDingTalkOAuthStart_Disabled calls the login start endpoint with DingTalk
// disabled in the fixture. It receives the default frontend callback redirect
// with dingtalk_not_enabled and no state cookies.
func TestDingTalkOAuthStart_Disabled(t *testing.T) {
	h, _ := newWeChatOAuthTestHandler(t, false)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/dingtalk/start", nil)
	h.DingTalkOAuthStart(c)
	require.Equal(t, http.StatusFound, recorder.Code)
	redirect, err := url.Parse(recorder.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "/auth/dingtalk/callback", redirect.Path)
	fragment, err := url.ParseQuery(redirect.Fragment)
	require.NoError(t, err)
	require.Equal(t, "dingtalk_not_enabled", fragment.Get("error"))
	require.Empty(t, recorder.Header().Values("Set-Cookie"))
}
