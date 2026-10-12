package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/backup"
)

// apiEnvelope decodes shared HTTP status and payload fields for compatibility assertions.
type apiEnvelope struct {
	Code    int             `json:"code"`    // response envelope status code
	Message string          `json:"message"` // response envelope display message
	Reason  string          `json:"reason"`  // structured availability error reason
	Data    json.RawMessage `json:"data"`    // encoded health payload JSON
}

// TestDataManagementHandler_AgentHealthAlways200 queries a retired facade whose
// diagnostic socket is absent. HTTP and envelope status must still indicate
// success while health data reports disabled deprecation and the configured path.
func TestDataManagementHandler_AgentHealthAlways200(t *testing.T) {
	svc := backup.NewDataManagementServiceWithOptions(filepath.Join(t.TempDir(), "missing.sock"), 50*time.Millisecond)
	h := NewDataManagementHandler(svc)

	r := gin.New()
	r.GET("/api/v1/admin/data-management/agent/health", h.GetAgentHealth)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/data-management/agent/health", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var envelope apiEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Equal(t, 0, envelope.Code)

	var data struct {
		Enabled    bool   `json:"enabled"`     // reported agent availability flag
		Reason     string `json:"reason"`      // reported disabled service reason
		SocketPath string `json:"socket_path"` // reported diagnostic socket path
	}
	require.NoError(t, json.Unmarshal(envelope.Data, &data))
	require.False(t, data.Enabled)
	require.Equal(t, backup.DataManagementDeprecatedReason, data.Reason)
	require.Equal(t, svc.SocketPath(), data.SocketPath)
}

// TestDataManagementHandler_NonHealthRouteReturns503WhenDisabled requests legacy
// configuration from the retired facade. The response must report service
// unavailability in HTTP and envelope status with the deprecation reason.
func TestDataManagementHandler_NonHealthRouteReturns503WhenDisabled(t *testing.T) {
	svc := backup.NewDataManagementServiceWithOptions(filepath.Join(t.TempDir(), "missing.sock"), 50*time.Millisecond)
	h := NewDataManagementHandler(svc)

	r := gin.New()
	r.GET("/api/v1/admin/data-management/config", h.GetConfig)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/data-management/config", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var envelope apiEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Equal(t, http.StatusServiceUnavailable, envelope.Code)
	require.Equal(t, backup.DataManagementDeprecatedReason, envelope.Reason)
}
