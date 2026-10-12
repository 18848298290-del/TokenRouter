package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// batchDeleteAdminService records deletion concurrency and results for handler tests.
type batchDeleteAdminService struct {
	ProviderManagement

	mu               sync.Mutex
	active           int
	maxActive        int
	deletedIDs       []int64
	deleteErrorsByID map[int64]error
	providersByID    map[int64]*providercore.Record
}

func TestProviderHandlerBatchDeleteReturnsStablePerProviderResults(t *testing.T) {
	adminSvc := &batchDeleteAdminService{
		deleteErrorsByID: map[int64]error{
			3: errors.New("delete failed"),
		},
	}
	router := setupProviderBatchDeleteRouter(adminSvc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/providers/batch-delete",
		bytes.NewBufferString(`{"provider_ids":[5,4,3,2,1,2,0,-1]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Data struct {
			Total      int     `json:"total"`
			Success    int     `json:"success"`
			Failed     int     `json:"failed"`
			SuccessIDs []int64 `json:"success_ids"`
			FailedIDs  []int64 `json:"failed_ids"`
			Errors     []struct {
				ProviderID int64  `json:"provider_id"`
				Error      string `json:"error"`
			} `json:"errors"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, 5, payload.Data.Total)
	require.Equal(t, 4, payload.Data.Success)
	require.Equal(t, 1, payload.Data.Failed)
	require.Equal(t, []int64{1, 2, 4, 5}, payload.Data.SuccessIDs)
	require.Equal(t, []int64{3}, payload.Data.FailedIDs)
	require.Equal(t, int64(3), payload.Data.Errors[0].ProviderID)
	require.Equal(t, "delete failed", payload.Data.Errors[0].Error)
	require.LessOrEqual(t, adminSvc.maxActive, 5)
	require.Greater(t, adminSvc.maxActive, 1)
}

func TestProviderHandlerBatchDeleteDoesNotRaceSelectedShadowWithParent(t *testing.T) {
	parentID := int64(1)
	adminSvc := &batchDeleteAdminService{
		providersByID: map[int64]*providercore.Record{
			1: {ID: 1},
			2: {ID: 2, ParentProviderID: &parentID},
			3: {ID: 3},
		},
	}
	router := setupProviderBatchDeleteRouter(adminSvc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/providers/batch-delete",
		bytes.NewBufferString(`{"provider_ids":[1,2,3]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Data struct {
			SuccessIDs []int64 `json:"success_ids"`
			FailedIDs  []int64 `json:"failed_ids"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, []int64{1, 2, 3}, payload.Data.SuccessIDs)
	require.Empty(t, payload.Data.FailedIDs)
	require.ElementsMatch(t, []int64{1, 3}, adminSvc.deletedIDs)
}

func TestProviderHandlerBatchDeleteRejectsEmptyNormalizedIDs(t *testing.T) {
	adminSvc := &batchDeleteAdminService{}
	router := setupProviderBatchDeleteRouter(adminSvc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/providers/batch-delete",
		bytes.NewBufferString(`{"provider_ids":[0,-1]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestProviderHandlerBatchRefreshSupportsQoderCosy(t *testing.T) {
	exchange := &providercore.ManualCredentialExchange{Qoder: func(_ context.Context, value *providercore.Record) (map[string]any, error) {
		require.Equal(t, "old-refresh", value.GetCredential("refresh_token"))
		require.Equal(t, "old-token", value.GetCredential("security_oauth_token"))
		require.Equal(t, "machine-1", value.GetCredential("machine_id"))
		return map[string]any{"security_oauth_token": "new-token", "refresh_token": "new-refresh", "machine_id": "machine-1"}, nil
	}}
	adminSvc := newManagementMutationFixture()
	adminSvc.providers = []providercore.Record{{
		ID:       44,
		Name:     "qoder",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
		},
	}}
	router := setupProviderMutationContractRouter(adminSvc)
	managed := newManagedRefreshFixture(adminSvc, exchange)
	handler := NewManagementHandler(adminSvc, ManagementOptions{Batch: providercore.NewManagementBatch(adminSvc, managed)})
	router.POST("/api/v1/admin/providers/batch-refresh", handler.BatchRefresh)

	body, _ := json.Marshal(map[string]any{"provider_ids": []int64{44}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-refresh", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, float64(0), resp["code"])
	data, ok := resp["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(1), data["success"])
	require.Equal(t, float64(0), data["failed"])
	require.Equal(t, "new-token", adminSvc.updateProviderInput.Credentials["security_oauth_token"])
	require.Equal(t, "new-refresh", adminSvc.updateProviderInput.Credentials["refresh_token"])
}

func (s *batchDeleteAdminService) GetProvidersByIDs(_ context.Context, ids []int64) ([]*providercore.Record, error) {
	providers := make([]*providercore.Record, 0, len(ids))
	for _, id := range ids {
		if s.providersByID != nil {
			if provider, ok := s.providersByID[id]; ok {
				providers = append(providers, provider)
			}
			continue
		}
		providers = append(providers, &providercore.Record{ID: id})
	}
	return providers, nil
}

func (s *batchDeleteAdminService) DeleteProvider(ctx context.Context, id int64) error {
	s.mu.Lock()
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
		return ctx.Err()
	case <-time.After(10 * time.Millisecond):
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	s.deletedIDs = append(s.deletedIDs, id)
	return s.deleteErrorsByID[id]
}

func setupProviderBatchDeleteRouter(adminSvc *batchDeleteAdminService) *gin.Engine {
	router := gin.New()
	handler := NewManagementHandler(adminSvc, ManagementOptions{Batch: providercore.NewManagementBatch(adminSvc, nil)})
	router.POST("/api/v1/admin/providers/batch-delete", handler.BatchDelete)
	return router
}
