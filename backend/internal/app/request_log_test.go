package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/requestlog"
)

// TestRequestLogRoutesAdminOnly checks that user URLs return 404 and admin routes apply authentication, auditing, and rate limiting.
func TestRequestLogRoutesAdminOnly(t *testing.T) {
	router := gin.New()
	var chain []string
	admin := func(c *gin.Context) {
		chain = append(chain, "admin")
		authctx.SetPrincipal(c, identity.Principal{UserID: 42, Role: c.GetHeader("Test-Role")}, 1, "")
		c.Next()
	}
	audit := func(c *gin.Context) {
		chain = append(chain, "audit")
		c.Next()
	}
	limit := func(c *gin.Context) {
		chain = append(chain, "limit")
		c.Next()
	}
	registerRequestLogRoutes(router.Group("/api/v1"), requestlog.NewService(nil, 30, nil), admin, audit, limit)
	for _, path := range []string{"/api/v1/requests?request_id=id", "/api/v1/requests/id"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, response.Code)
	}
	for _, path := range []string{"/api/v1/admin/requests?request_id=id", "/api/v1/admin/requests/id", "/api/v1/admin/requests/health"} {
		chain = nil
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Test-Role", "user")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusForbidden, response.Code)
		require.Equal(t, []string{"admin", "audit", "limit"}, chain)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/requests/health", nil)
	request.Header.Set("Test-Role", "admin")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
}
