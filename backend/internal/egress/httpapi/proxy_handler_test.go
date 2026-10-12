package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestProxyHandlerEndpoints exercises successful list, lookup,
// create/update/delete, batch-delete, probe, quality, statistics, and
// provider-summary routes against a fixture. It checks HTTP success statuses
// for these requests rather than real persistence or complete response
// payloads.
func TestProxyHandlerEndpoints(t *testing.T) {
	router, _ := setupProxyAdminContractRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxies", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxies/all", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxies/4", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body, _ := json.Marshal(map[string]any{"name": "proxy", "protocol": "http", "host": "localhost", "port": 8080})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/proxies", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body, _ = json.Marshal(map[string]any{"name": "proxy2"})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/proxies/4", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/proxies/4", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/proxies/batch-delete", bytes.NewBufferString(`{"ids":[1,2]}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/proxies/4/test", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/proxies/4/quality-check", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxies/4/stats", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxies/4/providers", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

// setupProxyAdminContractRouter binds direct handler endpoints to the synthetic
// administrator fixture without authentication middleware. Its zero-value
// idempotency executor invokes create callbacks directly, letting tests focus
// on endpoint response availability.
func setupProxyAdminContractRouter() (*gin.Engine, *proxyAdminFixture) {
	router := gin.New()
	source := newProxyAdminFixture()
	handler := NewProxyHandler(source)
	router.GET("/api/v1/admin/proxies", handler.List)
	router.GET("/api/v1/admin/proxies/all", handler.GetAll)
	router.GET("/api/v1/admin/proxies/:id", handler.GetByID)
	router.POST("/api/v1/admin/proxies", handler.Create)
	router.PUT("/api/v1/admin/proxies/:id", handler.Update)
	router.DELETE("/api/v1/admin/proxies/:id", handler.Delete)
	router.POST("/api/v1/admin/proxies/batch-delete", handler.BatchDelete)
	router.POST("/api/v1/admin/proxies/:id/test", handler.Test)
	router.POST("/api/v1/admin/proxies/:id/quality-check", handler.CheckQuality)
	router.GET("/api/v1/admin/proxies/:id/stats", handler.GetStats)
	router.GET("/api/v1/admin/proxies/:id/providers", handler.GetProxyProviders)
	return router, source
}
