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

// Provider backups contain raw credentials while ordinary management DTOs
// redact them, and credential shadows are omitted from archive entries.
// archive.go is tested with controlled provider and proxy rows, preserving
// selected-ID precedence, explicit field presence, proxy-key reuse and per-item
// validation outcomes.
//
// Import response idempotency normalizes the retired Extra field before
// fingerprinting, so changes to that ignored input replay one completed import.
// Captured provider and proxy creation inputs remain borrowed until synchronous
// requests finish; their append operations use the shared creation mutex. Test
// payload decoders keep the archive format distinct from management display
// objects and do not validate production export authentication middleware.

// archiveHTTPFixture captures provider and proxy transfer inputs while
// supplying serial configured archive rows. Provider and proxy creation
// captures use the inherited mutex; configured rows and query captures remain
// serial fixture state.
type archiveHTTPFixture struct {
	*managementMutationFixture                            // borrowed provider mutation capture fixture
	egress.ProxyAdministrator                             // unused embedded proxy administrator operations
	proxies                    []egress.Proxy             // configured serial proxy rows
	createdProxies             []*egress.CreateProxyInput // borrowed captured proxy creation inputs
	updatedProxies             []*egress.UpdateProxyInput // borrowed captured proxy update inputs
	updatedProxyIDs            []int64                    // captured proxy update identifiers
	list                       managementListFixture      // shared serial provider list fixture
	lastListProxies            struct {                   // captured serial proxy query fields
		// protocol: requested proxy protocol filter; status: requested proxy status
		// filter; search: requested proxy search text; sortBy: requested proxy
		// sorting column; sortOrder: requested proxy sorting direction
		protocol, status, search, sortBy, sortOrder string
		calls                                       int // serial proxy-list invocation count
	}
}

// dataResponse decodes the success envelope around a raw provider backup
// payload.
type dataResponse struct {
	Code int         `json:"code"` // decoded HTTP envelope code
	Data dataPayload `json:"data"` // decoded raw backup payload
}

// dataPayload decodes archive header, transferred entries and the
// skipped-shadow count.
type dataPayload struct {
	Type           string         `json:"type"`            // serialized archive format identifier
	Version        int            `json:"version"`         // serialized archive format version
	Proxies        []dataProxy    `json:"proxies"`         // transferred raw proxy entries
	Providers      []dataProvider `json:"providers"`       // transferred raw provider entries
	SkippedShadows int            `json:"skipped_shadows"` // omitted credential shadow count
}

// dataProxy decodes a serialized proxy identity with raw backup authentication
// values.
type dataProxy struct {
	ProxyKey string `json:"proxy_key"` // serialized proxy identity key
	Name     string `json:"name"`      // transferred entry display name
	Protocol string `json:"protocol"`  // transferred proxy protocol label
	Host     string `json:"host"`      // transferred proxy hostname text
	Port     int    `json:"port"`      // transferred proxy port number
	Username string `json:"username"`  // transferred proxy authentication username
	Password string `json:"password"`  // transferred proxy authentication password
	Status   string `json:"status"`    // transferred proxy state label
}

// dataProvider decodes transferred provider configuration and its optional
// serialized proxy association.
type dataProvider struct {
	Name        string         `json:"name"`        // transferred entry display name
	Platform    string         `json:"platform"`    // transferred upstream platform identifier
	Type        string         `json:"type"`        // provider authentication kind
	Credentials map[string]any `json:"credentials"` // transferred raw provider credentials
	Extra       map[string]any `json:"extra"`       // transferred provider configuration values
	// ProxyKey links a provider entry to its serialized proxy when proxy inclusion
	// is enabled. Disabled inclusion leaves this optional relationship nil in
	// backup output.
	ProxyKey    *string `json:"proxy_key"`   // optional serialized proxy identity
	Concurrency int     `json:"concurrency"` // transferred provider request capacity
	Priority    int     `json:"priority"`    // transferred scheduler priority value
}

// TestExportDataIncludesSecrets exports one provider and its selected proxy
// while another proxy remains unreferenced. The raw backup includes upstream
// credential and proxy password values with its required type and version
// header.
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

// TestExportDataWithoutProxies exports a provider with proxy inclusion
// explicitly disabled. The backup retains its provider entry but has no proxy
// entries and no proxy-key association for that provider.
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

// TestExportDataExcludesSparkShadow exports a parent provider and a credential
// shadow together. The archive omits the shadow, retains the parent and reports
// the one skipped shadow in its payload.
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

// TestExportDataPassesProviderFiltersAndSort requests a backup through
// platform, type, status, group, privacy and search filters. The fixture
// captures one list query with those predicates and the selected priority sort
// direction.
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

// TestExportDataSelectedIDsOverrideFilters supplies explicit provider IDs
// alongside list predicates and sorting input. The backup contains two selected
// fixture providers and the list query counter remains zero.
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

// TestImportDataReusesProxyAndSkipsDefaultGroup imports a provider whose
// serialized proxy key matches an existing proxy. The fixture records no new
// proxy creation and one provider creation; it does not inspect group binding.
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

	// Archive request fixtures use JSON-compatible values. An encoding failure
	// leaves the body empty, and the following HTTP-success assertion exposes that failed setup.
	body, _ := json.Marshal(dataPayload)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, adminSvc.createdProxies, 0)
	require.Len(t, adminSvc.createdProviders, 1)
}

// TestImportDataIdempotencyIgnoresDeprecatedLongContextBillingExtra repeats one
// keyed import with absent, scalar and malformed retired Extra values. The
// ignored key produces one creation, replay headers on repetitions and an
// unchanged retained neighboring Extra value.
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

// TestImportDataLeavesOpenAIOAuthModelWhitelistUnset imports OpenAI OAuth
// credentials with no whitelist key. Captured creation input keeps that key
// absent, allowing the provider's configured protocol rules to determine its
// model candidates.
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

// TestImportDataKeepsExistingOpenAIOAuthModelWhitelist imports OpenAI OAuth
// credentials with a supplied empty whitelist. Captured creation input retains
// that exact empty collection, distinguishing it from an absent credential key.
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

// TestImportDataTreatsOpenAIOAuthNullProviderFieldAsPresent imports OpenAI
// OAuth data with concurrency explicitly null. Captured creation input receives
// zero, protecting field-presence handling rather than substituting an omission
// default.
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

// TestImportDataLeavesAnthropicModelWhitelistUnset imports Anthropic OAuth data
// with no model whitelist key. Captured creation credentials keep that key
// absent after archive validation and provider preparation.
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

// TestImportDataAcceptsQoderCosyProvider imports a Qoder COSY entry containing
// PAT text. Creation occurs once and retains the selected platform,
// authentication kind and credential value in the captured input.
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

// TestImportDataRejectsQoderNonCosyProvider imports a Qoder API-key combination
// that archive validation rejects. HTTP still returns the item-outcome
// response, with one provider failure and no creation invocation.
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

// TestImportDataRejectsCosyNonQoderProvider imports an Anthropic entry using
// the Qoder-only COSY authentication kind. The archive reports one item failure
// within HTTP success and records no provider creation.
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

// ListProxies records the supplied selection and returns matching configured
// proxy values. It filters protocol, status and case-insensitive name or host
// search, while pagination and sort do not alter fixture order.
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

// GetProxy copies a matching configured proxy or synthesizes an active ID-only
// fallback. The returned scalar record is separate, while any pointed-to fields
// follow the configured proxy's shallow-copy representation.
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

// GetProxiesByIDs returns configured proxy values matching a requested
// identifier set in fixture order. Empty input returns an empty slice, and
// duplicate requested IDs do not duplicate returned configured proxies.
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

// CreateProxy captures the borrowed creation input under the inherited fixture
// mutex and returns an active synthetic proxy. The observation slice is read
// after synchronous import completion in these tests.
func (s *archiveHTTPFixture) CreateProxy(ctx context.Context, input *egress.CreateProxyInput) (*egress.Proxy, error) {
	s.mu.Lock()
	s.createdProxies = append(s.createdProxies, input)
	s.mu.Unlock()
	proxy := egress.Proxy{ID: 400, Name: input.Name, Status: billing.StatusActive}
	return &proxy, nil
}

// UpdateProxy captures the target ID and borrowed update input under the
// inherited mutex. It returns a synthetic active proxy while retaining those
// observations for serial import assertions.
func (s *archiveHTTPFixture) UpdateProxy(ctx context.Context, id int64, input *egress.UpdateProxyInput) (*egress.Proxy, error) {
	s.mu.Lock()
	s.updatedProxyIDs = append(s.updatedProxyIDs, id)
	s.updatedProxies = append(s.updatedProxies, input)
	s.mu.Unlock()
	proxy := egress.Proxy{ID: id, Name: input.Name, Status: billing.StatusActive}
	return &proxy, nil
}

// newArchiveHTTPFixture initializes shared provider mutation captures and one
// active HTTP proxy fixture. It reuses default list records and UTC timestamps,
// allowing tests to replace rows before export or import requests.
func newArchiveHTTPFixture() *archiveHTTPFixture {
	source := &archiveHTTPFixture{managementMutationFixture: newManagementMutationFixture()}
	source.providers = newManagementListFixture().providers
	now := time.Now().UTC()
	source.proxies = []egress.Proxy{{ID: 4, Name: "proxy", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: egress.StatusActive, CreatedAt: now, UpdatedAt: now}}
	return source
}

// ListProviders supplies fixture provider rows to the shared list adapter and
// returns its page. Filter and sort text is captured for assertions, while the
// substitute does not perform production database filtering.
func (s *archiveHTTPFixture) ListProviders(ctx context.Context, page, size int, platform, kind, status, search string, gid int64, privacy, sortBy, order string) ([]providercore.Record, int64, error) {
	s.list.providers = s.providers
	return s.list.ListProviders(ctx, page, size, platform, kind, status, search, gid, privacy, sortBy, order)
}

// setupProviderDataRouter mounts archive export and import with production
// proxy-transfer and archive use cases. The optional first coordinator enables
// response idempotency; this test router omits production step-up middleware
// for direct handler assertions.
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

// decodeImportResult decodes a recorded HTTP body into the typed archive-import
// result and fails the test on invalid JSON. It returns the response code and
// item counts for later assertions.
func decodeImportResult(t *testing.T, rec *httptest.ResponseRecorder) struct {
	Code int                       `json:"code"` // decoded HTTP envelope code
	Data transfer.DataImportResult `json:"data"` // decoded per-item import outcomes
} {
	t.Helper()
	var result struct {
		Code int                       `json:"code"` // decoded HTTP envelope code
		Data transfer.DataImportResult `json:"data"` // decoded per-item import outcomes
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	return result
}

// postImportProvider submits one serialized provider archive and asserts HTTP
// success. Item failures still use HTTP 200, so callers inspect the returned
// fixture effects when validating actual creation.
func postImportProvider(t *testing.T, router *gin.Engine, provider map[string]any) {
	t.Helper()

	rec := postImportProviderRaw(t, router, provider)
	require.Equal(t, http.StatusOK, rec.Code)
}

// postImportProviderRaw wraps one input provider in a current-version archive
// request and runs the import route. The returned recorder carries item
// outcomes, and the input object is borrowed during JSON marshaling.
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

	// A marshal failure supplies an empty request body to strict archive binding.
	// Return its recorded HTTP outcome so this helper's caller decides how to assert failure.
	body, _ := json.Marshal(dataPayload)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}
