package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/search"
)

// Production Manager selection can execute Brave through a local HTTP server
// using a real executor with fixture clients installed. The scenarios retain
// Brave result snippets and verify quota-store absence still permits a
// positive-limit candidate; shared endpoint and client-cache mutation happen
// before the searches.

// managerFixture exposes a Manager and the same executor injected for its
// searches.
type managerFixture struct {
	// Manager and Executor share the executor created by NewManager. Tests access
	// its clientCache through embedding and configure it before any concurrent
	// HTTP execution begins.
	*search.Manager // tested selection manager instance
	*Executor       // injected supplier executor instance
}

// TestManager_SearchWithBestProvider_UsesFirstAvailable provides Brave before
// Tavily with unlimited quota and local Brave response data. Search reports
// Brave and retains the decoded result count and snippet.
func TestManager_SearchWithBestProvider_UsesFirstAvailable(t *testing.T) {
	srvBrave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := braveResponse{}
		resp.Web.Results = []braveResult{{URL: "https://brave.com", Title: "Brave", Description: "from brave"}}
		// Emit the encodable Brave fixture and suppress handler write errors; the
		// outer search assertions require a successfully decoded result and snippet.
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srvBrave.Close()

	// Save and restore the shared Brave endpoint for each serial scenario;
	// concurrent Brave tests would otherwise observe this temporary local address.
	origURL := *braveSearchURL
	// Obtain the parsed URL from the known-valid local server address, suppressing
	// this fixture's request-construction error before endpoint replacement.
	u, _ := http.NewRequest(http.MethodGet, srvBrave.URL, nil)
	*braveSearchURL = *u.URL
	defer func() { *braveSearchURL = origURL }()

	m := NewManager([]ProviderConfig{
		{Type: "brave", APIKey: "k1"},
		{Type: "tavily", APIKey: "k2"},
	}, nil)
	// Populate both fixture entries before execution; this request selects the
	// empty direct-proxy key while the server-URL key remains additional cache
	// setup.
	m.clientCache[srvBrave.URL] = srvBrave.Client()
	m.clientCache[""] = srvBrave.Client()

	resp, providerName, err := m.SearchWithBestProvider(context.Background(), SearchRequest{Query: "test"})
	require.NoError(t, err)
	require.Equal(t, "brave", providerName)
	require.Len(t, resp.Results, 1)
	require.Equal(t, "from brave", resp.Results[0].Snippet)
}

// TestManager_SearchWithBestProvider_NilRedis provides a positive-limit Brave
// candidate with no quota state. A real local HTTP exchange succeeds and
// returns one result, checking the configured fail-open attempt path.
func TestManager_SearchWithBestProvider_NilRedis(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := braveResponse{}
		resp.Web.Results = []braveResult{{URL: "https://test.com", Title: "Test", Description: "result"}}
		// Emit the encodable Brave fixture and suppress handler write errors; the
		// outer assertions require successful decoding and one normalized result.
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// Save and restore the shared Brave endpoint for each serial scenario;
	// concurrent Brave tests would otherwise observe this temporary local address.
	origURL := *braveSearchURL
	// Obtain the parsed URL from the known-valid local server address, suppressing
	// this fixture's request-construction error before endpoint replacement.
	u, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	*braveSearchURL = *u.URL
	defer func() { *braveSearchURL = origURL }()

	m := NewManager([]ProviderConfig{
		{Type: "brave", APIKey: "k", QuotaLimit: 100},
	}, nil)
	// Install the local server client before executing the Manager so its
	// direct-proxy cache lookup uses the fixture exchange transport.
	m.clientCache[""] = srv.Client()

	resp, _, err := m.SearchWithBestProvider(context.Background(), SearchRequest{Query: "test"})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
}

// NewManager constructs a fixture Manager and exposes its injected real
// executor. The second argument is ignored and quota state is nil; tests
// populate clientCache before starting searches.
func NewManager(configs []ProviderConfig, _ any) *managerFixture {
	executor := NewExecutor()
	return &managerFixture{Manager: search.NewManager(configs, nil, executor, nil), Executor: executor}
}
