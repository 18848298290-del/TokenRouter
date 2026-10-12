//go:build integration

package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
)

// These constructors combine platform probing with application activity tracking in integration tests.
var (
	NewAntigravityRetryForTest = provideAntigravityRetry
	NewAntigravityProbeForTest = provideAntigravityProbe
	NewGatewayActivityForTest  = provideGatewayRequestActivity
)

type antigravityProbeBody struct {
	io.Reader
	closed bool
}

// antigravityProbeTransport supplies local response streams; database reads, token lookup, retry, and SSE
// handling use production components.
type antigravityProbeTransport struct {
	requests  []*http.Request
	bodies    []string
	responses []*antigravityProbeBody
}

func (b *antigravityProbeBody) Close() error { b.closed = true; return nil }

func (s *antigravityProbeTransport) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, string(body))
	response := &antigravityProbeBody{Reader: strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"probe result\"}]}}]}}\n\n")}
	s.responses = append(s.responses, response)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: response}, nil
}

func (s *antigravityProbeTransport) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxy, id, concurrency)
}

func TestNativeAntigravityProbeAssembly(t *testing.T) {
	t.Setenv("GATEWAY_ANTIGRAVITY_FORWARD_BASE_URL", "prod")
	f := newDatabaseFixture(t)
	store := providerpostgres.NewProviderStore(f.client, f.db, providerpostgres.ProviderStoreOptions{})
	cfg := &config.Config{}
	runtime := NewProviderHealthRuntimeForTest(store, nil, cfg, nil, nil, nil, nil, nil)
	tokens := &provider.AntigravityTokenSource{}
	transport := &antigravityProbeTransport{}
	retry := NewAntigravityRetryForTest(store, nil, runtime, nil, transport, cfg)
	manager := lifecycle.New()
	activity := NewGatewayActivityForTest(manager)
	probe := NewAntigravityProbeForTest(tokens, retry, activity)
	core := NewProviderTestsForTest(store, nil, nil, nil, probe, transport, cfg, nil, nil, nil, nil, manager, nil)
	require.Empty(t, transport.requests)

	row, err := f.client.Provider.Create().SetName("test-antigravity-probe").SetPlatform(provider.PlatformAntigravity).SetType(provider.ProviderTypeOAuth).SetCredentials(map[string]any{
		"access_token": "fixture-probe-token", "project_id": "fixture-project",
		"expires_at":    time.Now().Add(time.Hour).Format(time.RFC3339),
		"model_mapping": map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5", "gemini-3.1-pro-preview": "gemini-3.1-pro-high"},
	}).Save(t.Context())
	require.NoError(t, err)
	for _, model := range []string{"claude-sonnet-4-5", "gemini-3.1-pro-preview"} {
		t.Run(model, func(t *testing.T) {
			before := len(transport.requests)
			recorder := httptest.NewRecorder()
			err := core.Test(t.Context(), provider.TestRequest{ProviderID: row.ID, Model: model, Prompt: "fixed probe prompt", Automatic: true, UserAgent: "probe-client/9.9"}, providerhttp.NewTestEventSink(recorder))
			require.NoError(t, err)
			require.Len(t, transport.requests, before+1)
			require.Equal(t, "Bearer fixture-probe-token", transport.requests[before].Header.Get("Authorization"))
			require.Equal(t, "probe-client/9.9", transport.requests[before].Header.Get("User-Agent"))
			require.Contains(t, transport.requests[before].URL.Path, "streamGenerateContent")
			require.Contains(t, transport.bodies[before], "fixed probe prompt")
			require.True(t, transport.responses[before].closed)
			body := recorder.Body.String()
			require.Less(t, strings.Index(body, `"type":"test_start"`), strings.Index(body, `"type":"content"`))
			require.Less(t, strings.Index(body, `"type":"content"`), strings.Index(body, `"type":"test_complete"`))
			require.Contains(t, body, `"text":"probe result"`)
			require.Contains(t, body, `"success":true`)
			require.NotContains(t, body, "fixture-probe-token")
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, manager.Stop(ctx))
	before := len(transport.requests)
	recorder := httptest.NewRecorder()
	err = core.Test(t.Context(), provider.TestRequest{ProviderID: row.ID, Model: "claude-sonnet-4-5"}, providerhttp.NewTestEventSink(recorder))
	require.Error(t, err)
	require.Len(t, transport.requests, before, "关闭屏障后不再开始供应商推理")
}
