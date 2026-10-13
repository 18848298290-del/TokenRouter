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

// Provider connection tests load persisted credentials and dispatch through
// application-assembled platform targets. A recording transport supplies each
// platform's stream shape, letting the scenario check its request path and
// selected provider alongside typed test-start and successful completion
// events.

// nativeProviderTestTransport records platform test requests and serves a
// configured stream body.
type nativeProviderTestTransport struct {
	body string // configured platform stream
	// requests and ids are appended together for each Do invocation. Sequential
	// subtests inspect the corresponding indexes after platform Test returns, retaining each supplied request pointer.
	requests []*http.Request // captured upstream requests
	ids      []int64         // captured provider identities
}

// Do records the request and provider ID before returning the configured
// stream. The caller closes the owned response body, while proxy and
// concurrency inputs are ignored by this sequential platform-test fixture.
func (f *nativeProviderTestTransport) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	f.requests = append(f.requests, req)
	f.ids = append(f.ids, id)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(f.body))}, nil
}

// DoWithTLS delegates the configured platform test exchange while ignoring TLS
// profile.
func (f *nativeProviderTestTransport) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return f.Do(req, proxy, id, concurrency)
}

// TestNativeProviderTestAssembly runs OpenAI, Gemini, Anthropic, and Kimi tests
// against stored API-Key providers. It checks construction performs no
// exchange, each platform uses its expected path and provider, and SSE contains
// start and successful completion events.
func TestNativeProviderTestAssembly(t *testing.T) {
	f := newDatabaseFixture(t)
	store := providerpostgres.NewProviderStore(f.client, f.db, providerpostgres.ProviderStoreOptions{})
	transport := &nativeProviderTestTransport{}
	manager := lifecycle.New()
	core := NewProviderTestsForTest(store, nil, nil, nil, nil, transport, &config.Config{}, nil, nil, nil, nil, manager, nil)
	require.Empty(t, transport.requests, "构造不请求供应商")
	for _, test := range []struct {
		platform, model, path, body string // platform: tested provider platform; model: requested test model; path: expected upstream path; body: supplied protocol stream
	}{
		{provider.PlatformOpenAI, "gpt-5.4", "/v1/responses", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"},
		{provider.PlatformGemini, "gemini-2.5-flash", "/v1beta/models/gemini-2.5-flash:streamGenerateContent", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}\n\ndata: [DONE]\n\n"},
		{provider.PlatformAnthropic, "claude-sonnet-4-5", "/v1/messages", "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
		{provider.PlatformKimi, "kimi-k2", "/v1/chat/completions", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
	} {
		t.Run(test.platform, func(t *testing.T) {
			row, err := f.client.Provider.Create().SetName("test-native-test-" + test.platform).SetPlatform(test.platform).SetType(provider.ProviderTypeAPIKey).SetCredentials(map[string]any{"api_key": "fixture-key", "base_url": "https://upstream.example", "api_protocol": "chat_completions"}).Save(t.Context())
			require.NoError(t, err)
			transport.body = test.body
			before := len(transport.requests)
			recorder := httptest.NewRecorder()
			kind := provider.ProviderTestTypeText
			err = core.Test(t.Context(), provider.TestRequest{ProviderID: row.ID, Model: test.model, Prompt: "hello", Type: &kind}, providerhttp.NewTestEventSink(recorder))
			require.NoError(t, err)
			require.Len(t, transport.requests, before+1)
			require.Equal(t, row.ID, transport.ids[before])
			require.Equal(t, test.path, transport.requests[before].URL.Path)
			require.Contains(t, recorder.Body.String(), `"type":"test_start"`)
			require.Contains(t, recorder.Body.String(), `"type":"test_complete","success":true`)
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Join registered test resources after all platform subtests finish while
	// the fixture database remains available for any final provider cleanup work.
	require.NoError(t, manager.Stop(ctx))
}
