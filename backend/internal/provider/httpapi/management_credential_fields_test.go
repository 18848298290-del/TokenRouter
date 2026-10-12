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

// Credential-field batches validate provider existence and field values before
// their per-item writes. management_credential_fields.go is exercised with one
// injected missing record, one injected write failure and a serial
// token-rotation interleaving that distinguishes a field patch from a stale
// credential snapshot.

// credentialFieldRaceAdmin models a serial credential rotation immediately
// before merging a selected patch.
type credentialFieldRaceAdmin struct {
	ProviderManagement                 // unused embedded administrative operations
	current            provider.Record // serial mutable provider fixture
}

// failingAdminService counts logical update attempts and fails one configured
// provider identifier.
type failingAdminService struct {
	*managementCredentialFixture              // borrowed successful read and write fixture
	failOnProviderID             int64        // configured failing provider identifier
	updateCallCount              atomic.Int64 // atomic logical update invocation count
}

// getProviderFailingService injects one missing-provider read during credential
// prevalidation.
type getProviderFailingService struct {
	*managementCredentialFixture       // borrowed successful read and write fixture
	failOnProviderID             int64 // configured failing provider identifier
}

// managementCredentialFixture supplies synthetic records and accepted patch
// results for credential-field tests.
type managementCredentialFixture struct {
	ProviderManagement // unused embedded administrative operations
}

// TestBatchCredentialFieldDoesNotRestoreOldSnapshot rotates tokens and base URL
// inside the fixture's credential patch write. The selected organization UUID
// changes while those newer unselected credential values remain present.
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

// TestBatchUpdateCredentials_AllSuccess patches three fixture providers with an
// account UUID. The handler succeeds and the atomic invocation counter records
// one logical update for each supplied provider.
func TestBatchUpdateCredentials_AllSuccess(t *testing.T) {
	svc := &failingAdminService{managementCredentialFixture: &managementCredentialFixture{}}
	router, _ := setupProviderHandlerWithService(svc)

	// Each configured credential value is JSON-compatible. A marshal failure
	// leaves an empty body, which binding rejects before the requested patch can run.
	body, _ := json.Marshal(BatchUpdateCredentialsRequest{
		ProviderIDs: []int64{1, 2, 3},
		Field:       "account_uuid",
		Value:       "test-uuid",
	})

	w := httptest.NewRecorder()
	// The fixed POST method and relative URL are valid for request construction.
	// Its ignored error is unrelated to the credential value subsequently checked by the handler.
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "全部成功时应返回 200")
	require.Equal(t, int64(3), svc.updateCallCount.Load(), "应调用 3 次 UpdateProvider")
}

// TestBatchUpdateCredentials_PartialFailure injects a write failure for the
// second of three providers. The response remains HTTP 200 with two successes
// and one failure, and all three logical writes are attempted.
func TestBatchUpdateCredentials_PartialFailure(t *testing.T) {
	// Fail the second provider's update so later updates still run.
	svc := &failingAdminService{
		managementCredentialFixture: &managementCredentialFixture{},
		failOnProviderID:            2,
	}
	router, _ := setupProviderHandlerWithService(svc)

	// Each configured credential value is JSON-compatible. A marshal failure
	// leaves an empty body, which binding rejects before the requested patch can run.
	body, _ := json.Marshal(BatchUpdateCredentialsRequest{
		ProviderIDs: []int64{1, 2, 3},
		Field:       "org_uuid",
		Value:       "test-org",
	})

	w := httptest.NewRecorder()
	// The fixed POST method and relative URL are valid for request construction.
	// Its ignored error is unrelated to the credential value subsequently checked by the handler.
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

// TestBatchUpdateCredentials_FirstProviderNotFound injects a missing record
// during credential prevalidation. The handler returns 404 before it can return
// per-item update results for the selected batch.
func TestBatchUpdateCredentials_FirstProviderNotFound(t *testing.T) {
	// Override the fixture's successful reads to exercise a missing provider during prevalidation.
	svc := &getProviderFailingService{
		managementCredentialFixture: &managementCredentialFixture{},
		failOnProviderID:            1,
	}
	router, _ := setupProviderHandlerWithService(svc)

	// Each configured credential value is JSON-compatible. A marshal failure
	// leaves an empty body, which binding rejects before the requested patch can run.
	body, _ := json.Marshal(BatchUpdateCredentialsRequest{
		ProviderIDs: []int64{1, 2, 3},
		Field:       "account_uuid",
		Value:       "test",
	})

	w := httptest.NewRecorder()
	// The fixed POST method and relative URL are valid for request construction.
	// Its ignored error is unrelated to the credential value subsequently checked by the handler.
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, "第一阶段验证失败应返回 404")
}

// TestBatchUpdateCredentials_InterceptWarmupRequests_NonBool supplies text for
// the boolean warmup-interception field. HTTP value validation returns 400
// before admitted provider patching can use that invalid type.
func TestBatchUpdateCredentials_InterceptWarmupRequests_NonBool(t *testing.T) {
	svc := &failingAdminService{managementCredentialFixture: &managementCredentialFixture{}}
	router, _ := setupProviderHandlerWithService(svc)

	// A string value for the boolean intercept_warmup_requests field is rejected with 400.
	// Encode the invalid or clearing value as JSON input for field validation.
	// Marshal failure would produce an empty-body binding rejection instead of reaching that value check.
	body, _ := json.Marshal(map[string]any{
		"provider_ids": []int64{1},
		"field":        "intercept_warmup_requests",
		"value":        "not-a-bool",
	})

	w := httptest.NewRecorder()
	// The fixed POST method and relative URL are valid for request construction.
	// Its ignored error is unrelated to the credential value subsequently checked by the handler.
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code,
		"intercept_warmup_requests 传入非 bool 值应返回 400")
}

// TestBatchUpdateCredentials_InterceptWarmupRequests_ValidBool supplies true
// for the boolean warmup-interception field. The handler accepts the value and
// returns HTTP success through the controlled provider patch fixture.
func TestBatchUpdateCredentials_InterceptWarmupRequests_ValidBool(t *testing.T) {
	svc := &failingAdminService{managementCredentialFixture: &managementCredentialFixture{}}
	router, _ := setupProviderHandlerWithService(svc)

	// Encode the invalid or clearing value as JSON input for field validation.
	// Marshal failure would produce an empty-body binding rejection instead of reaching that value check.
	body, _ := json.Marshal(map[string]any{
		"provider_ids": []int64{1},
		"field":        "intercept_warmup_requests",
		"value":        true,
	})

	w := httptest.NewRecorder()
	// The fixed POST method and relative URL are valid for request construction.
	// Its ignored error is unrelated to the credential value subsequently checked by the handler.
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"intercept_warmup_requests 传入合法 bool 值应返回 200")
}

// TestBatchUpdateCredentials_AccountUUID_NonString supplies a number for an
// account UUID field. Value validation returns 400, protecting the
// string-valued credential field from this JSON numeric representation.
func TestBatchUpdateCredentials_AccountUUID_NonString(t *testing.T) {
	svc := &failingAdminService{managementCredentialFixture: &managementCredentialFixture{}}
	router, _ := setupProviderHandlerWithService(svc)

	// A numeric account_uuid is rejected with 400.
	// Encode the invalid or clearing value as JSON input for field validation.
	// Marshal failure would produce an empty-body binding rejection instead of reaching that value check.
	body, _ := json.Marshal(map[string]any{
		"provider_ids": []int64{1},
		"field":        "account_uuid",
		"value":        12345,
	})

	w := httptest.NewRecorder()
	// The fixed POST method and relative URL are valid for request construction.
	// Its ignored error is unrelated to the credential value subsequently checked by the handler.
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code,
		"account_uuid 传入非 string 值应返回 400")
}

// TestBatchUpdateCredentials_AccountUUID_NullValue supplies null for an account
// UUID field. The handler admits the clearing input and returns success through
// the credential patch use case.
func TestBatchUpdateCredentials_AccountUUID_NullValue(t *testing.T) {
	svc := &failingAdminService{managementCredentialFixture: &managementCredentialFixture{}}
	router, _ := setupProviderHandlerWithService(svc)

	// A null account_uuid clears the field and is accepted.
	// Encode the invalid or clearing value as JSON input for field validation.
	// Marshal failure would produce an empty-body binding rejection instead of reaching that value check.
	body, _ := json.Marshal(map[string]any{
		"provider_ids": []int64{1},
		"field":        "account_uuid",
		"value":        nil,
	})

	w := httptest.NewRecorder()
	// The fixed POST method and relative URL are valid for request construction.
	// Its ignored error is unrelated to the credential value subsequently checked by the handler.
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/providers/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"account_uuid 传入 null 应返回 200")
}

// GetProvider returns a record copy with a copied top-level credential map.
// Nested credential values remain borrowed, and state access follows the serial
// injected-interleaving fixture used by this test.
func (s *credentialFieldRaceAdmin) GetProvider(context.Context, int64) (*provider.Record, error) {
	v := s.current
	v.Credentials = make(map[string]any, len(s.current.Credentials))
	maps.Copy(v.Credentials, s.current.Credentials)
	return &v, nil
}

// UpdateProvider installs newer unselected credentials before merging the
// supplied patch. Its returned pointer borrows serial fixture state, letting
// the assertion detect any stale snapshot mistakenly present in the patch.
func (s *credentialFieldRaceAdmin) UpdateProvider(_ context.Context, _ int64, input *provider.UpdateProviderInput) (*provider.Record, error) {
	s.current.Credentials = map[string]any{"refresh_token": "rotated", "base_url": "https://new.invalid", "org_uuid": "old-org"}
	maps.Copy(s.current.Credentials, input.Credentials)
	return &s.current, nil
}

// UpdateProvider atomically counts logical writes and injects failure for the
// selected provider. Other IDs delegate to the successful fixture, letting
// batch tests observe continued attempts after one failure.
func (f *failingAdminService) UpdateProvider(ctx context.Context, id int64, input *provider.UpdateProviderInput) (*provider.Record, error) {
	f.updateCallCount.Add(1)
	if id == f.failOnProviderID {
		return nil, errors.New("database error")
	}
	return f.managementCredentialFixture.UpdateProvider(ctx, id, input)
}

// setupProviderHandlerWithService mounts credential-field maintenance with the
// supplied administrator and production ManagementBatch. It returns the router
// and handler for controlled request tests, with unrelated handler dependencies
// left unused.
func setupProviderHandlerWithService(adminSvc ProviderManagement) (*gin.Engine, *ManagementHandler) {
	router := gin.New()
	handler := NewManagementHandler(adminSvc, ManagementOptions{Batch: provider.NewManagementBatch(adminSvc, nil)})
	router.POST("/api/v1/admin/providers/batch-update-credentials", handler.BatchUpdateCredentials)
	return router, handler
}

// GetProvider injects a read failure for its selected provider identifier.
// Other identifiers use the successful fixture read so prevalidation tests can
// isolate one missing-record outcome.
func (f *getProviderFailingService) GetProvider(ctx context.Context, id int64) (*provider.Record, error) {
	if id == f.failOnProviderID {
		return nil, errors.New("not found")
	}
	return f.managementCredentialFixture.GetProvider(ctx, id)
}

// GetProvider constructs an active Anthropic OAuth record for the requested
// identifier. Every ID is available in this fixture, whose fresh record allows
// selected-field validation without actual provider storage.
func (s *managementCredentialFixture) GetProvider(_ context.Context, id int64) (*provider.Record, error) {
	return &provider.Record{ID: id, Name: "provider", Platform: provider.PlatformAnthropic, Type: provider.ProviderTypeOAuth, Status: provider.StatusActive}, nil
}

// UpdateProvider returns a fixture record containing the supplied credential
// map and requested type. The map remains borrowed from input, and this
// substitute performs no durable configuration write.
func (s *managementCredentialFixture) UpdateProvider(_ context.Context, id int64, input *provider.UpdateProviderInput) (*provider.Record, error) {
	return &provider.Record{ID: id, Name: input.Name, Platform: provider.PlatformAnthropic, Type: input.Type, Status: provider.StatusActive, Credentials: input.Credentials}, nil
}
