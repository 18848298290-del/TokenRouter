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

// OpenAI reset consumption and its later recovery and snapshot stages report
// separate administrator outcomes. openai_oauth.go is exercised with serial
// fixtures that record stage counts and context state, protecting visible
// partial success and the detached post-consumption cleanup path.
//
// The canceled-client case deliberately uses a consumption fixture that ignores
// cancellation, representing an accepted upstream result before cleanup. Query
// and write fixtures likewise record context state rather than enforcing
// network behavior. Shadow creation tests use controlled administrative results
// and strict binding, so their assertions establish HTTP presentation and error
// mapping rather than parent eligibility or durable creation.

// openAIQuotaWorkflowStub records serial quota-stage calls and context
// observations with configured outcomes. Returned usage and reset pointers are
// borrowed, and counters represent logical fixture calls rather than network
// attempts or durable writes.
type openAIQuotaWorkflowStub struct {
	resetResult *openai.OpenAIQuotaResetResult // borrowed configured credit-consumption result
	resetErr    error                          // configured logical consumption failure
	queryResult *openai.OpenAIQuotaUsage       // borrowed configured live quota observation
	queryErr    error                          // configured logical quota query failure
	cacheErr    error                          // configured logical snapshot-write failure

	resetCalls          int   // serial logical credit-consumption count
	queryCalls          int   // serial logical quota-query count
	cacheCalls          int   // serial logical snapshot-write count
	cacheCreditsCalls   int   // serial logical credit-snapshot count
	cachePostResetCalls int   // serial logical full-snapshot count
	queryCtxErr         error // query-time context error observation
	cacheCtxErr         error // write-time context error observation
}

// openAIProviderStateRecovererStub captures serial recovery dispatch and its
// context state for reset workflows.
type openAIProviderStateRecovererStub struct {
	err         error                                // configured logical recovery/read failure
	calls       int                                  // serial logical operation count
	providerID  int64                                // captured TokenRouter provider identifier
	lastOptions providercore.ProviderRecoveryOptions // captured runtime recovery options
	lastCtxErr  error                                // recovery-time context error observation
}

// openAIResetAdminServiceStub supplies a borrowed configured provider during
// serial post-reset rereading.
type openAIResetAdminServiceStub struct {
	OpenAIAdminOperations                      // unused embedded administrator operations
	provider              *providercore.Record // borrowed provider reread result
	err                   error                // configured logical recovery/read failure
	calls                 int                  // serial logical operation count
}

// openAIQuotaResetEnvelope decodes the typed reset outcome inside the common
// success envelope.
type openAIQuotaResetEnvelope struct {
	Data OpenAIQuotaResetResponse `json:"data"` // decoded typed HTTP action outcome
}

// openAIQuotaRefreshEnvelope decodes live usage and snapshot outcome inside the
// common success envelope.
type openAIQuotaRefreshEnvelope struct {
	Data OpenAIQuotaRefreshResponse `json:"data"` // decoded typed HTTP action outcome
}

// openAIShadowFixture returns controlled shadow output while its other embedded
// administrator methods remain unused.
type openAIShadowFixture struct {
	OpenAIAdminOperations       // unused embedded administrator operations
	createSparkShadowErr  error // configured shadow creation failure
}

// TestOpenAIResetQuotaRecoversProviderBeforeRefreshingCache runs a successful
// reset with recovery and snapshot fixtures. The response reports completed
// recovery and cache refresh, includes quota and a redacted provider, and
// retains the administrator scheduling switch.
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

// TestOpenAIResetQuotaRecoveryFailureStopsPostProcessing injects state recovery
// failure after successful reset consumption. HTTP returns the recovery
// warning, marks recovery incomplete and records no subsequent quota query,
// cache write or provider reread.
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

// TestOpenAIResetQuotaCacheFailureStillReturnsRecoveredProvider injects a
// snapshot write failure after successful state recovery. The response retains
// the recovered provider, omits the unsaved quota and reports separate recovery
// and cache outcome flags.
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

// TestOpenAIResetQuotaPostProcessingSurvivesClientCancellation uses an
// already-canceled HTTP context with a reset fixture that accepts consumption.
// The recorded recovery, query and cache context-error fields remain nil,
// and the response remains successful through this controlled accepted-reset
// fixture.
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

// TestOpenAIRefreshQuotaPersistFailureStillReturnsUsage injects reset-credit
// snapshot persistence failure after a live quota query. HTTP retains the
// queried timestamp and available count with cache_persisted=false and no full
// post-reset snapshot call.
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

// TestNewOpenAIOAuthHandlerKeepsNilQuotaCapabilitiesGuarded constructs a
// handler with quota and recovery dependencies absent. Its fields remain nil,
// preserving the configuration state that quota endpoints inspect before using
// their constructed action wrappers.
func TestNewOpenAIOAuthHandlerKeepsNilQuotaCapabilitiesGuarded(t *testing.T) {
	handler := NewOpenAIOAuthHandler(nil, &openAIResetAdminServiceStub{}, nil, nil, OpenAIHTTPOptions{})

	require.Nil(t, handler.Quota)
	require.Nil(t, handler.Recovery)
}

// TestCreateShadow_ReturnsCreatedShadow submits shadow scheduling and group
// input for a numeric parent path. The response retains the requested name and
// identifies that parent together with the Spark quota dimension.
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

// TestCreateShadow_InvalidID submits a shadow request against a nonnumeric
// parent path. HTTP returns 400 before the otherwise configured shadow fixture
// can supply a creation result.
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

// TestCreateShadow_ServiceError injects an unclassified shadow-creation failure
// from administration. The handler maps that failure to HTTP 500 through the
// shared error response helper.
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

// TestCreateShadow_BadBody submits malformed JSON to a numeric shadow-creation
// route. Strict request binding rejects it with 400 before administration
// receives any usable creation input.
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

// ResetCredit records a serial logical consumption call and returns its
// configured result and error. It ignores context cancellation, allowing the
// canceled-client test to model consumption that has already succeeded.
func (s *openAIQuotaWorkflowStub) ResetCredit(context.Context, int64) (*openai.OpenAIQuotaResetResult, error) {
	s.resetCalls++
	return s.resetResult, s.resetErr
}

// QueryUsage records one logical query and captures context error at
// invocation. The configured result pointer is borrowed, and this serial
// substitute performs no upstream request or cancellation enforcement.
func (s *openAIQuotaWorkflowStub) QueryUsage(ctx context.Context, _ int64) (*openai.OpenAIQuotaUsage, error) {
	s.queryCalls++
	s.queryCtxErr = ctx.Err()
	return s.queryResult, s.queryErr
}

// CacheResetCreditsSnapshot counts a logical credit-snapshot write and captures
// context error at invocation. It returns the configured persistence failure,
// allowing tests to distinguish live query results from successful snapshot
// storage.
func (s *openAIQuotaWorkflowStub) CacheResetCreditsSnapshot(ctx context.Context, _ int64, _ *openai.OpenAIRateLimitResetCredits) error {
	s.cacheCalls++
	s.cacheCreditsCalls++
	s.cacheCtxErr = ctx.Err()
	return s.cacheErr
}

// CachePostResetSnapshot counts a logical full post-reset snapshot write and
// captures context error at invocation. The same configured error controls this
// serial write, with no external cache state recorded.
func (s *openAIQuotaWorkflowStub) CachePostResetSnapshot(ctx context.Context, _ int64, _ *openai.OpenAIQuotaUsage) error {
	s.cacheCalls++
	s.cachePostResetCalls++
	s.cacheCtxErr = ctx.Err()
	return s.cacheErr
}

// RecoverProviderState records provider ID, options and context error and
// returns a fixed recovery observation plus its configured error. It models
// logical dispatch, leaving actual runtime and token-cache mutations to
// recovery tests.
func (s *openAIProviderStateRecovererStub) RecoverProviderState(ctx context.Context, providerID int64, options providercore.ProviderRecoveryOptions) (*providercore.SuccessfulTestRecovery, error) {
	s.calls++
	s.providerID = providerID
	s.lastOptions = options
	s.lastCtxErr = ctx.Err()
	return &providercore.SuccessfulTestRecovery{}, s.err
}

// GetProvider counts a serial provider reread and returns the configured
// borrowed record and error. The response assertions use this pointer to
// distinguish successful recovery from failed later presentation reads.
func (s *openAIResetAdminServiceStub) GetProvider(context.Context, int64) (*providercore.Record, error) {
	s.calls++
	return s.provider, s.err
}

// performOpenAIQuotaResetRequest mounts the reset route, optionally installs
// the supplied HTTP context and decodes its response. It returns status and
// typed action outcome, failing the test if response JSON cannot be parsed.
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

// performOpenAIQuotaRefreshRequest mounts the quota-refresh route and decodes
// its recorded response into the typed refresh outcome. The caller receives
// status and cache-persistence flag along with live usage for assertions.
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

// successfulOpenAIQuotaWorkflowStub creates accepted reset consumption and a
// live usage observation with an empty credit collection. Callers can override
// query or cache failures before their serial HTTP request executes.
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

// recoveredOpenAIProviderStub supplies an active OpenAI OAuth record whose
// administrator scheduling switch is false. The reset assertions observe that
// policy value independently from recovered runtime state.
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

// CreateShadow returns its configured error or a fixed shadow record derived
// from selected options. Group and parent pointers remain fixture values; this
// substitute returns creation output without validating actual parent
// eligibility.
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
