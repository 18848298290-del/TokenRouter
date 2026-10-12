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

// Batch creation must forward an explicitly configured scheduler load factor
// into each administrative creation input. The management_create.go test uses a
// required Qoder COSY credential object and records the supplied pointer
// through the shared creation fixture.

// TestProviderHandler_BatchCreate_ForwardsLoadFactor submits one Qoder COSY
// provider with an explicit load factor. The HTTP batch succeeds and the
// captured administrative creation input retains the nonnil pointer value
// twenty.
func TestProviderHandler_BatchCreate_ForwardsLoadFactor(t *testing.T) {
	adminSvc := &managementCreateFixture{}
	handler := newManagementCreateFixtureHandler(adminSvc)

	router := gin.New()
	router.POST("/api/v1/admin/providers/batch", handler.BatchCreate)

	body := map[string]any{
		"providers": []map[string]any{
			{
				"name":     "qoder-cosy-1",
				"platform": "qoder",
				"type":     "cosy",
				"credentials": map[string]any{
					"pat": "pat-123",
				},
				"concurrency": 5,
				"load_factor": 20,
				"priority":    1,
			},
		},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, adminSvc.createdProviders, 1)
	require.NotNil(t, adminSvc.createdProviders[0].LoadFactor)
	require.Equal(t, 20, *adminSvc.createdProviders[0].LoadFactor)
}
