package provider

// These scenarios exercise provider selection through the Manager and Brave HTTP executor.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/search"
)

type managerFixture struct {
	*search.Manager
	*Executor
}

func TestManager_SearchWithBestProvider_UsesFirstAvailable(t *testing.T) {
	srvBrave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := braveResponse{}
		resp.Web.Results = []braveResult{{URL: "https://brave.com", Title: "Brave", Description: "from brave"}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srvBrave.Close()

	origURL := *braveSearchURL
	u, _ := http.NewRequest(http.MethodGet, srvBrave.URL, nil)
	*braveSearchURL = *u.URL
	defer func() { *braveSearchURL = origURL }()

	m := NewManager([]ProviderConfig{
		{Type: "brave", APIKey: "k1"},
		{Type: "tavily", APIKey: "k2"},
	}, nil)
	m.clientCache[srvBrave.URL] = srvBrave.Client()
	m.clientCache[""] = srvBrave.Client()

	resp, providerName, err := m.SearchWithBestProvider(context.Background(), SearchRequest{Query: "test"})
	require.NoError(t, err)
	require.Equal(t, "brave", providerName)
	require.Len(t, resp.Results, 1)
	require.Equal(t, "from brave", resp.Results[0].Snippet)
}

func TestManager_SearchWithBestProvider_NilRedis(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := braveResponse{}
		resp.Web.Results = []braveResult{{URL: "https://test.com", Title: "Test", Description: "result"}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	origURL := *braveSearchURL
	u, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	*braveSearchURL = *u.URL
	defer func() { *braveSearchURL = origURL }()

	m := NewManager([]ProviderConfig{
		{Type: "brave", APIKey: "k", QuotaLimit: 100},
	}, nil)
	m.clientCache[""] = srv.Client()

	resp, _, err := m.SearchWithBestProvider(context.Background(), SearchRequest{Query: "test"})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
}

func NewManager(configs []ProviderConfig, _ any) *managerFixture {
	executor := NewExecutor()
	return &managerFixture{Manager: search.NewManager(configs, nil, executor, nil), Executor: executor}
}
