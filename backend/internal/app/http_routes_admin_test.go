package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	backuphttp "github.com/TokenFlux/TokenRouter/internal/backup/httpapi"
	egresshttp "github.com/TokenFlux/TokenRouter/internal/egress/httpapi"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	opshttp "github.com/TokenFlux/TokenRouter/internal/ops/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
	routinghttpapi "github.com/TokenFlux/TokenRouter/internal/routing/httpapi"
	schedulerhttp "github.com/TokenFlux/TokenRouter/internal/scheduler/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	servermiddleware "github.com/TokenFlux/TokenRouter/internal/server/middleware"
)

// Compatibility route tests use the backup HTTP adapter's ID validation.
var requireCanonicalBackupID = backuphttp.RequireCanonicalBackupID

// TestProtocolCatalogHTTPContract checks authentication before auditing and isolation from changes to injected
// catalogue data.
func TestProtocolCatalogHTTPContract(t *testing.T) {
	endpoints := testEndpoints()
	expected, err := json.Marshal(routinghttpapi.AdminProtocolCatalog(endpoints))
	require.NoError(t, err)
	catalog := routinghttpapi.NewProtocolCatalogHandler(endpoints)
	endpoints[protocol.ProtocolOpenAIResponses] = "unexpected-change"
	var calls []string
	auth := identityhttp.AdminAuthMiddleware(func(c *gin.Context) {
		calls = append(calls, "auth")
		switch c.GetHeader("Authorization") {
		case "admin":
			c.Next()
		case "user":
			c.AbortWithStatus(http.StatusForbidden)
		default:
			c.AbortWithStatus(http.StatusUnauthorized)
		}
	})
	audit := servermiddleware.AuditLogMiddleware(func(c *gin.Context) {
		calls = append(calls, "audit")
		c.Next()
	})
	router := gin.New()
	RegisterAdminRoutes(router.Group("/api/v1"), &routeTestHandlers{Admin: &routeTestAdminHandlers{Proxy: egresshttp.NewProxyHandler(nil), Group: routinghttpapi.NewGroupHandler(nil)}}, auth, audit, nil, nil, func(c *gin.Context) {
		calls = append(calls, "catalog")
		catalog(c)
	})
	for _, tc := range []struct {
		auth   string
		status int
	}{
		{"", http.StatusUnauthorized},
		{"user", http.StatusForbidden},
		{"admin", http.StatusOK},
	} {
		calls = nil
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/protocol-capabilities", nil)
		request.Header.Set("Authorization", tc.auth)
		router.ServeHTTP(recorder, request)
		require.Equal(t, tc.status, recorder.Code)
		if tc.status != http.StatusOK {
			require.Equal(t, []string{"auth"}, calls)
			continue
		}
		require.Equal(t, []string{"auth", "audit", "catalog"}, calls)
		var response struct {
			Code int             `json:"code"`
			Data json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		require.Zero(t, response.Code)
		require.JSONEq(t, string(expected), string(response.Data))
	}
}

// TestAdminImageStorageRoutesAreRemoved checks that retired image storage settings are absent and backup
// settings remain reachable.
func TestAdminImageStorageRoutesAreRemoved(t *testing.T) {
	router := gin.New()
	admin := router.Group("/api/v1/admin")
	h := &routeTestHandlers{
		Admin: &routeTestAdminHandlers{
			Backup: backuphttp.NewBackupHandler(nil, nil),
		},
	}
	registerBackupRoutes(admin, h, func(c *gin.Context) { c.Next() })

	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	removed := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/admin/backups/image-storage"},
		{method: http.MethodPut, path: "/api/v1/admin/backups/image-storage"},
		{method: http.MethodPost, path: "/api/v1/admin/backups/image-storage/test"},
	}
	for _, route := range removed {
		routeKey := route.method + " " + route.path
		require.False(t, registered[routeKey], "%s should not be registered", routeKey)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(route.method, route.path, nil)
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "method=%s path=%s", route.method, route.path)
	}

	for _, route := range []string{
		"GET /api/v1/admin/backups/storage-config",
		"PUT /api/v1/admin/backups/storage-config",
		"POST /api/v1/admin/backups/storage-config/test",
		"GET /api/v1/admin/backups/content-config",
		"PUT /api/v1/admin/backups/content-config",
		"GET /api/v1/admin/backups/s3-config",
		"PUT /api/v1/admin/backups/s3-config",
		"POST /api/v1/admin/backups/s3-config/test",
		"GET /api/v1/admin/backups/schedule",
		"PUT /api/v1/admin/backups/schedule",
	} {
		require.True(t, registered[route], "%s should remain registered", route)
	}
}

// TestAdminUpstreamBillingProbeRoutesAreRemoved checks ordinary 404 responses for retired billing multiplier
// probe endpoints.
func TestAdminUpstreamBillingProbeRoutesAreRemoved(t *testing.T) {
	router := gin.New()
	admin := router.Group("/api/v1/admin")
	h := &routeTestHandlers{Admin: &routeTestAdminHandlers{
		ProviderManagement:   &providerhttp.ManagementHandler{},
		SchedulerDiagnostics: &schedulerhttp.DiagnosticsHandler{},
		OAuth:                &providerhttp.ClaudeOAuthHandler{},
		OpenAIOAuth:          &providerhttp.OpenAIOAuthHandler{},
		CodexInviteReset:     &providerhttp.CodexInviteResetHandler{},
	}}
	registerProviderRoutes(admin, h, func(c *gin.Context) { c.Next() })

	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	removed := []struct {
		method      string
		routePath   string
		requestPath string
	}{
		{method: http.MethodGet, routePath: "/api/v1/admin/providers/upstream-billing-probe/settings", requestPath: "/api/v1/admin/providers/upstream-billing-probe/settings"},
		{method: http.MethodPut, routePath: "/api/v1/admin/providers/upstream-billing-probe/settings", requestPath: "/api/v1/admin/providers/upstream-billing-probe/settings"},
		{method: http.MethodPost, routePath: "/api/v1/admin/providers/upstream-billing-probe/batch", requestPath: "/api/v1/admin/providers/upstream-billing-probe/batch"},
		{method: http.MethodPut, routePath: "/api/v1/admin/providers/:id/upstream-billing-probe", requestPath: "/api/v1/admin/providers/42/upstream-billing-probe"},
		{method: http.MethodPost, routePath: "/api/v1/admin/providers/:id/upstream-billing-probe", requestPath: "/api/v1/admin/providers/42/upstream-billing-probe"},
	}
	for _, route := range removed {
		require.False(t, registered[route.method+" "+route.routePath])
		w := httptest.NewRecorder()
		req := httptest.NewRequest(route.method, route.requestPath, nil)
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "method=%s path=%s", route.method, route.requestPath)
	}
}

func TestAdminAdvancedSchedulerScoreRoutesAreRegistered(t *testing.T) {
	router := gin.New()
	admin := router.Group("/api/v1/admin")
	h := &routeTestHandlers{Admin: &routeTestAdminHandlers{
		ProviderManagement:   &providerhttp.ManagementHandler{},
		SchedulerDiagnostics: &schedulerhttp.DiagnosticsHandler{},
		OAuth:                &providerhttp.ClaudeOAuthHandler{},
		OpenAIOAuth:          &providerhttp.OpenAIOAuthHandler{},
		CodexInviteReset:     &providerhttp.CodexInviteResetHandler{},
	}}
	registerProviderRoutes(admin, h, func(c *gin.Context) { c.Next() })

	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	require.True(t, registered["GET /api/v1/admin/providers/:id/advanced-scheduler-score"])
	require.True(t, registered["POST /api/v1/admin/providers/:id/advanced-scheduler-score/preview"])
}

// TestCanonicalBackupIDRouteGuard checks that backup wildcard routes accept the service's generated ID format.
func TestCanonicalBackupIDRouteGuard(t *testing.T) {
	router := gin.New()
	router.GET("/backups/:id", requireCanonicalBackupID, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	tests := []struct {
		name       string
		id         string
		wantStatus int
	}{
		{name: "canonical", id: "0a1b2c3d", wantStatus: http.StatusNoContent},
		{name: "removed fixed path", id: "image-storage", wantStatus: http.StatusNotFound},
		{name: "uppercase", id: "0A1B2C3D", wantStatus: http.StatusNotFound},
		{name: "invalid character", id: "0a1b2c3g", wantStatus: http.StatusNotFound},
		{name: "wrong length", id: "0a1b2c3", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/backups/"+tt.id, nil)
			router.ServeHTTP(w, req)
			require.Equal(t, tt.wantStatus, w.Code)
		})
	}
}

// TestRetiredAdminStatisticsNativeRoutes checks retired routes after administrator authentication.
func TestRetiredAdminStatisticsNativeRoutes(t *testing.T) {
	router := gin.New()
	checked := 0
	security := httpRouteSecurity{
		Admin: func(c *gin.Context) {
			if c.GetHeader("Authorization") != "Bearer test-admin" {
				c.AbortWithStatus(http.StatusUnauthorized)
				return
			}
			checked++
			c.Next()
		},
		Audit:  func(c *gin.Context) { c.Next() },
		StepUp: func(c *gin.Context) { c.Next() },
		Panel:  servermiddleware.NewPanelRateLimiter(servermiddleware.NewRateLimiter(nil), nil),
	}
	routeInventoryMount[adminRouteMount](t, provideAdminRouteMount)(router.Group("/api/v1"), security, func(c *gin.Context) { c.Status(http.StatusOK) })
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/v1/admin/groups/2/stats", http.StatusNotFound},
		{"/api/v1/admin/redeem-codes/stats", http.StatusBadRequest},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer test-admin")
			router.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code)
			if tc.status == http.StatusBadRequest {
				require.Contains(t, rec.Body.String(), "Invalid redeem code ID")
			}
		})
	}
	// Gin returns 404 for absent routes; the redeem-code wildcard route authenticates first.
	require.Equal(t, 1, checked)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes/stats", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestOpsAdminRoutesRequireAdminAuthentication(t *testing.T) {
	router := gin.New()
	handlers := &routeTestHandlers{Admin: &routeTestAdminHandlers{Ops: opshttp.NewOpsHandler(nil)}}
	adminAuth := identityhttp.AdminAuthMiddleware(func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			httpx.AbortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "Authorization required")
			return
		}
		httpx.AbortWithError(c, http.StatusForbidden, "FORBIDDEN", "Admin access required")
	})
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := identityhttp.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })
	RegisterAdminRoutes(router.Group("/api/v1"), handlers, adminAuth, auditLog, stepUp, nil, func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, path := range []string{
		"/api/v1/admin/ops/ingress-rejections",
		"/api/v1/admin/ops/ingress-rejections/health",
		"/api/v1/admin/ops/dashboard/token-stats",
		"/api/v1/admin/ops/dashboard/openai-token-stats",
	} {
		for _, tc := range []struct {
			name       string
			auth       string
			wantStatus int
		}{
			{name: "unauthenticated", wantStatus: http.StatusUnauthorized},
			{name: "non-admin", auth: "Bearer user-token", wantStatus: http.StatusForbidden},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, path, nil)
				if tc.auth != "" {
					request.Header.Set("Authorization", tc.auth)
				}
				router.ServeHTTP(recorder, request)
				require.Equal(t, tc.wantStatus, recorder.Code)
			})
		}
	}
}
