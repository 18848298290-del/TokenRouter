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

// Batch deletion must map root-provider outcomes back to selected credential
// shadows while admitting unrelated roots concurrently. management_batch.go is
// exercised through ManagementBatch with sorted result assertions, duplicate
// and invalid IDs, and selected parent relationships supplied by a controlled
// store.
//
// The deletion fixture publishes active counters under a mutex before waiting
// on a short delay or cancellation. Completed attempts update their slices
// under the same mutex, and the handler returns after batch joining. This
// controlled overlap supports the concurrency assertions while leaving durable
// deletion and actual database cascade behavior to their owning store tests.

// batchDeleteAdminService records synchronized deletion overlap and supplies
// selected parent relationships for batches.
type batchDeleteAdminService struct {
	ProviderManagement // unused embedded administrator operations

	mu sync.Mutex // deletion observation synchronization mutex
	// active, maxActive and deletedIDs are protected by mu during concurrent
	// deletion calls. Tests read their completed observations after
	// ManagementBatch has joined its worker operations.
	active           int                            // current logical deletion calls
	maxActive        int                            // maximum overlapping deletion calls
	deletedIDs       []int64                        // joined logical deletion attempts
	deleteErrorsByID map[int64]error                // immutable selected deletion failures
	providersByID    map[int64]*providercore.Record // immutable selected record pointers
}

// TestProviderHandlerBatchDeleteReturnsStablePerProviderResults submits
// unsorted duplicates and invalid IDs with one injected deletion failure. The
// normalized response returns stable success and failure order, and fixture
// overlap stays between two and five active deletions.
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
		Data struct { // decoded HTTP result object
			Total      int        `json:"total"`       // normalized selected provider count
			Success    int        `json:"success"`     // reported successful provider count
			Failed     int        `json:"failed"`      // reported failed provider count
			SuccessIDs []int64    `json:"success_ids"` // ordered successful provider identifiers
			FailedIDs  []int64    `json:"failed_ids"`  // ordered failed provider identifiers
			Errors     []struct { // ordered provider failure details
				ProviderID int64  `json:"provider_id"` // failed selected provider identifier
				Error      string `json:"error"`       // recorded provider failure message
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

// TestProviderHandlerBatchDeleteDoesNotRaceSelectedShadowWithParent selects a
// parent, its shadow and an unrelated provider together. Root deletion calls
// occur for parent and unrelated IDs, while the response maps successful parent
// deletion back to the selected shadow.
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
		Data struct { // decoded HTTP result object
			SuccessIDs []int64 `json:"success_ids"` // ordered successful provider identifiers
			FailedIDs  []int64 `json:"failed_ids"`  // ordered failed provider identifiers
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, []int64{1, 2, 3}, payload.Data.SuccessIDs)
	require.Empty(t, payload.Data.FailedIDs)
	require.ElementsMatch(t, []int64{1, 3}, adminSvc.deletedIDs)
}

// TestProviderHandlerBatchDeleteRejectsEmptyNormalizedIDs submits a nonempty
// list containing only invalid nonpositive IDs. The handler returns 400 after
// normalization leaves no provider selection for the deletion use case.
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

// TestProviderHandlerBatchRefreshSupportsQoderCosy refreshes a Qoder COSY
// fixture with stored security token, refresh token and machine identity. The
// response counts one success and captured credential updates contain the newly
// exchanged token values.
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

	// The selection contains JSON integer IDs. An encoding failure would leave
	// an empty body, making the later HTTP-success assertion reject this fixture setup.
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

// GetProvidersByIDs returns configured borrowed records when its map is
// present, skipping missing entries. Without that map it creates ID-only
// records, letting deletion tests choose whether parent relationships are
// available.
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

// DeleteProvider records overlapping logical deletion calls and resolves
// cancellation against a controlled delay. Active and maximum counts are
// updated under mu before waiting, making concurrent batch admission
// observable. Cancellation decreases active before returning its error.
//
// After the delay, the fixture records the attempted ID and selected failure
// under the same mutex. The delayed call is a test interleaving, and the
// returned error models store failure rather than a durable deletion. The test
// inspects captured slices after the batch has joined its calls.
func (s *batchDeleteAdminService) DeleteProvider(ctx context.Context, id int64) error {
	s.mu.Lock()
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()

	// Keep deletion calls overlapping long enough to observe bounded batch
	// admission, while cancellation returns its active-count ownership before reporting failure.
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

// setupProviderBatchDeleteRouter mounts only normalized batch deletion with the
// supplied fixture administrator. Production ManagementBatch handles parent
// selection and concurrency; unrelated HTTP dependencies remain unused by this
// narrow request route.
func setupProviderBatchDeleteRouter(adminSvc *batchDeleteAdminService) *gin.Engine {
	router := gin.New()
	handler := NewManagementHandler(adminSvc, ManagementOptions{Batch: providercore.NewManagementBatch(adminSvc, nil)})
	router.POST("/api/v1/admin/providers/batch-delete", handler.BatchDelete)
	return router
}
