package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// credentialFieldRaceAdmin rotates tokens and configuration after the initial read to test concurrent updates.
type credentialFieldRaceAdmin struct {
	ProviderManagement
	current provider.Record
}

// failingAdminService fails UpdateProvider for the configured provider ID.
type failingAdminService struct {
	*managementCredentialFixture
	failOnProviderID int64
	updateCallCount  atomic.Int64
}

// getProviderFailingService returns not found for the configured provider ID.
type getProviderFailingService struct {
	*managementCredentialFixture
	failOnProviderID int64
}

// managementCredentialFixture supplies reads and updates for batch credential tests.
type managementCredentialFixture struct{ ProviderManagement }

func TestBatchCredentialFieldDoesNotRestoreOldSnapshot(t *testing.T) {
	admin := &credentialFieldRaceAdmin{current: provider.Record{ID: 984, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"refresh_token": "old-token", "base_url": "https://old.invalid", "org_uuid": "old-org"}}}
	h := NewManagementHandler(admin, ManagementOptions{Batch: provider.NewManagementBatch(admin, nil)})
	router := gin.New()
	router.POST("/batch", h.BatchUpdateCredentials)
	request := httptest.NewRequest(http.MethodPost, "/batch", bytes.NewBufferString(`{"provider_ids":[984],"field":"org_uuid","value":"new-org"}`))
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, request)
	require.Equal(t, http.StatusOK, result.Code)
	require.Equal(t, "rotated", admin.current.Credentials["refresh_token"])
	require.Equal(t, "https://new.invalid", admin.current.Credentials["base_url"])
	require.Equal(t, "new-org", admin.current.Credentials["org_uuid"])
}

func TestBatchUpdateCredentials_AllSuccess(t *testing.T) {
	svc := &failingAdminService{managementCredentialFixture: &managementCredentialFixture{}}
	router, _ := setupProviderHandlerWithService(svc)

	body, _ := json.Marshal(BatchUpdateCredentialsRequest{
		ProviderIDs: []int64{1, 2, 3},
		Field:       "account_uuid",
		Value:       "test-uuid",
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "全部成功时应返回 200")
	require.Equal(t, int64(3), svc.updateCallCount.Load(), "应调用 3 次 UpdateProvider")
}

func TestBatchUpdateCredentials_PartialFailure(t *testing.T) {
	// Fail the second provider's update so later updates still run.
	svc := &failingAdminService{
		managementCredentialFixture: &managementCredentialFixture{},
		failOnProviderID:            2,
	}
	router, _ := setupProviderHandlerWithService(svc)

	body, _ := json.Marshal(BatchUpdateCredentialsRequest{
		ProviderIDs: []int64{1, 2, 3},
		Field:       "org_uuid",
		Value:       "test-org",
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// Partial success returns HTTP 200 with per-item results.
	require.Equal(t, http.StatusOK, w.Code, "批量更新返回 200 + 成功/失败明细")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := testassert.MustType[map[string]any](resp["data"])
	require.Equal(t, float64(2), data["success"], "应有 2 个成功")
	require.Equal(t, float64(1), data["failed"], "应有 1 个失败")

	// Every provider is attempted, including those after a failure.
	require.Equal(t, int64(3), svc.updateCallCount.Load(),
		"应调用 3 次 UpdateProvider（逐个尝试，失败后继续）")
}

func TestBatchUpdateCredentials_FirstProviderNotFound(t *testing.T) {
	// Override the fixture's successful reads to exercise a missing provider during prevalidation.
	svc := &getProviderFailingService{
		managementCredentialFixture: &managementCredentialFixture{},
		failOnProviderID:            1,
	}
	router, _ := setupProviderHandlerWithService(svc)

	body, _ := json.Marshal(BatchUpdateCredentialsRequest{
		ProviderIDs: []int64{1, 2, 3},
		Field:       "account_uuid",
		Value:       "test",
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, "第一阶段验证失败应返回 404")
}

func TestBatchUpdateCredentials_InterceptWarmupRequests_NonBool(t *testing.T) {
	svc := &failingAdminService{managementCredentialFixture: &managementCredentialFixture{}}
	router, _ := setupProviderHandlerWithService(svc)

	// A string value for the boolean intercept_warmup_requests field is rejected with 400.
	body, _ := json.Marshal(map[string]any{
		"provider_ids": []int64{1},
		"field":        "intercept_warmup_requests",
		"value":        "not-a-bool",
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code,
		"intercept_warmup_requests 传入非 bool 值应返回 400")
}

func TestBatchUpdateCredentials_InterceptWarmupRequests_ValidBool(t *testing.T) {
	svc := &failingAdminService{managementCredentialFixture: &managementCredentialFixture{}}
	router, _ := setupProviderHandlerWithService(svc)

	body, _ := json.Marshal(map[string]any{
		"provider_ids": []int64{1},
		"field":        "intercept_warmup_requests",
		"value":        true,
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"intercept_warmup_requests 传入合法 bool 值应返回 200")
}

func TestBatchUpdateCredentials_AccountUUID_NonString(t *testing.T) {
	svc := &failingAdminService{managementCredentialFixture: &managementCredentialFixture{}}
	router, _ := setupProviderHandlerWithService(svc)

	// A numeric account_uuid is rejected with 400.
	body, _ := json.Marshal(map[string]any{
		"provider_ids": []int64{1},
		"field":        "account_uuid",
		"value":        12345,
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code,
		"account_uuid 传入非 string 值应返回 400")
}

func TestBatchUpdateCredentials_AccountUUID_NullValue(t *testing.T) {
	svc := &failingAdminService{managementCredentialFixture: &managementCredentialFixture{}}
	router, _ := setupProviderHandlerWithService(svc)

	// A null account_uuid clears the field and is accepted.
	body, _ := json.Marshal(map[string]any{
		"provider_ids": []int64{1},
		"field":        "account_uuid",
		"value":        nil,
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"account_uuid 传入 null 应返回 200")
}

func (s *credentialFieldRaceAdmin) GetProvider(context.Context, int64) (*provider.Record, error) {
	v := s.current
	v.Credentials = make(map[string]any, len(s.current.Credentials))
	maps.Copy(v.Credentials, s.current.Credentials)
	return &v, nil
}

func (s *credentialFieldRaceAdmin) UpdateProvider(_ context.Context, _ int64, input *provider.UpdateProviderInput) (*provider.Record, error) {
	s.current.Credentials = map[string]any{"refresh_token": "rotated", "base_url": "https://new.invalid", "org_uuid": "old-org"}
	maps.Copy(s.current.Credentials, input.Credentials)
	return &s.current, nil
}

func (f *failingAdminService) UpdateProvider(ctx context.Context, id int64, input *provider.UpdateProviderInput) (*provider.Record, error) {
	f.updateCallCount.Add(1)
	if id == f.failOnProviderID {
		return nil, errors.New("database error")
	}
	return f.managementCredentialFixture.UpdateProvider(ctx, id, input)
}

func setupProviderHandlerWithService(adminSvc ProviderManagement) (*gin.Engine, *ManagementHandler) {
	router := gin.New()
	handler := NewManagementHandler(adminSvc, ManagementOptions{Batch: provider.NewManagementBatch(adminSvc, nil)})
	router.POST("/api/v1/admin/providers/batch-update-credentials", handler.BatchUpdateCredentials)
	return router, handler
}

func (f *getProviderFailingService) GetProvider(ctx context.Context, id int64) (*provider.Record, error) {
	if id == f.failOnProviderID {
		return nil, errors.New("not found")
	}
	return f.managementCredentialFixture.GetProvider(ctx, id)
}

func (s *managementCredentialFixture) GetProvider(_ context.Context, id int64) (*provider.Record, error) {
	return &provider.Record{ID: id, Name: "provider", Platform: provider.PlatformAnthropic, Type: provider.ProviderTypeOAuth, Status: provider.StatusActive}, nil
}

func (s *managementCredentialFixture) UpdateProvider(_ context.Context, id int64, input *provider.UpdateProviderInput) (*provider.Record, error) {
	return &provider.Record{ID: id, Name: input.Name, Platform: provider.PlatformAnthropic, Type: input.Type, Status: provider.StatusActive, Credentials: input.Credentials}, nil
}
