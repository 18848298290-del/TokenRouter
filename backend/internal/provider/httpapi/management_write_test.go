package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	idempotencytest "github.com/TokenFlux/TokenRouter/internal/idempotency/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Duplicate creation can succeed before response idempotency completion is
// stored, so HTTP recovery needs the same administrator operation identity.
// management_write.go is exercised with controlled completion failures and one
// blocked first request, while separate creation and update assertions check
// retained configuration and credential redaction.
//
// Serial fixtures retain borrowed provider results and capture actor/key values
// for recovery. The competing-request fixture signals creation start, blocks on
// a test-owned channel and exposes atomic counts while the second request runs.
// The test releases and joins the first response, establishing completion
// before its final assertions rather than treating elapsed time as proof of
// creation.

// duplicateProviderAdminServiceStub keeps serial creation and recovery
// observations for one configured provider. Captured scope and key strings
// belong to each call, while the configured provider pointer is borrowed by
// responses.
type duplicateProviderAdminServiceStub struct {
	ProviderManagement                      // unused embedded administrator operations
	provider           *providercore.Record // borrowed configured duplicate record
	calls              int                  // logical duplicate invocation count
	recoverCalls       int                  // logical recovery invocation count
	providerID         int64                // last source provider identifier
	actorScope         string               // captured creation administrator scope
	operationKey       string               // captured creation idempotency key
	recoverScope       string               // captured recovery administrator scope
	recoverKey         string               // captured recovery idempotency key
	recoverErr         error                // configured recovery lookup failure
	created            bool                 // serial duplicate completion marker
}

// blockingDuplicateAdminServiceStub coordinates one blocked creation with a
// competing HTTP request. Test-owned start and release channels publish its
// phase, and atomic call counters remain readable while the original request is
// active.
type blockingDuplicateAdminServiceStub struct {
	ProviderManagement                      // unused embedded administrator operations
	provider           *providercore.Record // borrowed configured duplicate record
	started            chan struct{}        // single creation-start notification channel
	release            chan struct{}        // test-owned creation continuation channel
	calls              atomic.Int32         // logical duplicate invocation count
	recoverCalls       atomic.Int32         // logical recovery invocation count
	recoverErr         error                // configured recovery lookup failure
}

// failOnceMarkSucceededRepo delegates to memory storage after one serial
// injected completion-write failure.
type failOnceMarkSucceededRepo struct {
	*idempotencytest.MemoryStore      // shared in-memory idempotency implementation
	failNext                     bool // serial one-shot write failure flag
}

// TestProviderCreateWithoutAutomaticGrokProbeServiceStillSucceeds creates a
// Grok OAuth provider with no automatic-probe callback bound. The HTTP request
// still returns success through administration and response presentation with
// the supplied fixture.
func TestProviderCreateWithoutAutomaticGrokProbeServiceStillSucceeds(t *testing.T) {
	source := newGrokImportAdminService()
	presenter := NewRuntimePresenter(providercore.NewRuntimeStatusReader(providercore.RuntimeStatusOptions{}), source, nil)
	handler := NewManagementHandler(source, ManagementOptions{Presenter: presenter, Privacy: source})

	router := gin.New()
	router.POST("/api/v1/admin/providers", handler.Create)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/providers",
		strings.NewReader(`{"name":"grok-rt","platform":"grok","type":"oauth","credentials":{"refresh_token":"secret"}}`),
	)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
}

// TestProviderUpdateDeprecatedOnlyIsNormalizedToNoExtraUpdate submits an Extra
// object containing only a malformed retired setting. The handler succeeds and
// captures a nil Extra update, retaining omission rather than replacing
// configuration with an empty object.
func TestProviderUpdateDeprecatedOnlyIsNormalizedToNoExtraUpdate(t *testing.T) {
	stub := newManagementMutationFixture()
	handler := newMutationHandler(stub, nil)
	router := gin.New()
	router.PUT("/providers/:id", handler.Update)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/providers/1", bytes.NewBufferString(
		`{"extra":{"openai_long_context_billing_enabled":{"malformed":true}}}`,
	))
	request.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NotNil(t, stub.updateProviderInput)
	require.Nil(t, stub.updateProviderInput.Extra)
}

// TestDuplicateProviderHandlerRedactsCredentials returns a duplicated provider
// with a secret API key and disabled scheduling. The HTTP response includes the
// duplicate name, retains its scheduling switch and removes the secret value.
func TestDuplicateProviderHandlerRedactsCredentials(t *testing.T) {
	svc := &duplicateProviderAdminServiceStub{
		provider: &providercore.Record{
			ID:          43,
			Name:        "primary (Copy)",
			Platform:    capability.PlatformAnthropic,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: false,
			Credentials: map[string]any{"api_key": "top-secret-key"},
		},
	}
	router := setupDuplicateProviderRouter(t, svc)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/42/duplicate", nil)

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, svc.calls)
	require.Contains(t, recorder.Body.String(), `"name":"primary (Copy)"`)
	require.NotContains(t, recorder.Body.String(), "top-secret-key")
	var responseBody struct {
		Data struct { // decoded duplicate response object
			Credentials map[string]any `json:"credentials"` // redacted duplicate credential object
			Schedulable bool           `json:"schedulable"` // returned administrator scheduling switch
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &responseBody))
	require.Empty(t, responseBody.Data.Credentials)
	require.False(t, responseBody.Data.Schedulable)
}

// TestDuplicateProviderHandlerRejectsInvalidID requests duplication for a
// nonnumeric path identifier. HTTP returns 400 and the duplicate fixture
// records zero creation calls before any idempotency operation can run.
func TestDuplicateProviderHandlerRejectsInvalidID(t *testing.T) {
	svc := &duplicateProviderAdminServiceStub{}
	router := setupDuplicateProviderRouter(t, svc)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/not-a-number/duplicate", nil)

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, svc.calls)
}

// TestDuplicateProviderHandlerReplaysSameIdempotencyKey submits the same
// duplication request twice under one administrator key. Creation occurs once,
// the fixture captures the source and actor scope, and the repeated response
// carries the replay header.
func TestDuplicateProviderHandlerReplaysSameIdempotencyKey(t *testing.T) {
	svc := &duplicateProviderAdminServiceStub{
		provider: &providercore.Record{
			ID:          43,
			Name:        "primary (Copy)",
			Platform:    capability.PlatformAnthropic,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: false,
		},
	}
	repo := idempotencytest.NewMemoryStore()
	coordinator := idempotency.NewIdempotencyCoordinator(repo, idempotency.DefaultIdempotencyConfig())
	router := setupDuplicateProviderRouter(t, svc, coordinator)

	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/42/duplicate", nil)
		request.Header.Set("Idempotency-Key", "duplicate-provider-42")
		router.ServeHTTP(recorder, request)
		return recorder
	}

	first := call()
	second := call()

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, 1, svc.calls)
	require.Equal(t, int64(42), svc.providerID)
	require.Equal(t, "admin:77", svc.actorScope)
	require.Equal(t, "duplicate-provider-42", svc.operationKey)
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
}

// TestDuplicateProviderHandlerRecoversAfterMarkSucceededFailure injects failure
// while saving a completed duplicate response to idempotency storage. Both
// request outcomes recover the durable fixture result by actor and key, while
// the creation side effect remains counted once.
func TestDuplicateProviderHandlerRecoversAfterMarkSucceededFailure(t *testing.T) {
	svc := &duplicateProviderAdminServiceStub{
		provider: &providercore.Record{
			ID:          43,
			Name:        "primary (Copy)",
			Platform:    capability.PlatformAnthropic,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: false,
		},
	}
	repo := &failOnceMarkSucceededRepo{MemoryStore: idempotencytest.NewMemoryStore(), failNext: true}
	coordinator := idempotency.NewIdempotencyCoordinator(repo, idempotency.DefaultIdempotencyConfig())
	router := setupDuplicateProviderRouter(t, svc, coordinator)

	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/42/duplicate", nil)
		request.Header.Set("Idempotency-Key", "duplicate-provider-42-recovery")
		router.ServeHTTP(recorder, request)
		return recorder
	}

	first := call()
	second := call()

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "true", first.Header().Get("X-Idempotency-Recovered"))
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Recovered"))
	require.Equal(t, 1, svc.calls, "ambiguous retries must not repeat the create side effect")
	require.Equal(t, 2, svc.recoverCalls)
	require.Equal(t, "admin:77", svc.recoverScope)
	require.Equal(t, "duplicate-provider-42-recovery", svc.recoverKey)
	require.Contains(t, second.Body.String(), `"id":43`)
}

// TestDuplicateProviderHandlerPreservesIdempotencyErrorWhenRecoveryLookupFails
// injects both response-cache completion failure and duplicate recovery lookup
// failure. HTTP retains the original unavailable-store reason after one
// creation and one recovery attempt.
func TestDuplicateProviderHandlerPreservesIdempotencyErrorWhenRecoveryLookupFails(t *testing.T) {
	svc := &duplicateProviderAdminServiceStub{
		provider: &providercore.Record{
			ID:          43,
			Name:        "primary (Copy)",
			Platform:    capability.PlatformAnthropic,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: false,
		},
		recoverErr: errors.New("recovery database unavailable"),
	}
	repo := &failOnceMarkSucceededRepo{MemoryStore: idempotencytest.NewMemoryStore(), failNext: true}
	coordinator := idempotency.NewIdempotencyCoordinator(repo, idempotency.DefaultIdempotencyConfig())
	router := setupDuplicateProviderRouter(t, svc, coordinator)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/42/duplicate", nil)
	request.Header.Set("Idempotency-Key", "duplicate-provider-42-recovery-error")

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), "IDEMPOTENCY_STORE_UNAVAILABLE")
	require.Equal(t, 1, svc.calls)
	require.Equal(t, 1, svc.recoverCalls)
	require.Equal(t, "admin:77", svc.recoverScope)
}

// TestDuplicateProviderHandlerDoesNotReexecuteWhileOriginalIsProcessing blocks
// the first duplication call and sends another request with the same key. The
// second returns the in-progress conflict, then releasing the first joins one
// successful creation with one recovery lookup.
func TestDuplicateProviderHandlerDoesNotReexecuteWhileOriginalIsProcessing(t *testing.T) {
	svc := &blockingDuplicateAdminServiceStub{
		provider: &providercore.Record{
			ID:          43,
			Name:        "primary (Copy)",
			Platform:    capability.PlatformAnthropic,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: false,
		},
		started:    make(chan struct{}),
		release:    make(chan struct{}),
		recoverErr: errors.New("recovery database unavailable"),
	}
	coordinator := idempotency.NewIdempotencyCoordinator(idempotencytest.NewMemoryStore(), idempotency.DefaultIdempotencyConfig())
	router := setupDuplicateProviderRouter(t, svc, coordinator)

	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/42/duplicate", nil)
		request.Header.Set("Idempotency-Key", "duplicate-provider-42-active")
		router.ServeHTTP(recorder, request)
		return recorder
	}
	// Keep the first request active until the second observes in-progress.
	// The buffered result channel joins the original handler after its fixture is released.
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- call() }()
	<-svc.started

	second := call()
	require.Equal(t, http.StatusConflict, second.Code)
	require.Contains(t, second.Body.String(), "IDEMPOTENCY_IN_PROGRESS")
	require.Equal(t, int32(1), svc.calls.Load())
	require.Equal(t, int32(1), svc.recoverCalls.Load())

	close(svc.release)
	select {
	case first := <-firstDone:
		require.Equal(t, http.StatusOK, first.Code)
	case <-time.After(time.Second):
		t.Fatal("original duplicate request did not finish")
	}
}

// TestProviderHandler_Create_AnthropicAPIKeyPassthroughExtraForwarded creates
// an Anthropic API-key provider with passthrough enabled in Extra. The handler
// succeeds and captured creation input retains that platform, authentication
// kind and configured boolean.
func TestProviderHandler_Create_AnthropicAPIKeyPassthroughExtraForwarded(t *testing.T) {
	adminSvc := &managementCreateFixture{}
	handler := newManagementCreateFixtureHandler(adminSvc)

	router := gin.New()
	router.POST("/api/v1/admin/providers", handler.Create)

	body := map[string]any{
		"name":     "anthropic-key-1",
		"platform": "anthropic",
		"type":     "apikey",
		"credentials": map[string]any{
			"api_key":  "sk-ant-xxx",
			"base_url": "https://api.anthropic.com",
		},
		"extra": map[string]any{
			"anthropic_passthrough": true,
		},
		"concurrency": 1,
		"priority":    1,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, adminSvc.createdProviders, 1)

	created := adminSvc.createdProviders[0]
	require.Equal(t, "anthropic", created.Platform)
	require.Equal(t, "apikey", created.Type)
	require.NotNil(t, created.Extra)
	require.Equal(t, true, created.Extra["anthropic_passthrough"])
}

// MarkSucceeded injects one serial completion-write failure before delegating
// later calls to memory storage. The failure occurs after duplicate creation,
// modeling uncertain response-cache persistence for the recovery assertions.
func (r *failOnceMarkSucceededRepo) MarkSucceeded(ctx context.Context, id int64, responseStatus int, responseBody string, expiresAt time.Time) error {
	if r.failNext {
		r.failNext = false
		return errors.New("mark succeeded failed")
	}
	return r.MemoryStore.MarkSucceeded(ctx, id, responseStatus, responseBody, expiresAt)
}

// DuplicateProvider captures source ID, actor scope and operation key and marks
// the serial fixture created. Its configured provider pointer is returned
// unchanged, supplying a durable-result substitute for later recovery tests.
func (s *duplicateProviderAdminServiceStub) DuplicateProvider(_ context.Context, providerID int64, actorScope, operationKey string) (*providercore.Record, error) {
	s.calls++
	s.providerID = providerID
	s.actorScope = actorScope
	s.operationKey = operationKey
	s.created = true
	return s.provider, nil
}

// RecoverDuplicateProvider records actor scope and operation key during serial
// lookup. A configured error wins; otherwise only a previously created fixture
// result is returned, with nil representing no recoverable duplicate.
func (s *duplicateProviderAdminServiceStub) RecoverDuplicateProvider(_ context.Context, _ int64, actorScope, operationKey string) (*providercore.Record, error) {
	s.recoverCalls++
	s.recoverScope = actorScope
	s.recoverKey = operationKey
	if s.recoverErr != nil {
		return nil, s.recoverErr
	}
	if !s.created {
		return nil, nil
	}
	return s.provider, nil
}

// DuplicateProvider atomically counts the admitted fixture creation and signals
// its start before blocking on release. The test holds that call while sending
// a second HTTP request, so idempotency can report in-progress before the first
// result exists.
//
// Release is controlled by the test and the method deliberately ignores
// cancellation while waiting. After release it returns the borrowed configured
// provider. Channel ownership belongs to this one fixture invocation, and the
// test joins the original request before inspecting its successful response.
func (s *blockingDuplicateAdminServiceStub) DuplicateProvider(_ context.Context, _ int64, _, _ string) (*providercore.Record, error) {
	s.calls.Add(1)
	// Publish start after counting this invocation, then await the test-owned
	// release so a competing request observes an admitted creation with no result yet.
	close(s.started)
	<-s.release
	return s.provider, nil
}

// RecoverDuplicateProvider atomically counts attempted recovery and returns its
// configured failure. It supplies no successful record, forcing the handler to
// retain the active idempotency conflict during the blocked creation scenario.
func (s *blockingDuplicateAdminServiceStub) RecoverDuplicateProvider(_ context.Context, _ int64, _, _ string) (*providercore.Record, error) {
	s.recoverCalls.Add(1)
	return nil, s.recoverErr
}

// setupDuplicateProviderRouter installs a fixed authenticated administrator
// subject and mounts duplication with runtime presentation. The optional first
// coordinator enables response idempotency; later variadic values are unused by
// this fixture.
func setupDuplicateProviderRouter(t *testing.T, svc ProviderManagement, coordinators ...*idempotency.IdempotencyCoordinator) *gin.Engine {
	t.Helper()

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: 77})
		c.Next()
	})
	handler := NewManagementHandler(svc, ManagementOptions{Presenter: NewRuntimePresenter(providercore.NewRuntimeStatusReader(providercore.RuntimeStatusOptions{}), svc, nil)})
	if len(coordinators) > 0 {
		handler.BindIdempotency(coordinators[0])
	}
	router.POST("/api/v1/admin/providers/:id/duplicate", handler.Duplicate)
	return router
}
