package httpapi

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

type grokQuotaHandlerProviderRepo struct {
	providercore.GrokRateLimitWriter
	provider *providercore.Record
	updates  map[int64]map[string]any
	mu       sync.Mutex
}

type grokQuotaHandlerUpstream struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
}

type grokOAuthReconcilerStub struct {
	input  providercore.GrokOAuthReconcileInput
	calls  int
	result *providercore.GrokOAuthReconcileResult
	err    error
}

type grokOAuthHandlerClient struct{}

type grokImportOAuthClientStub struct{}

type grokImportProbeStub struct {
	mu           sync.Mutex
	calls        map[int64]int
	failures     map[int64]error
	active       int
	maxActive    int
	deadlineSeen bool
	block        <-chan struct{}
	started      chan int64
	done         chan int64
}

func TestGrokOAuthHandlerQueryQuotaProbesUpstream(t *testing.T) {
	repo := &grokQuotaHandlerProviderRepo{provider: &providercore.Record{
		ID:          42,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}}
	upstream := &grokQuotaHandlerUpstream{}
	tokens := &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}
	quotaService := provideradapter.NewGrokQuota(repo, tokens, &provideradapter.GrokQuotaTransport{Do: upstream.Do, MapStatus: forward.MapStatus}, nil)
	t.Cleanup(func() { require.NoError(t, quotaService.StopContext(context.Background())) })
	handler := NewGrokOAuthHandler(nil, nil, quotaService, GrokOAuthHTTPOptions{})

	router := gin.New()
	router.GET("/api/v1/admin/grok/providers/:id/quota", handler.QueryQuota)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/grok/providers/42/quota", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"source":"hybrid_probe"`)
	require.Contains(t, rec.Body.String(), `"billing":`)
	require.Contains(t, rec.Body.String(), `"snapshot":`)
	require.Contains(t, rec.Body.String(), `"headers_observed":true`)
	require.NotContains(t, rec.Body.String(), "access-token")
	require.Eventually(t, func() bool {
		upstream.mu.Lock()
		defer upstream.mu.Unlock()
		return len(upstream.requests) == 4
	}, time.Second, 10*time.Millisecond)
	upstream.mu.Lock()
	requests := append([]*http.Request(nil), upstream.requests...)
	bodies := append([][]byte(nil), upstream.bodies...)
	upstream.mu.Unlock()
	require.Len(t, requests, 4)
	responsesProbeSeen := false
	modelsSyncSeen := false
	for i, upstreamReq := range requests {
		require.Equal(t, "Bearer access-token", upstreamReq.Header.Get("Authorization"))
		if upstreamReq.URL.String() == xai.DefaultCLIBaseURL+"/responses" {
			responsesProbeSeen = true
			require.Equal(t, "application/json, text/event-stream", upstreamReq.Header.Get("Accept"))
			require.Contains(t, string(bodies[i]), `"model":"grok-4.5"`)
			require.Contains(t, string(bodies[i]), `"input":"hi"`)
			require.Contains(t, string(bodies[i]), `"stream":true`)
			require.NotContains(t, string(bodies[i]), `"max_output_tokens"`)
			require.NotContains(t, string(bodies[i]), `"store"`)
		}
		if upstreamReq.URL.String() == xai.DefaultCLIBaseURL+"/models" {
			modelsSyncSeen = true
		}
	}
	require.True(t, responsesProbeSeen)
	require.True(t, modelsSyncSeen)
	// Synchronize with persistence through the shared lock; a request count alone does not prove the write completed.
	repo.mu.Lock()
	require.NotNil(t, repo.updates[42])
	repo.mu.Unlock()
}

func TestGrokOAuthHandlerResetQuotaReturnsUnsupported(t *testing.T) {
	repo := &grokQuotaHandlerProviderRepo{provider: &providercore.Record{
		ID:       43,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
	}}
	quotaService := provideradapter.NewGrokQuota(repo, nil, nil, nil)
	t.Cleanup(func() { require.NoError(t, quotaService.StopContext(context.Background())) })
	handler := NewGrokOAuthHandler(nil, nil, quotaService, GrokOAuthHTTPOptions{})

	router := gin.New()
	router.POST("/api/v1/admin/grok/providers/:id/reset-quota", handler.ResetQuota)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/grok/providers/43/reset-quota", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	require.Contains(t, rec.Body.String(), `"reason":"GROK_QUOTA_RESET_UNSUPPORTED"`)
	require.NotContains(t, rec.Body.String(), "access-token")
}

func TestGrokOAuthHandlerRuntimeSanityDoesNotExposeSecrets(t *testing.T) {
	t.Setenv(xai.EnvBaseURL, "http://127.0.0.1:8080/v1?access_token=secret")
	t.Setenv(xai.EnvClientID, "client-secret-like-value")

	handler := NewGrokOAuthHandler(nil, nil, nil, GrokOAuthHTTPOptions{RuntimeSanity: func() any { return xai.RuntimeSanity() }})
	router := gin.New()
	router.GET("/api/v1/admin/grok/runtime-sanity", handler.RuntimeSanity)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/grok/runtime-sanity", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"public_gateway_scope":"responses_only"`)
	require.Contains(t, rec.Body.String(), `"valid":false`)
	require.NotContains(t, rec.Body.String(), "access_token")
	require.NotContains(t, rec.Body.String(), "secret")
	require.NotContains(t, rec.Body.String(), "client-secret-like-value")
}

func TestGrokOAuthHandlerValidateSSOTokenReturnsTokenInfo(t *testing.T) {
	oauthClient := &grokOAuthHandlerClient{}
	oauthService := newGrokAuthorizationForTest(nil, oauthClient)
	oauthService.Start()
	defer stopGrokAuthorizationForTest(t, oauthService)
	handler := NewGrokOAuthHandler(oauthService, nil, nil, GrokOAuthHTTPOptions{})

	router := gin.New()
	router.POST("/api/v1/admin/grok/oauth/sso-token", handler.ValidateSSOToken)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/grok/oauth/sso-token", strings.NewReader(`{"sso_token":"sso-token"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"access_token":"access-token"`)
	require.NotContains(t, rec.Body.String(), `"sso_token"`)
}

func TestGrokOAuthHandlerAuthorizePasswordReturnsTokenInfoWithoutPassword(t *testing.T) {
	oauthClient := &grokOAuthHandlerClient{}
	oauthService := newGrokAuthorizationForTest(nil, oauthClient, true)
	oauthService.Start()
	defer stopGrokAuthorizationForTest(t, oauthService)
	handler := NewGrokOAuthHandler(oauthService, nil, nil, GrokOAuthHTTPOptions{})

	router := gin.New()
	router.POST("/api/v1/admin/grok/oauth/password", handler.AuthorizePassword)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/grok/oauth/password", strings.NewReader(`{"email":"user@example.com","password":"super-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"access_token":"access-token"`)
	require.NotContains(t, rec.Body.String(), "super-secret")
}

func TestGrokOAuthHandlerPasswordCapabilityDefaultsToDisabled(t *testing.T) {
	oauthService := newGrokAuthorizationForTest(nil, &grokOAuthHandlerClient{})
	oauthService.Start()
	defer stopGrokAuthorizationForTest(t, oauthService)
	handler := NewGrokOAuthHandler(oauthService, nil, nil, GrokOAuthHTTPOptions{})

	router := gin.New()
	router.GET("/api/v1/admin/grok/oauth/capabilities", handler.GetCapabilities)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/grok/oauth/capabilities", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"password_auth_enabled":false`)
}

func TestGrokOAuthHandlerReconcileDefaultsToDryRun(t *testing.T) {
	reconciler := &grokOAuthReconcilerStub{result: &providercore.GrokOAuthReconcileResult{
		DryRun:      true,
		Scanned:     2,
		Actionable:  1,
		WouldBlock:  1,
		Items:       []providercore.GrokOAuthReconcileItem{{ProviderID: 42, Reason: providercore.GrokOAuthReconcileReasonMissingRefreshToken, Action: providercore.GrokOAuthReconcileActionBlock, Outcome: providercore.GrokOAuthReconcileOutcomePlanned}},
		NextAfterID: 0,
	}}
	handler := NewGrokOAuthHandler(nil, nil, nil, GrokOAuthHTTPOptions{Reconciler: reconciler})
	router := gin.New()
	router.POST("/api/v1/admin/grok/oauth/reconcile", handler.ReconcileOAuthProviders)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/grok/oauth/reconcile", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, reconciler.calls)
	require.True(t, reconciler.input.DryRun)
	require.False(t, reconciler.input.Apply)
	require.Contains(t, rec.Body.String(), `"reason":"missing_refresh_token"`)
	require.NotContains(t, rec.Body.String(), `"refresh_token":`)
	require.NotContains(t, rec.Body.String(), `"access_token":`)
}

func TestGrokOAuthHandlerReconcileRequiresExplicitApply(t *testing.T) {
	reconciler := &grokOAuthReconcilerStub{}
	handler := NewGrokOAuthHandler(nil, nil, nil, GrokOAuthHTTPOptions{Reconciler: reconciler})
	router := gin.New()
	router.POST("/api/v1/admin/grok/oauth/reconcile", handler.ReconcileOAuthProviders)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/grok/oauth/reconcile", strings.NewReader(`{"dry_run":false}`))
	req.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Zero(t, reconciler.calls)
	require.NotContains(t, rec.Body.String(), "credentials")
}

func TestGrokOAuthHandlerReconcileExplicitApply(t *testing.T) {
	reconciler := &grokOAuthReconcilerStub{result: &providercore.GrokOAuthReconcileResult{DryRun: false, Refreshed: 1}}
	handler := NewGrokOAuthHandler(nil, nil, nil, GrokOAuthHTTPOptions{Reconciler: reconciler})
	router := gin.New()
	router.POST("/api/v1/admin/grok/oauth/reconcile", handler.ReconcileOAuthProviders)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/grok/oauth/reconcile", strings.NewReader(`{"apply":true,"dry_run":false,"after_id":10,"limit":25,"refresh_window_seconds":3600}`))
	req.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, reconciler.calls)
	require.True(t, reconciler.input.Apply)
	require.False(t, reconciler.input.DryRun)
	require.Equal(t, int64(10), reconciler.input.AfterID)
	require.Equal(t, 25, reconciler.input.Limit)
	require.Equal(t, time.Hour, reconciler.input.RefreshWindow)
}

func TestGrokSSOBatchImportKeepsCreatedProvidersWhenOneAutomaticProbeFails(t *testing.T) {
	adminService := newGrokImportAdminService()
	oauthService := newGrokAuthorizationForTest(nil, grokImportOAuthClientStub{})
	oauthService.Start()
	defer stopGrokAuthorizationForTest(t, oauthService)
	prober := newGrokImportProbeStub(3)
	prober.failures[502] = apperror.New(502, "GROK_TEST_PROBE_FAILED", "sensitive-upstream-body")
	queue := providercore.NewGrokImportProbeScheduler(providercore.GrokImportProbeOptions{Concurrency: 3, Timeout: 25 * time.Second})
	var tasks sync.WaitGroup
	t.Cleanup(func() { tasks.Wait(); require.NoError(t, queue.StopContext(context.Background())) })
	imports := providercore.NewGrokProviderImport(oauthService, providercore.GrokProviderImportOptions{
		Get: adminService.GetProvider, Create: adminService.CreateProvider, Update: adminService.UpdateProvider,
		NormalizeToken: xai.NormalizeSSOToken, LogError: func(string, ...any) {},
		Schedule: func(value *providercore.Record) {
			snapshot := value.RoutingSnapshot()
			queue.Schedule(prober, &snapshot)
		},
		RunTask: func(_ string, run func()) { ; tasks.Go(func() { ; run() }) },
	})
	handler := NewGrokOAuthHandler(oauthService, imports, nil, GrokOAuthHTTPOptions{})

	router := gin.New()
	router.POST("/api/v1/admin/grok/sso-to-oauth", handler.CreateProvidersFromSSO)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/grok/sso-to-oauth",
		strings.NewReader(`{"sso_tokens":["sso-one","sso-two","sso-three"]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"created"`)
	require.NotContains(t, recorder.Body.String(), `GROK_TEST_PROBE_FAILED`)
	for range 3 {
		awaitGrokProbeSignal(t, prober.done)
	}
	calls, _, _ := prober.snapshot()
	require.Equal(t, map[int64]int{501: 1, 502: 1, 503: 1}, calls)
}

func (r *grokQuotaHandlerProviderRepo) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	if r.provider != nil && r.provider.ID == id {
		return r.provider, nil
	}
	return nil, providercore.ErrProviderNotFound
}

func (r *grokQuotaHandlerProviderRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updates == nil {
		r.updates = make(map[int64]map[string]any)
	}
	r.updates[id] = updates
	return nil
}

func (s *grokOAuthReconcilerStub) ReconcileGrokOAuth(_ context.Context, input providercore.GrokOAuthReconcileInput) (*providercore.GrokOAuthReconcileResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

func (u *grokQuotaHandlerUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	u.mu.Lock()
	u.requests = append(u.requests, req)
	u.bodies = append(u.bodies, body)
	u.mu.Unlock()
	if req.URL.Path == "/v1/responses" {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"X-Ratelimit-Limit-Requests":     []string{"10"},
				"X-Ratelimit-Remaining-Requests": []string{"8"},
			},
			Body: io.NopCloser(strings.NewReader(`{"id":"resp_probe"}`)),
		}, nil
	}
	payload := `{"config":{"billingPeriodStart":"2026-07-01T00:00:00Z","billingPeriodEnd":"2026-08-01T00:00:00Z"}}`
	if req.URL.RawQuery == "format=credits" {
		payload = `{"config":{"currentPeriod":{"type":"WEEKLY","start":"2026-07-09T03:25:00Z","end":"2026-07-16T03:25:00Z"}}}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
}

func (u *grokQuotaHandlerUpstream) DoWithTLS(
	req *http.Request,
	proxyURL string,
	providerID int64,
	providerConcurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	return u.Do(req, proxyURL, providerID, providerConcurrency)
}

func (c *grokOAuthHandlerClient) ExchangeCode(context.Context, string, string, string, string, string) (*xai.TokenResponse, error) {
	return nil, errors.New("unexpected exchange")
}

func (c *grokOAuthHandlerClient) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token", ExpiresIn: 3600}, nil
}

func (c *grokOAuthHandlerClient) LoginWithPassword(_ context.Context, email, _ string, _ string) (*providercore.GrokPasswordLoginResult, error) {
	return &providercore.GrokPasswordLoginResult{
		Email:    email,
		SSOToken: "sso-from-password",
	}, nil
}

func (c *grokOAuthHandlerClient) ConvertSSOToBuild(context.Context, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token", ExpiresIn: 3600}, nil
}

// newGrokAuthorizationForTest builds authorization with the application's option adapter.
func newGrokAuthorizationForTest(proxies egress.ProxyRepository, client providercore.GrokAuthorizationClient, enabled ...bool) *providercore.GrokAuthorization {
	return providercore.NewGrokAuthorization(client, provideradapter.GrokAuthorizationOptions(proxies, func() bool {
		return len(enabled) > 0 && enabled[0]
	}))
}

func stopGrokAuthorizationForTest(t *testing.T, authorization *providercore.GrokAuthorization) {
	t.Helper()
	require.NoError(t, authorization.StopContext(context.Background()))
}

func (grokImportOAuthClientStub) ExchangeCode(context.Context, string, string, string, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token", ExpiresIn: 3600}, nil
}

func (grokImportOAuthClientStub) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token", ExpiresIn: 3600}, nil
}

func (grokImportOAuthClientStub) LoginWithPassword(context.Context, string, string, string) (*providercore.GrokPasswordLoginResult, error) {
	return nil, errors.New("unexpected password login")
}

func (grokImportOAuthClientStub) ConvertSSOToBuild(context.Context, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{AccessToken: "access-token", RefreshToken: "refresh-token", ExpiresIn: 3600}, nil
}

func newGrokImportProbeStub(buffer int) *grokImportProbeStub {
	return &grokImportProbeStub{
		calls:    make(map[int64]int),
		failures: make(map[int64]error),
		started:  make(chan int64, buffer),
		done:     make(chan int64, buffer),
	}
}

func (s *grokImportProbeStub) QueryQuota(ctx context.Context, providerID int64) (*providercore.GrokImportProbeResult, error) {
	_, deadlineSeen := ctx.Deadline()
	s.mu.Lock()
	s.calls[providerID]++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.deadlineSeen = s.deadlineSeen || deadlineSeen
	s.mu.Unlock()

	s.started <- providerID
	var ctxErr error
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			ctxErr = ctx.Err()
		}
	}

	s.mu.Lock()
	s.active--
	failure := s.failures[providerID]
	s.mu.Unlock()
	s.done <- providerID
	if ctxErr != nil {
		return nil, ctxErr
	}
	if failure != nil {
		return nil, failure
	}
	return &providercore.GrokImportProbeResult{
		Model:      "grok-4.5",
		StatusCode: 200,
	}, nil
}

func (s *grokImportProbeStub) snapshot() (map[int64]int, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := make(map[int64]int, len(s.calls))
	maps.Copy(calls, s.calls)
	return calls, s.maxActive, s.deadlineSeen
}

func awaitGrokProbeSignal(t *testing.T, signals <-chan int64) int64 {
	t.Helper()
	select {
	case id := <-signals:
		return id
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Grok import probe")
		return 0
	}
}
