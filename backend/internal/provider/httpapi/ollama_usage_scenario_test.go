package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Shared Ollama identity resolution must expose the same usage and
// encryption-key flags across list, detail and dedicated status endpoints.
// Scenarios cover management.go, management_list.go and ollama_usage.go with
// one service instance, verifying batched group resolution and redaction using
// controlled stored snapshots.

// ollamaManagementFixture supplies serial list and detail reads for shared
// Ollama usage presentation.
type ollamaManagementFixture struct {
	ProviderManagement                       // unused embedded administrator operations
	providers          []providercore.Record // borrowed configured provider population
}

// ollamaCloudUsageHandlerTestRepo supplies borrowed serial records and counts
// shared-group resolution. All write operations are no-op fixtures, so the
// scenarios establish response consistency and grouping dispatch rather than
// durable session maintenance.
type ollamaCloudUsageHandlerTestRepo struct {
	provider          *providercore.Record   // borrowed configured single provider
	providers         []*providercore.Record // borrowed configured provider population
	groupResolveCalls int                    // serial group-resolution invocation count
}

// TestOllamaCloudUsageEncryptionKeyStateConsistentAcrossProviderResponses
// varies the configured encryption-key flag across list, detail and dedicated
// usage responses. All three views expose the same supplied service flag for
// the eligible provider.
func TestOllamaCloudUsageEncryptionKeyStateConsistentAcrossProviderResponses(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run("configured="+strconv.FormatBool(configured), func(t *testing.T) {
			provider := &providercore.Record{
				ID:          7,
				Name:        "ollama",
				Platform:    capability.PlatformOpenAI,
				Type:        capability.ProviderTypeAPIKey,
				Credentials: map[string]any{"base_url": "https://ollama.com", "api_key": "test-key"},
				Extra:       map[string]any{},
				Status:      providercore.StatusActive,
			}
			adminService := &ollamaManagementFixture{}
			adminService.providers = []providercore.Record{*provider}
			usageService := providercore.NewOllamaCloudUsageService(
				&ollamaCloudUsageHandlerTestRepo{provider: provider}, nil, nil, providercore.OllamaUsageOptions{EncryptionKeyConfigured: configured},
			)
			// Join the fixture service after its response observations. The same owner
			// serves all mounted views, so accepted operation ownership ends before fixture teardown.
			t.Cleanup(usageService.Stop)

			handler := newOllamaManagementHandler(adminService, usageService)
			router := gin.New()
			router.GET("/providers", handler.List)
			router.GET("/providers/:id", handler.GetByID)
			router.GET("/providers/:id/ollama-cloud-usage", NewOllamaUsageHandler(usageService).GetOllamaCloudUsage)

			listRecorder := httptest.NewRecorder()
			router.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/providers?page=1&page_size=20", nil))
			require.Equal(t, http.StatusOK, listRecorder.Code)
			var listPayload struct {
				Data struct { // decoded HTTP success response
					Items []struct { // returned management page items
						// returned redacted Ollama usage state
						OllamaCloudUsage *providercore.OllamaCloudUsageState `json:"ollama_cloud_usage"`
					} `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(listRecorder.Body.Bytes(), &listPayload))
			require.Len(t, listPayload.Data.Items, 1)
			require.NotNil(t, listPayload.Data.Items[0].OllamaCloudUsage)

			detailRecorder := httptest.NewRecorder()
			router.ServeHTTP(detailRecorder, httptest.NewRequest(http.MethodGet, "/providers/7", nil))
			require.Equal(t, http.StatusOK, detailRecorder.Code)
			var detailPayload struct {
				Data struct { // decoded HTTP success response
					// returned redacted Ollama usage state
					OllamaCloudUsage *providercore.OllamaCloudUsageState `json:"ollama_cloud_usage"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(detailRecorder.Body.Bytes(), &detailPayload))
			require.NotNil(t, detailPayload.Data.OllamaCloudUsage)

			stateRecorder := httptest.NewRecorder()
			router.ServeHTTP(stateRecorder, httptest.NewRequest(http.MethodGet, "/providers/7/ollama-cloud-usage", nil))
			require.Equal(t, http.StatusOK, stateRecorder.Code)
			var statePayload struct {
				Data providercore.OllamaCloudUsageState `json:"data"` // decoded HTTP success response
			}
			require.NoError(t, json.Unmarshal(stateRecorder.Body.Bytes(), &statePayload))

			listConfigured := listPayload.Data.Items[0].OllamaCloudUsage.EncryptionKeyConfigured
			detailConfigured := detailPayload.Data.OllamaCloudUsage.EncryptionKeyConfigured
			require.Equal(t, configured, listConfigured)
			require.Equal(t, statePayload.Data.EncryptionKeyConfigured, listConfigured)
			require.Equal(t, statePayload.Data.EncryptionKeyConfigured, detailConfigured)
		})
	}
}

// TestOllamaCloudUsageSharedStateMatchesListDetailAndSpecialEndpointWithoutListNPlusOne
// lists two provider records sharing normalized Ollama identity with one stored
// session snapshot. A single group-resolution query enriches both rows,
// dedicated/detail views agree and none of their bodies contains the fixture
// secrets.
func TestOllamaCloudUsageSharedStateMatchesListDetailAndSpecialEndpointWithoutListNPlusOne(t *testing.T) {
	now := time.Now().UTC()
	source := &providercore.Record{
		ID: 7, Name: "source", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://ollama.com", "api_key": "shared-secret-key"},
		Extra: map[string]any{
			providercore.OllamaCloudUsageSessionExtraKey:     "ciphertext-secret",
			providercore.OllamaCloudUsageAutoRefreshExtraKey: true,
			providercore.OllamaCloudUsageSnapshotExtraKey: &providercore.OllamaCloudUsageSnapshot{
				Status: providercore.OllamaCloudUsageStatusOK, Data: &providercore.OllamaCloudUsageData{Plan: "pro"},
				LastAttemptAt: now, NextRefreshAt: now.Add(time.Hour),
			},
		},
		Status: providercore.StatusActive,
	}
	sibling := &providercore.Record{
		ID: 8, Name: "sibling", Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"base_url": "HTTPS://WWW.OLLAMA.COM:443/v1", "api_key": "shared-secret-key"},
		Extra:       map[string]any{}, Status: providercore.StatusActive,
	}
	repo := &ollamaCloudUsageHandlerTestRepo{providers: []*providercore.Record{source, sibling}}
	adminService := &ollamaManagementFixture{}
	adminService.providers = []providercore.Record{*source, *sibling}
	usageService := providercore.NewOllamaCloudUsageService(repo, nil, nil, providercore.OllamaUsageOptions{EncryptionKeyConfigured: true})
	// Join the shared fixture service after all list and detail assertions so
	// any admitted usage work finishes before these borrowed records leave scope.
	t.Cleanup(usageService.Stop)
	handler := newOllamaManagementHandler(adminService, usageService)
	router := gin.New()
	router.GET("/providers", handler.List)
	router.GET("/providers/:id", handler.GetByID)
	router.GET("/providers/:id/ollama-cloud-usage", NewOllamaUsageHandler(usageService).GetOllamaCloudUsage)

	listRecorder := httptest.NewRecorder()
	router.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/providers?page=1&page_size=20", nil))
	require.Equal(t, http.StatusOK, listRecorder.Code)
	require.Equal(t, 1, repo.groupResolveCalls, "the full list page must use one group-resolution batch")
	var listPayload struct {
		Data struct { // decoded HTTP success response
			Items []struct { // returned management page items
				// returned TokenRouter provider identifier
				ID int64 `json:"id"`
				// returned redacted Ollama usage state
				OllamaCloudUsage *providercore.OllamaCloudUsageState `json:"ollama_cloud_usage"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(listRecorder.Body.Bytes(), &listPayload))
	require.Len(t, listPayload.Data.Items, 2)
	for _, item := range listPayload.Data.Items {
		require.True(t, item.OllamaCloudUsage.Configured)
		require.Equal(t, "pro", item.OllamaCloudUsage.Snapshot.Data.Plan)
	}

	detailRecorder := httptest.NewRecorder()
	router.ServeHTTP(detailRecorder, httptest.NewRequest(http.MethodGet, "/providers/8", nil))
	require.Equal(t, http.StatusOK, detailRecorder.Code)
	var detailPayload struct {
		Data struct { // decoded HTTP success response
			// returned redacted Ollama usage state
			OllamaCloudUsage *providercore.OllamaCloudUsageState `json:"ollama_cloud_usage"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(detailRecorder.Body.Bytes(), &detailPayload))

	stateRecorder := httptest.NewRecorder()
	router.ServeHTTP(stateRecorder, httptest.NewRequest(http.MethodGet, "/providers/8/ollama-cloud-usage", nil))
	require.Equal(t, http.StatusOK, stateRecorder.Code)
	var statePayload struct {
		Data providercore.OllamaCloudUsageState `json:"data"` // decoded HTTP success response
	}
	require.NoError(t, json.Unmarshal(stateRecorder.Body.Bytes(), &statePayload))
	require.Equal(t, statePayload.Data.Configured, detailPayload.Data.OllamaCloudUsage.Configured)
	require.Equal(t, statePayload.Data.Snapshot, detailPayload.Data.OllamaCloudUsage.Snapshot)
	for _, body := range []string{listRecorder.Body.String(), detailRecorder.Body.String(), stateRecorder.Body.String()} {
		require.NotContains(t, body, "shared-secret-key")
		require.NotContains(t, body, "ciphertext-secret")
	}
}

// GetProvider returns a shallow matching configured provider or the
// provider-not-found error. Record maps remain borrowed by this serial fixture,
// whose handler resolves shared Ollama usage before mapping responses.
func (f *ollamaManagementFixture) GetProvider(_ context.Context, id int64) (*providercore.Record, error) {
	for _, value := range f.providers {
		if value.ID == id {
			return &value, nil
		}
	}
	return nil, providercore.ErrProviderNotFound
}

// ListProviders returns its entire borrowed fixture population and count in
// configured order. Paging, predicates and sorting are ignored here, allowing
// the scenario to inspect one whole-page group-resolution invocation.
func (f *ollamaManagementFixture) ListProviders(context.Context, int, int, string, string, string, string, int64, string, string, string) ([]providercore.Record, int64, error) {
	return f.providers, int64(len(f.providers)), nil
}

// newOllamaManagementHandler binds list and detail presentation to the same
// Ollama service instance. Runtime options are empty, while ManagementList and
// RuntimePresenter perform shared-state resolution and consistent
// encryption-key enrichment.
func newOllamaManagementHandler(source *ollamaManagementFixture, usage *providercore.OllamaCloudUsageService) *ManagementHandler {
	runtime := providercore.NewRuntimeStatusReader(providercore.RuntimeStatusOptions{})
	presenter := NewRuntimePresenter(runtime, source, usage)
	return NewManagementHandler(source, ManagementOptions{
		List:             providercore.NewManagementList(source, runtime, nil, usage),
		RuntimePresenter: presenter,
		Presenter:        presenter,
		Ollama:           usage,
	})
}

// GetByID returns the configured single-provider pointer first, then a matching
// population pointer. Missing IDs return provider-not-found; serial callers
// borrow these records while the usage service resolves identity groups.
func (r *ollamaCloudUsageHandlerTestRepo) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	if r.provider != nil && r.provider.ID == id {
		return r.provider, nil
	}
	for _, provider := range r.providers {
		if provider.ID == id {
			return provider, nil
		}
	}
	return nil, providercore.ErrProviderNotFound
}

// ListOllamaCloudUsageGroupProviders counts a serial resolution call and
// returns shallow configured record copies. It ignores the requested candidates
// and includes all fixture rows, allowing shared-identity grouping to use
// controlled data.
func (r *ollamaCloudUsageHandlerTestRepo) ListOllamaCloudUsageGroupProviders(_ context.Context, _ []*providercore.Record) ([]providercore.Record, error) {
	r.groupResolveCalls++
	result := make([]providercore.Record, 0, len(r.providers)+1)
	if r.provider != nil {
		result = append(result, *r.provider)
	}
	for _, provider := range r.providers {
		result = append(result, *provider)
	}
	return result, nil
}

// SaveOllamaCloudUsageSession accepts the unused fixture session-write path and
// records no state.
func (r *ollamaCloudUsageHandlerTestRepo) SaveOllamaCloudUsageSession(context.Context, *providercore.Record, string, bool) error {
	return nil
}

// DeleteOllamaCloudUsageSession accepts the unused fixture session-delete path
// and records no state.
func (r *ollamaCloudUsageHandlerTestRepo) DeleteOllamaCloudUsageSession(context.Context, *providercore.Record) error {
	return nil
}

// SetOllamaCloudUsageAutoRefresh accepts the unused fixture refresh-policy path
// and records no state.
func (r *ollamaCloudUsageHandlerTestRepo) SetOllamaCloudUsageAutoRefresh(context.Context, *providercore.Record, bool) error {
	return nil
}

// UpdateOllamaCloudUsageSnapshot accepts the unused fixture snapshot-write path
// and records no state.
func (r *ollamaCloudUsageHandlerTestRepo) UpdateOllamaCloudUsageSnapshot(context.Context, *providercore.Record, *providercore.OllamaCloudUsageSnapshot) error {
	return nil
}

// DisableOllamaCloudUsageAutoRefresh accepts the unused fixture policy-disable
// path and records no state.
func (r *ollamaCloudUsageHandlerTestRepo) DisableOllamaCloudUsageAutoRefresh(context.Context, *providercore.Record) error {
	return nil
}

// ListDueOllamaCloudUsageProviders returns no rows for the unused fixture
// maintenance-selection path.
func (r *ollamaCloudUsageHandlerTestRepo) ListDueOllamaCloudUsageProviders(context.Context, time.Time, time.Duration, time.Duration, int) ([]providercore.Record, error) {
	return nil, nil
}
