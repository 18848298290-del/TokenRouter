package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type bulkUpdateProbeProviderRepo struct {
	provideradapter.OpenAIProviderTestStore
	mu        sync.Mutex
	providers map[int64]*providercore.Record
	done      chan int64
}

type bulkUpdateProbeHTTPUpstream struct {
	mu   sync.Mutex
	urls []string
}

func TestProviderHandlerBulkUpdateOpenAIAPIKeyDoesNotProbe(t *testing.T) {
	provider := providercore.Record{
		ID:          11,
		Name:        "openai-apikey",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-new",
			"base_url": "http://upstream.example",
		},
	}
	adminSvc := newManagementMutationFixture()
	adminSvc.providers = []providercore.Record{provider}

	repo := &bulkUpdateProbeProviderRepo{
		providers: map[int64]*providercore.Record{provider.ID: &provider},
		done:      make(chan int64, 1),
	}
	upstream := &bulkUpdateProbeHTTPUpstream{}
	store := repo
	policy := &provideradapter.OpenAIProbePolicy{Available: true, Read: store.GetByID}
	executor := &provideradapter.OpenAIProviderTest{Store: store, Transport: upstream, ValidateURL: (egress.OperatorURLPolicy{AllowInsecureHTTP: true}).Validate, Prepare: policy.Prepare, ApplyRouting: policy.ApplyTestRouting, ResolveTLS: policy.ResolveTestTLS}
	tests := providercore.NewTestService(&provideradapter.TestTargets{Read: store.GetByID, OpenAI: executor}, providercore.TestOptions{Now: time.Now})

	router := gin.New()
	providerHandler := newMutationHandler(adminSvc, nil)
	// Bind the probe endpoint separately, as production routes do, to check that bulk updates do not invoke it.
	router.POST("/api/v1/admin/providers/:id/test", NewTestHandler(tests, nil).Test)
	router.POST("/api/v1/admin/providers/bulk-update", providerHandler.BulkUpdate)

	body, _ := json.Marshal(map[string]any{
		"provider_ids": []int64{provider.ID},
		"credentials": map[string]any{
			"api_key": "sk-new",
		},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/bulk-update", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	// Allow time for an unexpected asynchronous probe, then check upstream requests and capability writes.
	select {
	case <-repo.done:
		t.Fatal("OpenAI capability probe must not run")
	case <-time.After(50 * time.Millisecond):
	}
	upstream.mu.Lock()
	require.Empty(t, upstream.urls)
	upstream.mu.Unlock()

	repo.mu.Lock()
	require.NotContains(t, repo.providers[provider.ID].Extra, "openai_responses_probe_status")
	repo.mu.Unlock()
}

func TestBulkUpdateAcceptsFilterTargetRequest(t *testing.T) {
	adminSvc := newManagementMutationFixture()
	router := setupProviderMutationContractRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"filters": map[string]any{
			"platform":     "openai",
			"type":         "oauth",
			"status":       "active",
			"group":        "12",
			"privacy_mode": "blocked",
			"search":       "bulk-target",
		},
		"schedulable": true,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/bulk-update", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, float64(0), resp["code"])
}

func (r *bulkUpdateProbeProviderRepo) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := r.providers[id]
	if provider == nil {
		return nil, providercore.ErrProviderNotFound
	}
	copy := *provider
	return &copy, nil
}

func (r *bulkUpdateProbeProviderRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	provider := r.providers[id]
	if provider != nil {
		if provider.Extra == nil {
			provider.Extra = map[string]any{}
		}
		maps.Copy(provider.Extra, updates)
	}
	r.mu.Unlock()

	if _, ok := updates["openai_responses_probe_status"]; ok && r.done != nil {
		select {
		case r.done <- id:
		default:
		}
	}
	return nil
}

func (u *bulkUpdateProbeHTTPUpstream) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

func (u *bulkUpdateProbeHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.mu.Lock()
	u.urls = append(u.urls, req.URL.String())
	u.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
	}, nil
}
