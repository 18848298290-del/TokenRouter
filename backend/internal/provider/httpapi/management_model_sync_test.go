package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Model-query HTTP errors distinguish invalid local credentials from upstream
// failures and return safe messages. management_model_sync.go is tested through
// the production model catalogue adapter with fixed consumable responses,
// including preview parsing and duplicate-ID normalization.

// syncUpstreamHTTPUpstream returns a controlled model-list response or injected
// transport error.
type syncUpstreamHTTPUpstream struct {
	// resp is returned by pointer when err is nil. Its body is consumed by the
	// production query, so callers supply a fresh configured response for each
	// request.
	resp *http.Response // borrowed consumable upstream response
	err  error          // injected upstream transport failure
}

// TestProviderHandlerSyncUpstreamModels_ConfigErrorReturnsBadRequest loads an
// API-key provider without its required key. The handler returns 400 with the
// configuration message before a usable upstream model query can complete.
func TestProviderHandlerSyncUpstreamModels_ConfigErrorReturnsBadRequest(t *testing.T) {
	svc := &availableModelsAdminService{
		managementMutationFixture: newManagementMutationFixture(),
		provider: providercore.Record{
			ID:       44,
			Name:     "openai-apikey-missing-key",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Status:   billing.StatusActive,
			Credentials: map[string]any{
				"base_url": "https://openai.example.com/v1",
			},
		},
	}
	router := setupSyncUpstreamModelsRouter(svc, &syncUpstreamHTTPUpstream{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/44/models/sync-upstream", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "No OpenAI API key is available")
}

// TestProviderHandlerSyncUpstreamModels_UpstreamErrorDoesNotExposeBody supplies
// an upstream 502 response containing secret-shaped body text. The
// administrator sees the safe status message and that body secret remains
// absent from the HTTP response.
func TestProviderHandlerSyncUpstreamModels_UpstreamErrorDoesNotExposeBody(t *testing.T) {
	svc := &availableModelsAdminService{
		managementMutationFixture: newManagementMutationFixture(),
		provider: providercore.Record{
			ID:       45,
			Name:     "openai-apikey-upstream-error",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Status:   billing.StatusActive,
			Credentials: map[string]any{
				"api_key":  "openai-key",
				"base_url": "https://openai.example.com/v1",
			},
		},
	}
	upstream := &syncUpstreamHTTPUpstream{resp: &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"SECRET_TOKEN should not be exposed"}`)),
	}}
	router := setupSyncUpstreamModelsRouter(svc, upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/45/models/sync-upstream", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "Upstream model list request failed with HTTP 502")
	require.NotContains(t, rec.Body.String(), "SECRET_TOKEN")
}

// TestProviderHandlerSyncUpstreamModelsPreview_UsesProvidedCredentials supplies
// temporary API-key configuration and a model list with duplicates. Preview
// succeeds and returns the de-duplicated model order produced by the production
// catalogue adapter.
func TestProviderHandlerSyncUpstreamModelsPreview_UsesProvidedCredentials(t *testing.T) {
	router := setupSyncUpstreamModelsRouter(newManagementMutationFixture(), &syncUpstreamHTTPUpstream{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-5.1"},{"id":"gpt-5.1"},{"id":"o3"}]}`)),
	}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/providers/models/sync-upstream-preview",
		strings.NewReader(`{"platform":"openai","type":"apikey","base_url":"https://openai.example.com/v1","api_key":"openai-key"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Data struct { // decoded HTTP success envelope
			Models []string `json:"models"` // returned normalized model identifiers
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, []string{"gpt-5.1", "o3"}, resp.Data.Models)
}

// TestProviderHandlerSyncUpstreamModelsPreview_ConfigErrorReturnsBadRequest
// omits the required temporary API key from preview JSON. Binding rejects the
// request with 400 and exposes the required-field message before querying
// models.
func TestProviderHandlerSyncUpstreamModelsPreview_ConfigErrorReturnsBadRequest(t *testing.T) {
	router := setupSyncUpstreamModelsRouter(newManagementMutationFixture(), &syncUpstreamHTTPUpstream{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/providers/models/sync-upstream-preview",
		strings.NewReader(`{"platform":"openai","type":"apikey","base_url":"https://openai.example.com/v1"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "required")
}

// Do returns the configured response or injected transport error for this
// model-query fixture. Response bodies are consumable and reused by pointer, so
// each test supplies a fresh response for its request.
func (u *syncUpstreamHTTPUpstream) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	if u.err != nil {
		return nil, u.err
	}
	return u.resp, nil
}

// DoWithTLS delegates to the fixed response path while ignoring profile
// selection. The returned response and body are shared with Do; callers supply
// a fresh consumable response for each request.
func (u *syncUpstreamHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, providerID, providerConcurrency)
}

// setupSyncUpstreamModelsRouter mounts stored and preview model queries using
// the production catalogue parser and URL policy. The supplied transport
// controls responses, and an eight-MiB body limit bounds parsed fixture
// results.
func setupSyncUpstreamModelsRouter(adminSvc ProviderManagement, upstream provideradapter.QoderTransport) *gin.Engine {
	router := gin.New()
	modelPolicy := egress.OperatorURLPolicy{}
	catalogue := &provideradapter.ModelCatalogue{Transport: upstream, Options: provideradapter.ModelCatalogueOptions{ValidateURL: modelPolicy.Validate, OperatorValidator: modelPolicy.Validate, BodyLimit: 8 * 1024 * 1024, CodexModelsURL: provideradapter.DefaultCodexModelsURL}}
	models := providercore.NewModelSyncService(catalogue.FetchUpstreamSupportedModels)
	handler := NewManagementHandler(adminSvc, ManagementOptions{Models: models})
	router.POST("/api/v1/admin/providers/:id/models/sync-upstream", handler.SyncUpstreamModels)
	router.POST("/api/v1/admin/providers/models/sync-upstream-preview", handler.SyncUpstreamModelsPreview)
	return router
}
