package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
)

// proxyTaskFixture tracks asynchronously accepted import callbacks so test
// cleanup waits for their completion.
type proxyTaskFixture struct {
	// wg tracks each accepted callback through its return. setupProxyDataRouter
	// registers cleanup waiting after construction so test completion joins the
	// background work admitted while handlers process imports.
	wg sync.WaitGroup // accepted callback completion tracking
}

// proxyDataResponse decodes the common response code and exported proxy backup
// envelope for assertions.
type proxyDataResponse struct {
	Code int                  `json:"code"` // common response outcome code
	Data transfer.DataPayload `json:"data"` // exported proxy backup envelope
}

// proxyImportResponse decodes response status and partial proxy import outcomes
// for fixture assertions.
type proxyImportResponse struct {
	Code int                       `json:"code"` // common response outcome code
	Data transfer.DataImportResult `json:"data"` // partial proxy import outcomes
}

// TestProxyExportDataRespectsFilters configures HTTP and HTTPS records before
// exporting with a protocol filter. It checks envelope identity/version,
// selected protocol, empty provider collection, and forwarded default ordering
// values.
func TestProxyExportDataRespectsFilters(t *testing.T) {
	router, adminSvc := setupProxyDataRouter(t)

	adminSvc.proxies = []egress.Proxy{
		{
			ID:       1,
			Name:     "proxy-a",
			Protocol: "http",
			Host:     "127.0.0.1",
			Port:     8080,
			Username: "user",
			Password: "pass",
			Status:   billing.StatusActive,
		},
		{
			ID:       2,
			Name:     "proxy-b",
			Protocol: "https",
			Host:     "10.0.0.2",
			Port:     443,
			Username: "u",
			Password: "p",
			Status:   billing.StatusDisabled,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxies/data?protocol=https", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp proxyDataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Equal(t, transfer.DataType, resp.Data.Type)
	require.Equal(t, transfer.DataVersion, resp.Data.Version)
	require.Len(t, resp.Data.Proxies, 1)
	require.Len(t, resp.Data.Providers, 0)
	require.Equal(t, "https", resp.Data.Proxies[0].Protocol)
	require.Equal(t, 1, adminSvc.lastListProxies.calls)
	require.Equal(t, "https", adminSvc.lastListProxies.protocol)
	require.Equal(t, "id", adminSvc.lastListProxies.sortBy)
	require.Equal(t, "desc", adminSvc.lastListProxies.sortOrder)
}

// TestProxyExportDataWithSelectedIDs exports one selected fixture ID from two
// different connection records. It checks the returned record's protocol and
// host and verifies the ordinary filtered listing path was not invoked.
func TestProxyExportDataWithSelectedIDs(t *testing.T) {
	router, adminSvc := setupProxyDataRouter(t)

	adminSvc.proxies = []egress.Proxy{
		{
			ID:       1,
			Name:     "proxy-a",
			Protocol: "http",
			Host:     "127.0.0.1",
			Port:     8080,
			Username: "user",
			Password: "pass",
			Status:   billing.StatusActive,
		},
		{
			ID:       2,
			Name:     "proxy-b",
			Protocol: "https",
			Host:     "10.0.0.2",
			Port:     443,
			Username: "u",
			Password: "p",
			Status:   billing.StatusDisabled,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxies/data?ids=2", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp proxyDataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Proxies, 1)
	require.Equal(t, "https", resp.Data.Proxies[0].Protocol)
	require.Equal(t, "10.0.0.2", resp.Data.Proxies[0].Host)
	require.Equal(t, 0, adminSvc.lastListProxies.calls)
}

// TestProxyExportDataPassesSortParams requests protocol/status/search filters
// and name ordering through export. It checks the successful response and every
// recorded ordinary-list filter/order value passed to the fixture service.
func TestProxyExportDataPassesSortParams(t *testing.T) {
	router, adminSvc := setupProxyDataRouter(t)

	adminSvc.proxies = []egress.Proxy{
		{
			ID:       1,
			Name:     "proxy-a",
			Protocol: "http",
			Host:     "127.0.0.1",
			Port:     8080,
			Username: "user",
			Password: "pass",
			Status:   billing.StatusActive,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxies/data?protocol=http&status=active&search=proxy&sort_by=name&sort_order=asc", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, 1, adminSvc.lastListProxies.calls)
	require.Equal(t, "http", adminSvc.lastListProxies.protocol)
	require.Equal(t, "active", adminSvc.lastListProxies.status)
	require.Equal(t, "proxy", adminSvc.lastListProxies.search)
	require.Equal(t, "name", adminSvc.lastListProxies.sortBy)
	require.Equal(t, "asc", adminSvc.lastListProxies.sortOrder)
}

// TestProxyExportDataSortByProviderCountUsesProviderCountListing supplies
// distinct ordinary and augmented fixtures with provider-count ordering. It
// checks exported names follow the configured augmented order and ordinary
// listing was not called.
func TestProxyExportDataSortByProviderCountUsesProviderCountListing(t *testing.T) {
	router, adminSvc := setupProxyDataRouter(t)

	adminSvc.proxies = []egress.Proxy{
		{
			ID:       1,
			Name:     "proxy-id-1",
			Protocol: "http",
			Host:     "127.0.0.1",
			Port:     8080,
			Status:   billing.StatusActive,
		},
		{
			ID:       2,
			Name:     "proxy-id-2",
			Protocol: "http",
			Host:     "127.0.0.2",
			Port:     8081,
			Status:   billing.StatusActive,
		},
	}
	adminSvc.proxyCounts = []egress.ProxyWithProviderCount{
		{
			Proxy: egress.Proxy{
				ID:       2,
				Name:     "proxy-count-high",
				Protocol: "http",
				Host:     "127.0.0.2",
				Port:     8081,
				Status:   billing.StatusActive,
			},
			ProviderCount: 9,
		},
		{
			Proxy: egress.Proxy{
				ID:       1,
				Name:     "proxy-count-low",
				Protocol: "http",
				Host:     "127.0.0.1",
				Port:     8080,
				Status:   billing.StatusActive,
			},
			ProviderCount: 1,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxies/data?sort_by=provider_count&sort_order=desc", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp proxyDataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Proxies, 2)
	require.Equal(t, "proxy-count-high", resp.Data.Proxies[0].Name)
	require.Equal(t, "proxy-count-low", resp.Data.Proxies[1].Name)
	require.Equal(t, 0, adminSvc.lastListProxies.calls)
}

// TestProxyImportDataReusesAndTriggersLatencyProbe imports one existing
// connection with changed status and one new connection. It checks partial
// counters, recorded reused-ID update, and eventual scheduling of one fixture
// latency probe.
func TestProxyImportDataReusesAndTriggersLatencyProbe(t *testing.T) {
	router, adminSvc := setupProxyDataRouter(t)

	adminSvc.proxies = []egress.Proxy{
		{
			ID:       1,
			Name:     "proxy-a",
			Protocol: "http",
			Host:     "127.0.0.1",
			Port:     8080,
			Username: "user",
			Password: "pass",
			Status:   billing.StatusActive,
		},
	}

	payload := map[string]any{
		"data": map[string]any{
			"type":    transfer.DataType,
			"version": transfer.DataVersion,
			"proxies": []map[string]any{
				{
					"proxy_key": "http|127.0.0.1|8080|user|pass",
					"name":      "proxy-a",
					"protocol":  "http",
					"host":      "127.0.0.1",
					"port":      8080,
					"username":  "user",
					"password":  "pass",
					"status":    "inactive",
				},
				{
					"proxy_key": "https|10.0.0.2|443|u|p",
					"name":      "proxy-b",
					"protocol":  "https",
					"host":      "10.0.0.2",
					"port":      443,
					"username":  "u",
					"password":  "p",
					"status":    "active",
				},
			},
			"providers": []map[string]any{},
		},
	}

	body, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/proxies/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp proxyImportResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Equal(t, 1, resp.Data.ProxyCreated)
	require.Equal(t, 1, resp.Data.ProxyReused)
	require.Equal(t, 0, resp.Data.ProxyFailed)

	adminSvc.mu.Lock()
	updatedIDs := append([]int64(nil), adminSvc.updatedProxyIDs...)
	adminSvc.mu.Unlock()
	require.Contains(t, updatedIDs, int64(1))

	// Import returns before accepted probe work completes. Observe the recorded
	// callback under its fixture lock before cleanup joins the tracked task lifetime.
	require.Eventually(t, func() bool {
		adminSvc.mu.Lock()
		defer adminSvc.mu.Unlock()
		return len(adminSvc.testedProxyIDs) == 1
	}, time.Second, 10*time.Millisecond)
}

// Go admits every supplied callback and tracks its asynchronous execution with
// the fixture wait group. Cleanup waits for registered callbacks, and the
// fixture provides no cancellation, admission rejection, or panic recovery
// behavior.
func (f *proxyTaskFixture) Go(_ string, run func()) bool {
	// Register callback completion with the wait group before returning admission,
	// so test cleanup includes work whose goroutine starts after the HTTP response.
	f.wg.Go(func() { ; run() })
	return true
}

// setupProxyDataRouter constructs direct export/import routes using a serial
// admin fixture and tracked background tasks. Cleanup waits for those
// callbacks; authentication is outside this router, so tests inspect binding
// and transfer behavior only.
func setupProxyDataRouter(t *testing.T) (*gin.Engine, *proxyAdminFixture) {
	router := gin.New()
	adminSvc := newProxyAdminFixture()

	tasks := &proxyTaskFixture{}
	t.Cleanup(tasks.wg.Wait)
	h := NewProxyHandler(adminSvc, egress.NewProxyTransfer(adminSvc, tasks, time.Now))
	router.GET("/api/v1/admin/proxies/data", h.ExportData)
	router.POST("/api/v1/admin/proxies/data", h.ImportData)

	return router, adminSvc
}
