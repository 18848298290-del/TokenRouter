package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestProviderTerminologyBoundary covers legacy field rejection, nested credentials, and subsequent JSON binding.
func TestProviderTerminologyBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/api/v1/admin/providers", `{"provider_ids":[1],"credentials":{"account_id":"external"}}`, 200},
		{"/api/v1/admin/providers", `{"provider_ids":[1],"account_ids":[2]}`, 400},
		{"/api/v1/admin/usage?account_id=1", `{}`, 400},
		{"/api/v1/admin/usage?provider_id=1", `{}`, 200},
		{"/api/v1/admin/users", `{"account":"login"}`, 200},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			engine := gin.New()
			engine.Use(ProviderTerminology())
			engine.POST("/api/v1/admin/:section", func(c *gin.Context) {
				var body map[string]any
				require.NoError(t, c.ShouldBindJSON(&body))
				c.JSON(200, body)
			})
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, tc.status, recorder.Code)
		})
	}
}
