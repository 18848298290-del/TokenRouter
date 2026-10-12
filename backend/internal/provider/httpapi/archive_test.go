package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	idempotencytest "github.com/TokenFlux/TokenRouter/internal/idempotency/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// archiveHTTPFixture records provider and proxy import inputs and list queries.
type archiveHTTPFixture struct {
	*managementMutationFixture
	egress.ProxyAdministrator
	proxies         []egress.Proxy
	createdProxies  []*egress.CreateProxyInput
	updatedProxies  []*egress.UpdateProxyInput
	updatedProxyIDs []int64
	list            managementListFixture
	lastListProxies struct {
		protocol, status, search, sortBy, sortOrder string
		calls                                       int
	}
}

type dataResponse struct {
	Code int         `json:"code"`
	Data dataPayload `json:"data"`
}

type dataPayload struct {
	Type           string         `json:"type"`
	Version        int            `json:"version"`
	Proxies        []dataProxy    `json:"proxies"`
	Providers      []dataProvider `json:"providers"`
	SkippedShadows int            `json:"skipped_shadows"`
}

type dataProxy struct {
	ProxyKey string `json:"proxy_key"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Status   string `json:"status"`
}

type dataProvider struct {
	Name        string         `json:"name"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Credentials map[string]any `json:"credentials"`
	Extra       map[string]any `json:"extra"`
	ProxyKey    *string        `json:"proxy_key"`
	Concurrency int            `json:"concurrency"`
	Priority    int            `json:"priority"`
}

func TestExportDataIncludesSecrets(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	proxyID := int64(11)
	adminSvc.proxies = []egress.Proxy{
		{
			ID:       proxyID,
			Name:     "proxy",
			Protocol: "http",
			Host:     "127.0.0.1",
			Port:     8080,
			Username: "user",
			Password: "pass",
			Status:   billing.StatusActive,
		},
		{
			ID:       12,
			Name:     "orphan",
			Protocol: "https",
			Host:     "10.0.0.1",
			Port:     443,
			Username: "o",
			Password: "p",
			Status:   billing.StatusActive,
		},
	}
	adminSvc.providers = []providercore.Record{
		{
			ID:          21,
			Name:        "provider",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Credentials: map[string]any{"token": "secret"},
			Extra:       map[string]any{"note": "x"},
			ProxyID:     &proxyID,
			Concurrency: 3,
			Priority:    50,
			Status:      billing.StatusDisabled,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers/data", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp dataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Equal(t, transfer.DataType, resp.Data.Type)
	require.Equal(t, transfer.DataVersion, resp.Data.Version)
	require.Len(t, resp.Data.Proxies, 1)
	require.Equal(t, "pass", resp.Data.Proxies[0].Password)
	require.Len(t, resp.Data.Providers, 1)
	require.Equal(t, "secret", resp.Data.Providers[0].Credentials["token"])
}

func TestExportDataWithoutProxies(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	proxyID := int64(11)
	adminSvc.proxies = []egress.Proxy{
		{
			ID:       proxyID,
			Name:     "proxy",
			Protocol: "http",
			Host:     "127.0.0.1",
			Port:     8080,
			Username: "user",
			Password: "pass",
			Status:   billing.StatusActive,
		},
	}
	adminSvc.providers = []providercore.Record{
		{
			ID:          21,
			Name:        "provider",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Credentials: map[string]any{"token": "secret"},
			ProxyID:     &proxyID,
			Concurrency: 3,
			Priority:    50,
			Status:      billing.StatusDisabled,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers/data?include_proxies=false", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp dataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Proxies, 0)
	require.Len(t, resp.Data.Providers, 1)
	require.Nil(t, resp.Data.Providers[0].ProxyKey)
}

// TestExportDataExcludesSparkShadow checks that exports skip Spark shadows and report the skipped count.
// Shadows have no credentials, so including them would produce backups rejected by import validation.
func TestExportDataExcludesSparkShadow(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	parentID := int64(21)
	adminSvc.providers = []providercore.Record{
		{
			ID:          parentID,
			Name:        "mother",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Credentials: map[string]any{"token": "secret"},
			Status:      billing.StatusActive,
		},
		{
			ID:               22,
			Name:             "mother (Spark)",
			Platform:         capability.PlatformOpenAI,
			Type:             capability.ProviderTypeOAuth,
			Credentials:      map[string]any{}, // Shadows have empty credentials.
			ParentProviderID: &parentID,        // Identifies the shadow's parent.
			QuotaDimension:   providercore.QuotaDimensionSpark,
			Status:           billing.StatusActive,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/providers/data?include_proxies=false", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp dataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Providers, 1, "影子应被排除,仅导出母提供商")
	require.Equal(t, "mother", resp.Data.Providers[0].Name)
	require.Equal(t, 1, resp.Data.SkippedShadows, "跳过的影子数量应透出")
}

func TestExportDataPassesProviderFiltersAndSort(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()
	adminSvc.providers = []providercore.Record{
		{ID: 1, Name: "acc-1", Status: billing.StatusActive},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/providers/data?platform=openai&type=oauth&status=active&group=12&privacy_mode=blocked&search=keyword&sort_by=priority&sort_order=desc",
		nil,
	)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, 1, adminSvc.list.lastListProviders.calls)
	require.Equal(t, "openai", adminSvc.list.lastListProviders.platform)
	require.Equal(t, "oauth", adminSvc.list.lastListProviders.providerType)
	require.Equal(t, "active", adminSvc.list.lastListProviders.status)
	require.Equal(t, int64(12), adminSvc.list.lastListProviders.groupID)
	require.Equal(t, "blocked", adminSvc.list.lastListProviders.privacyMode)
	require.Equal(t, "keyword", adminSvc.list.lastListProviders.search)
	require.Equal(t, "priority", adminSvc.list.lastListProviders.sortBy)
	require.Equal(t, "desc", adminSvc.list.lastListProviders.sortOrder)
}

func TestExportDataSelectedIDsOverrideFilters(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/providers/data?ids=1,2&platform=openai&search=keyword&sort_by=priority&sort_order=desc",
		nil,
	)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp dataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Providers, 2)
	require.Equal(t, 0, adminSvc.list.lastListProviders.calls)
}

func TestImportDataReusesProxyAndSkipsDefaultGroup(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	adminSvc.proxies = []egress.Proxy{
		{
			ID:       1,
			Name:     "proxy",
			Protocol: "socks5",
			Host:     "1.2.3.4",
			Port:     1080,
			Username: "u",
			Password: "p",
			Status:   billing.StatusActive,
		},
	}

	dataPayload := map[string]any{
		"data": map[string]any{
			"type":    transfer.DataType,
			"version": transfer.DataVersion,
			"proxies": []map[string]any{
				{
					"proxy_key": "socks5|1.2.3.4|1080|u|p",
					"name":      "proxy",
					"protocol":  "socks5",
					"host":      "1.2.3.4",
					"port":      1080,
					"username":  "u",
					"password":  "p",
					"status":    "active",
				},
			},
			"providers": []map[string]any{
				{
					"name":        "acc",
					"platform":    capability.PlatformOpenAI,
					"type":        capability.ProviderTypeOAuth,
					"credentials": map[string]any{"token": "x"},
					"proxy_key":   "socks5|1.2.3.4|1080|u|p",
					"concurrency": 3,
					"priority":    50,
				},
			},
		},
	}

	body, _ := json.Marshal(dataPayload)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, adminSvc.createdProxies, 0)
	require.Len(t, adminSvc.createdProviders, 1)
}

func TestImportDataIdempotencyIgnoresDeprecatedLongContextBillingExtra(t *testing.T) {
	const deprecatedKey = "openai_long_context_billing_enabled"
	coordinator := idempotency.NewIdempotencyCoordinator(
		idempotencytest.NewMemoryStore(),
		idempotency.DefaultIdempotencyConfig(),
	)

	router, adminSvc := setupProviderDataRouter(coordinator)
	call := func(extra map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		provider := map[string]any{
			"name":        "openai-oauth",
			"platform":    capability.PlatformOpenAI,
			"type":        capability.ProviderTypeOAuth,
			"credentials": map[string]any{"access_token": "token"},
			"extra":       extra,
		}
		payload := map[string]any{
			"data": map[string]any{
				"type":      transfer.DataType,
				"version":   transfer.DataVersion,
				"proxies":   []map[string]any{},
				"providers": []map[string]any{provider},
			},
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/data", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "import-deprecated-long-context")
		router.ServeHTTP(recorder, request)
		return recorder
	}

	first := call(map[string]any{
		deprecatedKey: false,
		"preserved":   "value",
	})
	second := call(map[string]any{"preserved": "value"})
	third := call(map[string]any{
		deprecatedKey: map[string]any{"malformed": true},
		"preserved":   "value",
	})

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, http.StatusOK, third.Code, third.Body.String())
	require.Empty(t, first.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, "true", third.Header().Get("X-Idempotency-Replayed"))
	require.Len(t, adminSvc.createdProviders, 1)
	require.NotContains(t, adminSvc.createdProviders[0].Extra, deprecatedKey)
	require.Equal(t, "value", adminSvc.createdProviders[0].Extra["preserved"])
}

func TestImportDataLeavesOpenAIOAuthModelWhitelistUnset(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	postImportProvider(t, router, map[string]any{
		"name":        "openai-oauth",
		"platform":    capability.PlatformOpenAI,
		"type":        capability.ProviderTypeOAuth,
		"credentials": map[string]any{"access_token": "token"},
	})

	require.Len(t, adminSvc.createdProviders, 1)
	require.NotContains(t, adminSvc.createdProviders[0].Credentials, "model_whitelist")
}

func TestImportDataKeepsExistingOpenAIOAuthModelWhitelist(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	postImportProvider(t, router, map[string]any{
		"name":     "openai-oauth",
		"platform": capability.PlatformOpenAI,
		"type":     capability.ProviderTypeOAuth,
		"credentials": map[string]any{
			"access_token":    "token",
			"model_whitelist": []any{},
		},
	})

	require.Len(t, adminSvc.createdProviders, 1)
	require.Equal(t, []any{}, adminSvc.createdProviders[0].Credentials["model_whitelist"])
}

func TestImportDataTreatsOpenAIOAuthNullProviderFieldAsPresent(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	postImportProvider(t, router, map[string]any{
		"name":        "openai-oauth",
		"platform":    capability.PlatformOpenAI,
		"type":        capability.ProviderTypeOAuth,
		"credentials": map[string]any{"access_token": "token"},
		"concurrency": nil,
	})

	require.Len(t, adminSvc.createdProviders, 1)
	require.Equal(t, 0, adminSvc.createdProviders[0].Concurrency)
}

func TestImportDataLeavesAnthropicModelWhitelistUnset(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	postImportProvider(t, router, map[string]any{
		"name":        "anthropic-oauth",
		"platform":    capability.PlatformAnthropic,
		"type":        capability.ProviderTypeOAuth,
		"credentials": map[string]any{"access_token": "token"},
	})

	require.Len(t, adminSvc.createdProviders, 1)
	_, exists := adminSvc.createdProviders[0].Credentials["model_whitelist"]
	require.False(t, exists)
}

func TestImportDataAcceptsQoderCosyProvider(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	postImportProvider(t, router, map[string]any{
		"name":     "qoder-cosy",
		"platform": capability.PlatformQoder,
		"type":     capability.ProviderTypeCosy,
		"credentials": map[string]any{
			"pat": "pat-123",
		},
	})

	require.Len(t, adminSvc.createdProviders, 1)
	require.Equal(t, capability.PlatformQoder, adminSvc.createdProviders[0].Platform)
	require.Equal(t, capability.ProviderTypeCosy, adminSvc.createdProviders[0].Type)
	require.Equal(t, "pat-123", adminSvc.createdProviders[0].Credentials["pat"])
}

func TestImportDataRejectsQoderNonCosyProvider(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	rec := postImportProviderRaw(t, router, map[string]any{
		"name":        "qoder-apikey",
		"platform":    capability.PlatformQoder,
		"type":        capability.ProviderTypeAPIKey,
		"credentials": map[string]any{"api_key": "key"},
	})

	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, adminSvc.createdProviders)
	result := decodeImportResult(t, rec)
	require.Equal(t, 1, result.Data.ProviderFailed)
	require.Contains(t, rec.Body.String(), "qoder providers require cosy")
}

func TestImportDataRejectsCosyNonQoderProvider(t *testing.T) {
	router, adminSvc := setupProviderDataRouter()

	rec := postImportProviderRaw(t, router, map[string]any{
		"name":        "anthropic-cosy",
		"platform":    capability.PlatformAnthropic,
		"type":        capability.ProviderTypeCosy,
		"credentials": map[string]any{"pat": "pat-123"},
	})

	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, adminSvc.createdProviders)
	result := decodeImportResult(t, rec)
	require.Equal(t, 1, result.Data.ProviderFailed)
	require.Contains(t, rec.Body.String(), "cosy provider type requires qoder platform")
}

func (s *archiveHTTPFixture) ListProxies(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]egress.Proxy, int64, error) {
	s.lastListProxies.protocol = protocol
	s.lastListProxies.status = status
	s.lastListProxies.search = search
	s.lastListProxies.sortBy = sortBy
	s.lastListProxies.sortOrder = sortOrder
	s.lastListProxies.calls++
	search = strings.TrimSpace(strings.ToLower(search))
	filtered := make([]egress.Proxy, 0, len(s.proxies))
	for _, proxy := range s.proxies {
		if protocol != "" && proxy.Protocol != protocol {
			continue
		}
		if status != "" && proxy.Status != status {
			continue
		}
		if search != "" {
			name := strings.ToLower(proxy.Name)
			host := strings.ToLower(proxy.Host)
			if !strings.Contains(name, search) && !strings.Contains(host, search) {
				continue
			}
		}
		filtered = append(filtered, proxy)
	}
	return filtered, int64(len(filtered)), nil
}

func (s *archiveHTTPFixture) GetProxy(ctx context.Context, id int64) (*egress.Proxy, error) {
	for i := range s.proxies {
		proxy := s.proxies[i]
		if proxy.ID == id {
			return &proxy, nil
		}
	}
	proxy := egress.Proxy{ID: id, Name: "proxy", Status: billing.StatusActive}
	return &proxy, nil
}

func (s *archiveHTTPFixture) GetProxiesByIDs(ctx context.Context, ids []int64) ([]egress.Proxy, error) {
	if len(ids) == 0 {
		return []egress.Proxy{}, nil
	}
	out := make([]egress.Proxy, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	for i := range s.proxies {
		proxy := s.proxies[i]
		if _, ok := seen[proxy.ID]; ok {
			out = append(out, proxy)
		}
	}
	return out, nil
}

func (s *archiveHTTPFixture) CreateProxy(ctx context.Context, input *egress.CreateProxyInput) (*egress.Proxy, error) {
	s.mu.Lock()
	s.createdProxies = append(s.createdProxies, input)
	s.mu.Unlock()
	proxy := egress.Proxy{ID: 400, Name: input.Name, Status: billing.StatusActive}
	return &proxy, nil
}

func (s *archiveHTTPFixture) UpdateProxy(ctx context.Context, id int64, input *egress.UpdateProxyInput) (*egress.Proxy, error) {
	s.mu.Lock()
	s.updatedProxyIDs = append(s.updatedProxyIDs, id)
	s.updatedProxies = append(s.updatedProxies, input)
	s.mu.Unlock()
	proxy := egress.Proxy{ID: id, Name: input.Name, Status: billing.StatusActive}
	return &proxy, nil
}

func newArchiveHTTPFixture() *archiveHTTPFixture {
	source := &archiveHTTPFixture{managementMutationFixture: newManagementMutationFixture()}
	source.providers = newManagementListFixture().providers
	now := time.Now().UTC()
	source.proxies = []egress.Proxy{{ID: 4, Name: "proxy", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: egress.StatusActive, CreatedAt: now, UpdatedAt: now}}
	return source
}

func (s *archiveHTTPFixture) ListProviders(ctx context.Context, page, size int, platform, kind, status, search string, gid int64, privacy, sortBy, order string) ([]providercore.Record, int64, error) {
	s.list.providers = s.providers
	return s.list.ListProviders(ctx, page, size, platform, kind, status, search, gid, privacy, sortBy, order)
}

func setupProviderDataRouter(coordinators ...*idempotency.IdempotencyCoordinator) (*gin.Engine, *archiveHTTPFixture) {
	router := gin.New()
	adminSvc := newArchiveHTTPFixture()

	options := providercore.ArchiveOptions{Now: time.Now, DecodeIDToken: provideradapter.DecodeArchiveIDToken}
	core := providercore.NewArchive(adminSvc, egress.NewProxyTransfer(adminSvc, nil, time.Now), options)
	h := NewArchiveHandler(core)
	if len(coordinators) > 0 {
		h.BindIdempotency(coordinators[0])
	}

	router.GET("/api/v1/admin/providers/data", h.ExportData)
	router.POST("/api/v1/admin/providers/data", h.ImportData)
	return router, adminSvc
}

func decodeImportResult(t *testing.T, rec *httptest.ResponseRecorder) struct {
	Code int                       `json:"code"`
	Data transfer.DataImportResult `json:"data"`
} {
	t.Helper()
	var result struct {
		Code int                       `json:"code"`
		Data transfer.DataImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	return result
}

func postImportProvider(t *testing.T, router *gin.Engine, provider map[string]any) {
	t.Helper()

	rec := postImportProviderRaw(t, router, provider)
	require.Equal(t, http.StatusOK, rec.Code)
}

func postImportProviderRaw(t *testing.T, router *gin.Engine, provider map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	dataPayload := map[string]any{
		"data": map[string]any{
			"type":      transfer.DataType,
			"version":   transfer.DataVersion,
			"proxies":   []map[string]any{},
			"providers": []map[string]any{provider},
		},
	}

	body, _ := json.Marshal(dataPayload)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}
