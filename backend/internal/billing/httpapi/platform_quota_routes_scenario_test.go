package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Removed endpoint cases compare both billing route registration functions
// against the supplied former platform-quota paths.

// TestRemovedPlatformQuotaRoutesReturnNotFound registers the user/admin
// billing route families with empty handlers. It checks the four supplied
// former platform-quota paths return 404 for their GET, PUT and POST methods.
func TestRemovedPlatformQuotaRoutesReturnNotFound(t *testing.T) {
	router := gin.New()
	RegisterUserRoutes(router.Group("/api/v1"), &RedeemHandler{}, &SubscriptionHandler{})
	RegisterSubscriptionRoutes(router.Group("/api/v1/admin"), &AdminSubscriptionHandler{})
	for _, item := range []struct {
		// method and path identify each removed endpoint together; the registered
		// route families must leave that exact HTTP request unmatched by any handler.
		method, path string // method: requested HTTP verb; path: former endpoint URL
	}{
		{http.MethodGet, "/api/v1/user/platform-quotas"},
		{http.MethodGet, "/api/v1/admin/users/1/platform-quotas"},
		{http.MethodPut, "/api/v1/admin/users/1/platform-quotas"},
		{http.MethodPost, "/api/v1/admin/users/1/platform-quotas/reset"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(item.method, item.path, nil))
		require.Equal(t, http.StatusNotFound, recorder.Code, "%s %s", item.method, item.path)
	}
}
