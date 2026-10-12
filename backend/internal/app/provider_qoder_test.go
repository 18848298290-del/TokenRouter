package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
)

// Qoder OAuth administrator paths cover authorization URL creation, code
// exchange, and polling. POST fixtures check that those paths receive responses
// other than 404, while cleanup stops the session maintenance started for the
// authorization core.

// TestAdminRoutesQoderOAuthPathsAreRegistered mounts the three Qoder OAuth POST
// routes. Fixture requests must receive non-404 responses for URL creation,
// code exchange, and polling, exercising the mounted administrator paths.
func TestAdminRoutesQoderOAuthPathsAreRegistered(t *testing.T) {
	router := gin.New()
	qoderOAuthService := provideQoderAuthorization(nil)
	// Start session maintenance and register its stop before making requests.
	// Cleanup waits for that worker and any admitted authorization operations
	// after the route probes have returned.
	qoderOAuthService.Core.Start()
	t.Cleanup(func() { require.NoError(t, qoderOAuthService.Core.StopContext(context.Background())) })

	registerQoderOAuthRoutes(
		router.Group("/api/v1/admin"),
		&routeTestHandlers{
			Admin: &routeTestAdminHandlers{
				QoderOAuth: providerhttp.NewQoderOAuthHandler(qoderOAuthService),
			},
		},
	)

	for _, tc := range []struct {
		path string // mounted administrator URL
		body string // submitted JSON fixture
	}{
		{path: "/api/v1/admin/qoder/oauth/auth-url", body: `{}`},
		{path: "/api/v1/admin/qoder/oauth/exchange-code", body: `{"session_id":"missing","state":"state","code":"code"}`},
		{path: "/api/v1/admin/qoder/oauth/poll", body: `{"session_id":"missing","state":"state"}`},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)
		require.NotEqual(t, http.StatusNotFound, rec.Code, "path=%s should hit Qoder OAuth handler", tc.path)
	}
}
