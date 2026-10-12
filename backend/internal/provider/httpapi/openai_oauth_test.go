package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type openAIQuotaWorkflowStub struct {
	resetResult *openai.OpenAIQuotaResetResult
	resetErr    error
	queryResult *openai.OpenAIQuotaUsage
	queryErr    error
	cacheErr    error

	resetCalls          int
	queryCalls          int
	cacheCalls          int
	cacheCreditsCalls   int
	cachePostResetCalls int
	queryCtxErr         error
	cacheCtxErr         error
}

type openAIProviderStateRecovererStub struct {
	err         error
	calls       int
	providerID  int64
	lastOptions providercore.ProviderRecoveryOptions
	lastCtxErr  error
}

type openAIResetAdminServiceStub struct {
	OpenAIAdminOperations
	provider *providercore.Record
	err      error
	calls    int
}

type openAIQuotaResetEnvelope struct {
	Data OpenAIQuotaResetResponse `json:"data"`
}

type openAIQuotaRefreshEnvelope struct {
	Data OpenAIQuotaRefreshResponse `json:"data"`
}

// openAIShadowFixture returns a shadow creation result or the configured error.
type openAIShadowFixture struct {
	OpenAIAdminOperations
	createSparkShadowErr error
}

func TestOpenAIResetQuotaRecoversProviderBeforeRefreshingCache(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflowStub()
	recoverer := &openAIProviderStateRecovererStub{}
	adminService := recoveredOpenAIProviderStub()
	handler := &OpenAIOAuthHandler{
		Admin:    adminService,
		Quota:    quota,
		Recovery: recoverer,
	}

	status, envelope := performOpenAIQuotaResetRequest(t, handler, nil)

	require.Equal(t, http.StatusOK, status)
	require.Empty(t, envelope.Data.WarningCode)
	require.True(t, envelope.Data.ProviderStateRecovered)
	require.True(t, envelope.Data.CacheRefreshed)
	require.NotNil(t, envelope.Data.Quota)
	require.NotNil(t, envelope.Data.Provider)
	require.False(t, envelope.Data.Provider.Schedulable, "不得改动人工调度开关")
	require.Equal(t, int64(42), recoverer.providerID)
	require.True(t, recoverer.lastOptions.InvalidateToken)
	require.Equal(t, 1, quota.resetCalls)
	require.Equal(t, 1, quota.queryCalls)
	require.Equal(t, 1, quota.cacheCalls)
	require.Zero(t, quota.cacheCreditsCalls, "重置后应写入完整 usage 快照，不应只写 credits")
	require.Equal(t, 1, quota.cachePostResetCalls)
	require.Equal(t, 1, adminService.calls)
}

func TestOpenAIResetQuotaRecoveryFailureStopsPostProcessing(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflowStub()
	recoverer := &openAIProviderStateRecovererStub{err: errors.New("recovery failed")}
	adminService := recoveredOpenAIProviderStub()
	handler := &OpenAIOAuthHandler{
		Admin:    adminService,
		Quota:    quota,
		Recovery: recoverer,
	}

	status, envelope := performOpenAIQuotaResetRequest(t, handler, nil)

	require.Equal(t, http.StatusOK, status)
	require.Equal(t, OpenAIQuotaResetWarningProviderRecoveryFailed, envelope.Data.WarningCode)
	require.False(t, envelope.Data.ProviderStateRecovered)
	require.Zero(t, quota.queryCalls)
	require.Zero(t, quota.cacheCalls)
	require.Zero(t, adminService.calls)
}

func TestOpenAIResetQuotaCacheFailureStillReturnsRecoveredProvider(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflowStub()
	quota.cacheErr = errors.New("cache write failed")
	recoverer := &openAIProviderStateRecovererStub{}
	adminService := recoveredOpenAIProviderStub()
	handler := &OpenAIOAuthHandler{
		Admin:    adminService,
		Quota:    quota,
		Recovery: recoverer,
	}

	status, envelope := performOpenAIQuotaResetRequest(t, handler, nil)

	require.Equal(t, http.StatusOK, status)
	require.Equal(t, OpenAIQuotaResetWarningCacheRefreshFailed, envelope.Data.WarningCode)
	require.True(t, envelope.Data.ProviderStateRecovered)
	require.False(t, envelope.Data.CacheRefreshed)
	require.Nil(t, envelope.Data.Quota)
	require.NotNil(t, envelope.Data.Provider)
}

func TestOpenAIResetQuotaPostProcessingSurvivesClientCancellation(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflowStub()
	recoverer := &openAIProviderStateRecovererStub{}
	adminService := recoveredOpenAIProviderStub()
	handler := &OpenAIOAuthHandler{
		Admin:    adminService,
		Quota:    quota,
		Recovery: recoverer,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	status, _ := performOpenAIQuotaResetRequest(t, handler, ctx)

	require.Equal(t, http.StatusOK, status)
	require.NoError(t, recoverer.lastCtxErr)
	require.NoError(t, quota.queryCtxErr)
	require.NoError(t, quota.cacheCtxErr)
}

func TestOpenAIRefreshQuotaPersistFailureStillReturnsUsage(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflowStub()
	quota.queryResult = &openai.OpenAIQuotaUsage{
		FetchedAt:             456,
		RateLimitResetCredits: &openai.OpenAIRateLimitResetCredits{AvailableCount: 2},
	}
	quota.cacheErr = errors.New("expiration details unavailable")
	handler := &OpenAIOAuthHandler{
		Admin: &openAIResetAdminServiceStub{},
		Quota: quota,
	}

	status, envelope := performOpenAIQuotaRefreshRequest(t, handler)

	require.Equal(t, http.StatusOK, status)
	require.False(t, envelope.Data.CachePersisted)
	require.Equal(t, int64(456), envelope.Data.FetchedAt)
	require.NotNil(t, envelope.Data.RateLimitResetCredits)
	require.Equal(t, 2, envelope.Data.RateLimitResetCredits.AvailableCount)
	require.Equal(t, 1, quota.cacheCreditsCalls, "手动 refresh 只应更新 credits 快照")
	require.Zero(t, quota.cachePostResetCalls)
}

func TestNewOpenAIOAuthHandlerKeepsNilQuotaCapabilitiesGuarded(t *testing.T) {
	handler := NewOpenAIOAuthHandler(nil, &openAIResetAdminServiceStub{}, nil, nil, OpenAIHTTPOptions{})

	require.Nil(t, handler.Quota)
	require.Nil(t, handler.Recovery)
}

func TestCreateShadow_ReturnsCreatedShadow(t *testing.T) {
	stub := &openAIShadowFixture{}
	h := NewOpenAIOAuthHandler(nil, stub, nil, nil, OpenAIHTTPOptions{})

	router := gin.New()
	router.POST("/api/v1/admin/providers/:id/shadow", h.CreateShadow)

	body := `{"name":"p-spark","priority":50,"concurrency":2,"group_ids":[10,20]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/42/shadow", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	data, ok := resp["data"].(map[string]any)
	require.True(t, ok, "response should have data field")

	// The response identifies the parent selected by the path parameter.
	pid, ok := data["parent_provider_id"].(float64)
	require.True(t, ok, "parent_provider_id should be present")
	require.Equal(t, float64(42), pid)

	require.Equal(t, providercore.QuotaDimensionSpark, data["quota_dimension"])

	require.Equal(t, "p-spark", data["name"])
}

func TestCreateShadow_InvalidID(t *testing.T) {
	h := NewOpenAIOAuthHandler(nil, &openAIShadowFixture{}, nil, nil, OpenAIHTTPOptions{})

	router := gin.New()
	router.POST("/api/v1/admin/providers/:id/shadow", h.CreateShadow)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/not-a-number/shadow",
		strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateShadow_ServiceError(t *testing.T) {
	stub := &openAIShadowFixture{createSparkShadowErr: errors.New("database unavailable")}
	h := NewOpenAIOAuthHandler(nil, stub, nil, nil, OpenAIHTTPOptions{})

	router := gin.New()
	router.POST("/api/v1/admin/providers/:id/shadow", h.CreateShadow)

	body := `{"name":"p-spark","priority":50}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/42/shadow", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	// An unclassified service error maps to HTTP 500 through ErrorFrom.
	require.GreaterOrEqual(t, rec.Code, http.StatusBadRequest)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestCreateShadow_BadBody(t *testing.T) {
	h := NewOpenAIOAuthHandler(nil, &openAIShadowFixture{}, nil, nil, OpenAIHTTPOptions{})

	router := gin.New()
	router.POST("/api/v1/admin/providers/:id/shadow", h.CreateShadow)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/42/shadow",
		strings.NewReader(`{not valid json`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func (s *openAIQuotaWorkflowStub) ResetCredit(context.Context, int64) (*openai.OpenAIQuotaResetResult, error) {
	s.resetCalls++
	return s.resetResult, s.resetErr
}

func (s *openAIQuotaWorkflowStub) QueryUsage(ctx context.Context, _ int64) (*openai.OpenAIQuotaUsage, error) {
	s.queryCalls++
	s.queryCtxErr = ctx.Err()
	return s.queryResult, s.queryErr
}

func (s *openAIQuotaWorkflowStub) CacheResetCreditsSnapshot(ctx context.Context, _ int64, _ *openai.OpenAIRateLimitResetCredits) error {
	s.cacheCalls++
	s.cacheCreditsCalls++
	s.cacheCtxErr = ctx.Err()
	return s.cacheErr
}

func (s *openAIQuotaWorkflowStub) CachePostResetSnapshot(ctx context.Context, _ int64, _ *openai.OpenAIQuotaUsage) error {
	s.cacheCalls++
	s.cachePostResetCalls++
	s.cacheCtxErr = ctx.Err()
	return s.cacheErr
}

func (s *openAIProviderStateRecovererStub) RecoverProviderState(ctx context.Context, providerID int64, options providercore.ProviderRecoveryOptions) (*providercore.SuccessfulTestRecovery, error) {
	s.calls++
	s.providerID = providerID
	s.lastOptions = options
	s.lastCtxErr = ctx.Err()
	return &providercore.SuccessfulTestRecovery{}, s.err
}

func (s *openAIResetAdminServiceStub) GetProvider(context.Context, int64) (*providercore.Record, error) {
	s.calls++
	return s.provider, s.err
}

func performOpenAIQuotaResetRequest(t *testing.T, handler *OpenAIOAuthHandler, ctx context.Context) (int, openAIQuotaResetEnvelope) {
	t.Helper()

	router := gin.New()
	router.POST("/api/v1/admin/openai/providers/:id/reset-quota", handler.ResetQuota)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/openai/providers/42/reset-quota", nil)
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	router.ServeHTTP(recorder, request)

	var envelope openAIQuotaResetEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return recorder.Code, envelope
}

func performOpenAIQuotaRefreshRequest(t *testing.T, handler *OpenAIOAuthHandler) (int, openAIQuotaRefreshEnvelope) {
	t.Helper()

	router := gin.New()
	router.POST("/api/v1/admin/openai/providers/:id/quota/refresh", handler.RefreshQuota)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/openai/providers/42/quota/refresh", nil)
	router.ServeHTTP(recorder, request)

	var envelope openAIQuotaRefreshEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return recorder.Code, envelope
}

func successfulOpenAIQuotaWorkflowStub() *openAIQuotaWorkflowStub {
	return &openAIQuotaWorkflowStub{
		resetResult: &openai.OpenAIQuotaResetResult{Code: "success", WindowsReset: 1},
		queryResult: &openai.OpenAIQuotaUsage{
			FetchedAt: 123,
			RateLimitResetCredits: &openai.OpenAIRateLimitResetCredits{
				AvailableCount: 0,
				Credits:        []openai.OpenAIRateLimitResetCreditDetail{},
			},
		},
	}
}

func recoveredOpenAIProviderStub() *openAIResetAdminServiceStub {
	return &openAIResetAdminServiceStub{provider: &providercore.Record{
		ID:          42,
		Name:        "recovered",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: false,
	}}
}

func (s *openAIShadowFixture) CreateShadow(ctx context.Context, parentID int64, opts providercore.ShadowOptions) (*providercore.Record, error) {
	if s.createSparkShadowErr != nil {
		return nil, s.createSparkShadowErr
	}
	pid := parentID
	return &providercore.Record{
		ID:               9001,
		Name:             opts.Name,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		Priority:         opts.Priority,
		Concurrency:      opts.Concurrency,
		GroupIDs:         opts.GroupIDs,
		ParentProviderID: &pid,
		QuotaDimension:   providercore.QuotaDimensionSpark,
		Credentials:      map[string]any{},
		Extra:            map[string]any{},
	}, nil
}
