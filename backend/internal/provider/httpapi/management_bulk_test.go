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

// OpenAI API-key bulk edits should use configured protocol choices without
// triggering a separate capability probe. management_bulk.go is exercised with
// a mounted test endpoint and synchronized request/write observations; the
// timed absence assertion covers this controlled fixture window.

// bulkUpdateProbeProviderRepo exposes shallow record reads and synchronized
// capability-write observation for bulk-update tests.
type bulkUpdateProbeProviderRepo struct {
	provideradapter.OpenAIProviderTestStore            // unused embedded test-store operations
	mu                                      sync.Mutex // captured state synchronization mutex
	// providers is read and edited under mu during fixture calls. Record copies
	// share nested maps, so the test reads captured Extra only while holding that
	// mutex.
	providers map[int64]*providercore.Record // configured fixture record pointers
	// done receives a best-effort provider ID after a Responses status write. An
	// unavailable channel receiver drops the signal after the fixture has
	// completed its map update.
	done chan int64 // optional capability-write observation channel
}

// bulkUpdateProbeHTTPUpstream counts captured request URLs while supplying a
// fixed missing-route response.
type bulkUpdateProbeHTTPUpstream struct {
	mu sync.Mutex // captured state synchronization mutex
	// urls is appended under mu while an unexpected probe can run. The test holds
	// that same mutex when asserting the captured request collection is empty.
	urls []string // captured upstream request URLs
}

// TestProviderHandlerBulkUpdateOpenAIAPIKeyDoesNotProbe updates OpenAI API-key
// credentials with a separately mounted probe endpoint. During the observation
// window, the update produces no upstream requests or recorded
// Responses-capability Extra write.
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

	// These bulk request values use JSON-compatible scalars and maps. An encoding
	// failure leaves an empty body and is detected by the later HTTP-success assertion.
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
	// Give an unexpected background probe a fixture observation window before
	// reading synchronized request and Extra captures; the timeout bounds this absence assertion.
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

// TestBulkUpdateAcceptsFilterTargetRequest supplies bulk list filters and a
// scheduling-switch change instead of explicit provider IDs. The handler admits
// that filter selection and returns the common successful response envelope.
func TestBulkUpdateAcceptsFilterTargetRequest(t *testing.T) {
	adminSvc := newManagementMutationFixture()
	router := setupProviderMutationContractRouter(adminSvc)

	// These bulk request values use JSON-compatible scalars and maps. An encoding
	// failure leaves an empty body and is detected by the later HTTP-success assertion.
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

// GetByID copies the selected record struct while holding the fixture mutex.
// Missing IDs return provider-not-found, while copied record maps and pointers
// remain borrowed from fixture storage.
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

// UpdateExtra merges supplied values into the configured record under the
// mutex. A Responses-probe status key sends a best-effort completion signal
// after unlocking, allowing the test to detect unexpected capability
// persistence.
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
			// Capability observation is best effort: a full or absent fixture channel
			// cannot block the already completed Extra write or the surrounding HTTP request.
		}
	}
	return nil
}

// Do records a fixture request through the TLS-capable method with no profile.
// It returns a fresh 404 response body for the probe caller to consume and
// close, using the same synchronized URL capture as DoWithTLS.
func (u *bulkUpdateProbeHTTPUpstream) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

// DoWithTLS records the request URL under the fixture mutex and returns a new
// 404 body. Tests read the captured collection under that lock; proxy and
// profile input do not change the fixed response.
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
