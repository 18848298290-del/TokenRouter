package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Provider terminology validation applies to selected management resources and
// their top-level identifiers. The scenarios distinguish forbidden legacy keys
// from nested credential fields, then use an ordinary JSON binder after
// middleware inspection to verify the accepted body remains readable.

// TestProviderTerminologyBoundary supplies legacy query fields, top-level JSON
// keys, and nested upstream credentials across management sections. The
// response status protects the selected scope, and allowed input remains usable
// by downstream ordinary JSON binding.
func TestProviderTerminologyBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		// path supplies the resource section and optional query fields. body supplies
		// the JSON payload consumed by inspection and the downstream binder.
		path, body string
		status     int // expected final response status
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
