package httpapi

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// proxyAdminFixture records proxy writes and probe calls for admin handler tests.
//
// mu protects createdProxies, updatedProxies, updatedProxyIDs, and
// testedProxyIDs because post-import probes can run asynchronously. proxies,
// proxyCounts, and lastListProxies are configured and read serially by the
// handler fixtures.
type proxyAdminFixture struct {
	proxies []egress.Proxy // shared ordinary proxy fixture records
	// shared augmented proxy fixture records
	proxyCounts []egress.ProxyWithProviderCount
	// createdProxies retains the pointers passed to CreateProxy for later fixture
	// inspection. Tests leave those input objects stable until all tracked
	// background callbacks finish.
	createdProxies []*egress.CreateProxyInput // retained proxy creation input pointers
	// updatedProxies retains the pointers passed to UpdateProxy with the
	// corresponding IDs. Tests serialize writes and read recorded collections
	// under mu when callbacks can be active.
	updatedProxies []*egress.UpdateProxyInput // retained proxy update input pointers
	// updatedProxyIDs records update targets; testedProxyIDs records connectivity
	// probe targets.
	updatedProxyIDs, testedProxyIDs []int64
	mu                              sync.Mutex // asynchronous recording fixture lock
	// lastListProxies retains the latest filter and ordering inputs from serial
	// ordinary listing calls. Tests read these values after handler execution
	// because this recording is outside mu.
	lastListProxies struct {
		// protocol records scheme filtering; status records availability filtering;
		// search records the submitted query; sortBy records ordering selection;
		// sortOrder records ordering direction.
		protocol, status, search, sortBy, sortOrder string
		calls                                       int // observed ordinary listing calls
	} // latest ordinary listing observations
}

// TestProxy records the selected probe ID under mu and returns a fixed success
// result. It performs no network I/O, allowing import tests to observe task
// scheduling separately from actual proxy connectivity.
func (s *proxyAdminFixture) TestProxy(ctx context.Context, id int64) (*egress.ProxyTestResult, error) {
	s.mu.Lock()
	s.testedProxyIDs = append(s.testedProxyIDs, id)
	s.mu.Unlock()
	return &egress.ProxyTestResult{Success: true, Message: "ok"}, nil
}

// newProxyAdminFixture supplies one active loopback-shaped proxy and an
// augmented record with one provider. Timestamps use direct UTC wall clock, and
// tests can replace the retained slices before serving requests.
func newProxyAdminFixture() *proxyAdminFixture {
	now := time.Now().UTC()
	p := egress.Proxy{ID: 4, Name: "proxy", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: billing.StatusActive, CreatedAt: now, UpdatedAt: now}
	return &proxyAdminFixture{proxies: []egress.Proxy{p}, proxyCounts: []egress.ProxyWithProviderCount{{Proxy: p, ProviderCount: 1}}}
}

// ListProxies captures ordinary filter/order inputs and returns matching
// fixture values with their count. Search matches names or hosts
// case-insensitively here; pagination and sorting are recorded but not
// implemented by this serial fixture.
func (s *proxyAdminFixture) ListProxies(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]egress.Proxy, int64, error) {
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

// ListProxiesWithProviderCount returns the configured augmented slice and its
// length directly. It performs no filtering, sorting, or pagination, so tests
// supply the order and values they expect the handler to serialize.
func (s *proxyAdminFixture) ListProxiesWithProviderCount(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]egress.ProxyWithProviderCount, int64, error) {
	return s.proxyCounts, int64(len(s.proxyCounts)), nil
}

// GetAllProxies returns the retained ordinary fixture slice directly with no
// filtering. Tests configure active records when exercising this route;
// returned optional values remain associated with those shared fixture records.
func (s *proxyAdminFixture) GetAllProxies(ctx context.Context) ([]egress.Proxy, error) {
	return s.proxies, nil
}

// GetAllProxiesWithProviderCount returns the retained augmented fixture slice
// and a nil error. Tests configure its contents before handler execution, and
// this fixture performs no active-status filtering or cloning.
func (s *proxyAdminFixture) GetAllProxiesWithProviderCount(ctx context.Context) ([]egress.ProxyWithProviderCount, error) {
	return s.proxyCounts, nil
}

// GetProxy returns a shallow copy of the first fixture record with a matching
// ID. Missing IDs produce a synthetic active record rather than a lookup error,
// keeping route tests focused on successful response behavior.
func (s *proxyAdminFixture) GetProxy(ctx context.Context, id int64) (*egress.Proxy, error) {
	for i := range s.proxies {
		proxy := s.proxies[i]
		if proxy.ID == id {
			return &proxy, nil
		}
	}
	proxy := egress.Proxy{ID: id, Name: "proxy", Status: billing.StatusActive}
	return &proxy, nil
}

// GetProxiesByIDs returns retained records matching selected IDs in fixture
// order. Empty input returns a present empty slice; duplicates collapse through
// set membership, and optional pointers remain shared with configured records.
func (s *proxyAdminFixture) GetProxiesByIDs(ctx context.Context, ids []int64) ([]egress.Proxy, error) {
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

// CreateProxy retains the input pointer under mu and returns a synthetic active
// record with ID 400. It does not insert into proxies or run a probe, letting
// handler/import tests separately inspect creation and queued reused-ID work.
func (s *proxyAdminFixture) CreateProxy(ctx context.Context, input *egress.CreateProxyInput) (*egress.Proxy, error) {
	s.mu.Lock()
	s.createdProxies = append(s.createdProxies, input)
	s.mu.Unlock()
	proxy := egress.Proxy{ID: 400, Name: input.Name, Status: billing.StatusActive}
	return &proxy, nil
}

// UpdateProxy records its ID and input pointer under mu and returns a synthetic
// active record. It does not apply fields to proxies, so tests inspect recorded
// writes rather than model persisted replacement behavior.
func (s *proxyAdminFixture) UpdateProxy(ctx context.Context, id int64, input *egress.UpdateProxyInput) (*egress.Proxy, error) {
	s.mu.Lock()
	s.updatedProxyIDs = append(s.updatedProxyIDs, id)
	s.updatedProxies = append(s.updatedProxies, input)
	s.mu.Unlock()
	proxy := egress.Proxy{ID: id, Name: input.Name, Status: billing.StatusActive}
	return &proxy, nil
}

// DeleteProxy returns success without mutating the configured fixture proxy
// collection.
func (s *proxyAdminFixture) DeleteProxy(ctx context.Context, id int64) error {
	return nil
}

// BatchDeleteProxies returns the supplied ID slice as successful deletion
// identities without mutation. It performs no provider checks or deletion work,
// and the returned slice remains shared with the request.
func (s *proxyAdminFixture) BatchDeleteProxies(ctx context.Context, ids []int64) (*egress.ProxyBatchDeleteResult, error) {
	return &egress.ProxyBatchDeleteResult{DeletedIDs: ids}, nil
}

// GetProxyProviders returns one fixed provider summary for administrator route
// response fixtures.
func (s *proxyAdminFixture) GetProxyProviders(ctx context.Context, proxyID int64) ([]egress.ProxyProviderSummary, error) {
	return []egress.ProxyProviderSummary{{ID: 1, Name: "provider"}}, nil
}

// CheckProxyExists returns false for every connection tuple in this handler
// fixture.
func (s *proxyAdminFixture) CheckProxyExists(ctx context.Context, host string, port int, username, password string) (bool, error) {
	return false, nil
}

// CheckProxyQuality returns a fixed assessment with the requested proxy ID and
// direct wall-clock timestamp. Counters and item contents are configured
// independently in this fixture, so route tests check response delivery rather
// than real aggregation.
func (s *proxyAdminFixture) CheckProxyQuality(ctx context.Context, id int64) (*egress.ProxyQualityCheckResult, error) {
	return &egress.ProxyQualityCheckResult{
		ProxyID:        id,
		Score:          95,
		Grade:          "A",
		Summary:        "通过 5 项，告警 0 项，失败 0 项，挑战 0 项",
		PassedCount:    5,
		WarnCount:      0,
		FailedCount:    0,
		ChallengeCount: 0,
		CheckedAt:      time.Now().Unix(),
		Items: []egress.ProxyQualityCheckItem{
			{Target: "base_connectivity", Status: "pass", Message: "ok"},
			{Target: "openai", Status: "pass", HTTPStatus: 401},
			{Target: "anthropic", Status: "pass", HTTPStatus: 401},
			{Target: "gemini", Status: "pass", HTTPStatus: 200},
		},
	}, nil
}
