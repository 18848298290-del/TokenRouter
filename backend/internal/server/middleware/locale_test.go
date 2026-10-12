package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// TestLocaleSelection checks HTML cookies, weighted API language preferences, and admin presentation scope.
func TestLocaleSelection(t *testing.T) {
	for _, tc := range []struct {
		path, accept, language, cookie, want string
		user                                 bool
	}{
		{"/", "text/html", "en", "zh", "zh-Hans", true},
		{"/api/v1/user", "application/json", "en;q=0.2,zh-CN;q=0.9", "en", "zh-Hans", true},
		{"/api/v1/admin/users", "application/json", "en", "zh", "en", false},
		{"/v1/messages", "application/json", "invalid", "", "zh-Hans", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			router := gin.New()
			router.Use(Locale(func() string { return "zh-Hans" }))
			router.GET(tc.path, func(c *gin.Context) {
				require.Equal(t, tc.want, locale.FromContext(c.Request.Context()))
				require.Equal(t, tc.user, locale.UserPresentation(c.Request.Context()))
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.Header.Set("Accept", tc.accept)
			request.Header.Set("Accept-Language", tc.language)
			if tc.cookie != "" {
				request.AddCookie(&http.Cookie{Name: "tokenrouter_locale", Value: tc.cookie})
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, tc.want, response.Header().Get("Content-Language"))
			require.Contains(t, response.Header().Values("Vary"), "Accept-Language")
		})
	}
}
