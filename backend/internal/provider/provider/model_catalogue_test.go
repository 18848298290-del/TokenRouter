package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// modelCatalogueTransportFixture captures one model request and returns the
// configured response object.
type modelCatalogueTransportFixture struct {
	// resp is returned directly by the fixture. Fetch closes its body, so
	// configure a fresh response for each independent query.
	resp *http.Response
	// lastReq retains the query's prepared request pointer. Sequential assertions
	// inspect those same headers after synchronous transport capture.
	lastReq *http.Request // captured model catalogue request
}

// TestBuildUpstreamModelsRequest_CNProviders selects CN pay-as-you-go and
// coding credentials without custom roots. Each request must use its platform's
// OpenAI-format models endpoint and the configured bearer key.
func TestBuildUpstreamModelsRequest_CNProviders(t *testing.T) {
	t.Parallel()

	svc := &ModelCatalogue{Options: upstreamModelSyncTestConfig()}
	cases := []struct {
		name     string // model catalogue scenario label
		platform string // provider platform under test
		mode     string // provider service mode selection
		wantURL  string // expected complete model endpoint
	}{
		{"kimi default", capability.PlatformKimi, "", "https://api.moonshot.cn/v1/models"},
		{"kimi coding", capability.PlatformKimi, providercore.ProviderModeCoding, "https://api.kimi.com/coding/v1/models"},
		{"zhipu default", capability.PlatformZhipu, "", "https://open.bigmodel.cn/api/paas/v4/models"},
		{"zhipu coding", capability.PlatformZhipu, providercore.ProviderModeCoding, "https://open.bigmodel.cn/api/coding/paas/v4/models"},
		{"deepseek", capability.PlatformDeepseek, "", "https://api.deepseek.com/v1/models"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			creds := map[string]any{"api_key": "sk-test"}
			if tc.mode != "" {
				creds["provider_mode"] = tc.mode
			}
			provider := &providercore.Record{ID: 1, Platform: tc.platform, Type: capability.ProviderTypeAPIKey, Credentials: creds}
			req, err := svc.buildUpstreamModelsRequest(context.Background(), provider)
			require.NoError(t, err)
			require.Equal(t, tc.wantURL, req.URL.String())
			require.Equal(t, "Bearer sk-test", req.Header.Get("Authorization"))
		})
	}
}

// TestBuildUpstreamModelsRequest_AnthropicProtocol configures a Zhipu Messages
// root for inference. Model synchronization must choose the platform's
// OpenAI-format root and append its models endpoint.
func TestBuildUpstreamModelsRequest_AnthropicProtocol(t *testing.T) {
	t.Parallel()
	svc := &ModelCatalogue{Options: upstreamModelSyncTestConfig()}
	provider := &providercore.Record{
		ID: 1, Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":      "sk-test",
			"api_protocol": providercore.APIProtocolAnthropic,
			"base_url":     "https://open.bigmodel.cn/api/anthropic",
		},
	}
	req, err := svc.buildUpstreamModelsRequest(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "https://open.bigmodel.cn/api/paas/v4/models", req.URL.String())
}

// TestBuildV1ModelsURL supplies bare, versioned, complete, and prefixed
// Anthropic roots. Construction must retain prefixes and produce one v1/models
// endpoint without duplicating an existing version suffix.
func TestBuildV1ModelsURL(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://api.anthropic.com/v1/models", buildV1ModelsURL("https://api.anthropic.com"))
	require.Equal(t, "https://api.anthropic.com/v1/models", buildV1ModelsURL("https://api.anthropic.com/v1"))
	require.Equal(t, "https://api.anthropic.com/v1/models", buildV1ModelsURL("https://api.anthropic.com/v1/models"))
	require.Equal(t, "https://gateway.example.com/antigravity/v1/models", buildV1ModelsURL("https://gateway.example.com/antigravity/"))
}

// TestBuildOpenAIModelsURL supplies multiple OpenAI-compatible version roots
// and completed model paths. Endpoint construction must retain platform and
// version prefixes while appending models exactly once.
func TestBuildOpenAIModelsURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string // model catalogue scenario label
		base string // supplied model catalogue root
		want string // expected model endpoint result
	}{
		{
			name: "zhipu v4 coding base url",
			base: "https://open.bigmodel.cn/api/coding/paas/v4",
			want: "https://open.bigmodel.cn/api/coding/paas/v4/models",
		},
		{
			name: "openai v1 base url",
			base: "https://api.openai.com/v1",
			want: "https://api.openai.com/v1/models",
		},
		{
			name: "models url unchanged",
			base: "https://api.openai.com/v1/models",
			want: "https://api.openai.com/v1/models",
		},
		{
			name: "host fallback uses v1",
			base: "https://api.openai.com",
			want: "https://api.openai.com/v1/models",
		},
		{
			name: "trailing slash on v4",
			base: "https://open.bigmodel.cn/api/coding/paas/v4/",
			want: "https://open.bigmodel.cn/api/coding/paas/v4/models",
		},
		{
			name: "v2 base url",
			base: "https://gateway.example.com/openai/v2",
			want: "https://gateway.example.com/openai/v2/models",
		},
		{
			name: "v3 base url",
			base: "https://gateway.example.com/openai/v3",
			want: "https://gateway.example.com/openai/v3/models",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, buildOpenAIModelsURL(tt.base))
		})
	}
}

// TestBuildGeminiModelsURL supplies bare, versioned, and complete Gemini roots.
// Endpoint construction must produce one v1beta/models path and retain roots
// that already name that endpoint.
func TestBuildGeminiModelsURL(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://generativelanguage.googleapis.com/v1beta/models", buildGeminiModelsURL("https://generativelanguage.googleapis.com"))
	require.Equal(t, "https://generativelanguage.googleapis.com/v1beta/models", buildGeminiModelsURL("https://generativelanguage.googleapis.com/v1beta"))
	require.Equal(t, "https://generativelanguage.googleapis.com/v1beta/models", buildGeminiModelsURL("https://generativelanguage.googleapis.com/v1beta/models"))
}

// TestExtractUpstreamModelIDs parses object wrappers, arrays, Codex slugs, and
// Gemini resource names. Standard IDs must win over extra model fields, and
// results must be distinct and sorted with resource prefixes removed.
func TestExtractUpstreamModelIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string   // model catalogue scenario label
		body string   // supplied catalogue response JSON
		want []string // expected sorted model identifiers
	}{
		{
			name: "openai and anthropic data array",
			body: `{"data":[{"id":"claude-sonnet-4-5"},{"id":"gpt-5"},{"id":"gpt-5"},{"id":""}]}`,
			want: []string{"claude-sonnet-4-5", "gpt-5"},
		},
		{
			name: "gemini models array strips prefix",
			body: `{"models":[{"name":"models/gemini-2.5-pro"},{"name":"gemini-2.5-flash"}]}`,
			want: []string{"gemini-2.5-flash", "gemini-2.5-pro"},
		},
		{
			name: "top level array",
			body: `[{"id":"z-model"},{"name":"models/a-model"}]`,
			want: []string{"a-model", "z-model"},
		},
		{
			name: "standard id wins over provider-specific model field",
			body: `{"data":[{"id":"canonical-id","model":"display-model"}]}`,
			want: []string{"canonical-id"},
		},
		{
			name: "codex manifest uses slug",
			body: `{"models":[{"slug":"gpt-5.6-sol"},{"slug":"gpt-5.5-codex"}]}`,
			want: []string{"gpt-5.5-codex", "gpt-5.6-sol"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := extractUpstreamModelIDs([]byte(tt.body))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestBuildUpstreamModelsRequestSupportsOpenAIOAuth supplies account
// credentials to the generic builder. The request must use the Codex manifest
// with client version, bearer authentication, account headers, and nonempty
// client identity headers.
func TestBuildUpstreamModelsRequestSupportsOpenAIOAuth(t *testing.T) {
	svc := &ModelCatalogue{Options: upstreamModelSyncTestConfig()}
	provider := &providercore.Record{
		ID:       11,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "openai-oauth-token",
			"chatgpt_account_id": "chatgpt-provider",
		},
	}

	req, err := svc.buildUpstreamModelsRequest(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, DefaultCodexModelsURL, req.URL.Scheme+"://"+req.URL.Host+req.URL.Path)
	require.NotEmpty(t, req.URL.Query().Get("client_version"))
	require.Equal(t, "Bearer openai-oauth-token", req.Header.Get("Authorization"))
	require.Equal(t, "chatgpt-provider", req.Header.Get("chatgpt-account-id"))
	require.NotEmpty(t, req.Header.Get("Originator"))
	require.NotEmpty(t, req.Header.Get("User-Agent"))
	require.NotEmpty(t, req.Header.Get("Version"))
}

// TestFetchUpstreamSupportedModelsParsesOpenAIOAuthManifest returns Codex model
// slugs through the configured transport. Fetch must produce sorted model IDs
// while authenticating the manifest request with the OAuth token.
func TestFetchUpstreamSupportedModelsParsesOpenAIOAuthManifest(t *testing.T) {
	upstream := &modelCatalogueTransportFixture{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-5.6-sol"},{"slug":"gpt-5.5-codex"}]}`)),
	}}
	svc := &ModelCatalogue{
		Transport: upstream,
		Options:   upstreamModelSyncTestConfig(),
	}

	models, err := svc.FetchUpstreamSupportedModels(context.Background(), &providercore.Record{
		ID:       12,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "openai-oauth-token",
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-5.5-codex", "gpt-5.6-sol"}, models)
	require.Equal(t, "Bearer openai-oauth-token", upstream.lastReq.Header.Get("Authorization"))
}

// TestExtractGrokUpstreamModelIDs mixes protocol ID fields, metadata, display
// names, and invalid metadata. The result must prefer usable protocol
// identities and retain outer IDs when metadata cannot be decoded.
func TestExtractGrokUpstreamModelIDs(t *testing.T) {
	t.Parallel()

	models, err := extractGrokUpstreamModelIDs([]byte(`{"data":[{"id":"display-id","model":"grok-4.5"},{"modelId":"grok-build-0.1"},{"model_id":"grok-composer-2.5-fast"},{"name":"Grok Meta Display Name","_meta":{"model":"grok-meta"}},{"name":"grok-name"},{"id":"grok-safe","_meta":"not-an-object"}]}`))
	require.NoError(t, err)
	require.Equal(t, []string{"grok-4.5", "grok-build-0.1", "grok-composer-2.5-fast", "grok-meta", "grok-name", "grok-safe"}, models)
}

// TestBuildUpstreamModelsRequestsForAPIKeyProviders prepares Anthropic,
// bearer-Messages, OpenAI, Grok, and Gemini catalogue requests. Each must
// select its protocol root, version suffix, and configured authentication
// scheme.
func TestBuildUpstreamModelsRequestsForAPIKeyProviders(t *testing.T) {
	t.Parallel()

	svc := &ModelCatalogue{Options: upstreamModelSyncTestConfig()}
	ctx := context.Background()

	anthropicReq, err := svc.buildAnthropicUpstreamModelsRequest(ctx, &providercore.Record{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "anthropic-key",
			"base_url": "https://anthropic.example.com/v1",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://anthropic.example.com/v1/models", anthropicReq.URL.String())
	require.Equal(t, "anthropic-key", anthropic.GetHeaderRaw(anthropicReq.Header, "x-api-key"))
	require.Equal(t, "2023-06-01", anthropicReq.Header.Get("anthropic-version"))

	anthropicBearerReq, err := svc.buildAnthropicUpstreamModelsRequest(ctx, &providercore.Record{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "ollama-key",
			"base_url": "https://ollama.com",
		},
		Extra: map[string]any{
			"anthropic_apikey_auth_scheme": providercore.AnthropicAPIKeyAuthSchemeAuthorizationBearer,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://ollama.com/v1/models", anthropicBearerReq.URL.String())
	require.Equal(t, "Bearer ollama-key", anthropic.GetHeaderRaw(anthropicBearerReq.Header, "authorization"))
	require.Empty(t, anthropic.GetHeaderRaw(anthropicBearerReq.Header, "x-api-key"))
	require.Equal(t, "2023-06-01", anthropicBearerReq.Header.Get("anthropic-version"))

	openAIReq, err := svc.buildOpenAIUpstreamModelsRequest(ctx, &providercore.Record{
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "openai-key",
			"base_url": "https://openai.example.com",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://openai.example.com/v1/models", openAIReq.URL.String())
	require.Equal(t, "Bearer openai-key", openAIReq.Header.Get("Authorization"))

	grokReq, err := svc.buildUpstreamModelsRequest(ctx, &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "xai-key",
			"base_url": "https://xai.example.com/v1",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://xai.example.com/v1/models", grokReq.URL.String())
	require.Equal(t, "Bearer xai-key", grokReq.Header.Get("Authorization"))

	// Unconfigured API key providers use the same official endpoint as forwarding.
	defaultGrokReq, err := svc.buildUpstreamModelsRequest(ctx, &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "xai-key",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/models", defaultGrokReq.URL.String())

	geminiReq, err := svc.buildGeminiUpstreamModelsRequest(ctx, &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "gemini-key",
			"base_url": "https://generativelanguage.googleapis.com/v1beta",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://generativelanguage.googleapis.com/v1beta/models", geminiReq.URL.String())
	require.Equal(t, "gemini-key", geminiReq.Header.Get("x-goog-api-key"))
}

// TestBuildUpstreamModelsRequestSupportsGrokOAuth prepares a valid OAuth record
// for the trusted CLI proxy. The request must use its access token and CLI
// headers while adding the account ID and email.
func TestBuildUpstreamModelsRequestSupportsGrokOAuth(t *testing.T) {
	t.Parallel()

	svc := &ModelCatalogue{
		Options:    upstreamModelSyncTestConfig(),
		GrokTokens: &providercore.GrokTokenSource{Policy: providercore.GrokProviderRefreshPolicy()},
	}
	req, err := svc.buildUpstreamModelsRequest(context.Background(), grokOAuthModelSyncTestProvider(""))
	require.NoError(t, err)
	require.Equal(t, "https://cli-chat-proxy.grok.com/v1/models", req.URL.String())
	require.Equal(t, "Bearer oauth-access-token", req.Header.Get("Authorization"))
	require.Equal(t, xai.CLIClientVersion, req.Header.Get("X-Grok-Client-Version"))
	require.Equal(t, "interactive", req.Header.Get("X-Grok-Client-Mode"))
	require.Equal(t, xai.CLIUserAgent(xai.CLIClientVersion), req.Header.Get("User-Agent"))
	require.Equal(t, "grok-user-id", req.Header.Get("X-UserID"))
	require.Equal(t, "grok-user@example.com", req.Header.Get("X-Email"))
	require.NotContains(t, req.Header.Get("Authorization"), "oauth-refresh-token")
}

// TestBuildUpstreamModelsRequestGrokOAuthRequiresTokenProvider omits the token
// source for a stored OAuth record. Preparation must report a
// configuration-kind synchronization error before constructing the model
// request.
func TestBuildUpstreamModelsRequestGrokOAuthRequiresTokenProvider(t *testing.T) {
	t.Parallel()

	svc := &ModelCatalogue{Options: upstreamModelSyncTestConfig()}
	_, err := svc.buildUpstreamModelsRequest(context.Background(), grokOAuthModelSyncTestProvider(""))
	require.Error(t, err)

	var syncErr *providercore.UpstreamModelSyncError
	require.True(t, errors.As(err, &syncErr))
	require.Equal(t, providercore.UpstreamModelSyncErrorConfiguration, syncErr.Kind)
	require.Contains(t, syncErr.SafeMessage(), "token provider")
}

// TestBuildAnthropicUpstreamModelsRequestRejectsBedrock asks the Messages
// catalogue builder to handle a Bedrock record. It must return an
// unsupported-kind error instead of creating an Anthropic request.
func TestBuildAnthropicUpstreamModelsRequestRejectsBedrock(t *testing.T) {
	t.Parallel()

	svc := &ModelCatalogue{Options: upstreamModelSyncTestConfig()}
	_, err := svc.buildAnthropicUpstreamModelsRequest(context.Background(), &providercore.Record{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeBedrock,
	})
	require.Error(t, err)

	var syncErr *providercore.UpstreamModelSyncError
	require.True(t, errors.As(err, &syncErr))
	require.Equal(t, providercore.UpstreamModelSyncErrorUnsupported, syncErr.Kind)
}

// TestFetchUpstreamSupportedModelsParsesOpenAIResponse returns duplicate
// standard IDs and a name fallback. Fetch must return the sorted distinct list
// and send the configured bearer key to the versioned models path.
func TestFetchUpstreamSupportedModelsParsesOpenAIResponse(t *testing.T) {
	t.Parallel()

	upstream := &modelCatalogueTransportFixture{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-5"},{"id":"gpt-5"},{"name":"o3"}]}`)),
	}}
	svc := &ModelCatalogue{
		Transport: upstream,
		Options:   upstreamModelSyncTestConfig(),
	}

	models, err := svc.FetchUpstreamSupportedModels(context.Background(), &providercore.Record{
		ID:       7,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "openai-key",
			"base_url": "https://openai.example.com/v1",
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-5", "o3"}, models)
	require.Equal(t, "https://openai.example.com/v1/models", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer openai-key", upstream.lastReq.Header.Get("Authorization"))
}

// TestFetchUpstreamSupportedModelsUsesConfiguredBodyLimit returns catalogue
// JSON beyond an eight-byte ceiling. Fetch must reject the oversized body with
// a limit diagnostic before model extraction succeeds.
func TestFetchUpstreamSupportedModelsUsesConfiguredBodyLimit(t *testing.T) {
	t.Parallel()

	upstream := &modelCatalogueTransportFixture{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-5"}]}`)),
	}}
	cfg := upstreamModelSyncTestConfig()
	cfg.BodyLimit = 8
	svc := &ModelCatalogue{Transport: upstream, Options: cfg}

	_, err := svc.FetchUpstreamSupportedModels(context.Background(), &providercore.Record{
		ID:       7,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "openai-key",
			"base_url": "https://openai.example.com/v1",
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "response exceeds 8 bytes")
}

// TestFetchUpstreamSupportedModelsParsesGrokAPIKeyResponse returns repeated xAI
// catalogue IDs through an API-key transport. Fetch must deduplicate those IDs
// and retain the configured endpoint and bearer credentials.
func TestFetchUpstreamSupportedModelsParsesGrokAPIKeyResponse(t *testing.T) {
	t.Parallel()

	upstream := &modelCatalogueTransportFixture{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"grok-4.5"},{"id":"grok-4.5"},{"id":"grok-imagine"}]}`)),
	}}
	svc := &ModelCatalogue{
		Transport: upstream,
		Options:   upstreamModelSyncTestConfig(),
	}

	models, err := svc.FetchUpstreamSupportedModels(context.Background(), &providercore.Record{
		ID:       9,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "xai-key",
			"base_url": "https://xai.example.com/v1",
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"grok-4.5", "grok-imagine"}, models)
	require.Equal(t, "https://xai.example.com/v1/models", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer xai-key", upstream.lastReq.Header.Get("Authorization"))
}

// TestFetchUpstreamSupportedModelsParsesGrokOAuthResponse returns protocol
// model fields from the trusted CLI proxy. Fetch must deduplicate and sort them
// while using OAuth credentials, CLI identity, and account headers.
func TestFetchUpstreamSupportedModelsParsesGrokOAuthResponse(t *testing.T) {
	t.Parallel()

	upstream := &modelCatalogueTransportFixture{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"model":"grok-4.5"},{"model":"grok-4.5"},{"modelId":"grok-build-0.1"}]}`)),
	}}
	svc := &ModelCatalogue{
		Transport:  upstream,
		Options:    upstreamModelSyncTestConfig(),
		GrokTokens: &providercore.GrokTokenSource{Policy: providercore.GrokProviderRefreshPolicy()},
	}

	models, err := svc.FetchUpstreamSupportedModels(context.Background(), grokOAuthModelSyncTestProvider(""))
	require.NoError(t, err)
	require.Equal(t, []string{"grok-4.5", "grok-build-0.1"}, models)
	require.Equal(t, "https://cli-chat-proxy.grok.com/v1/models", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer oauth-access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, xai.CLIClientVersion, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.Equal(t, "interactive", upstream.lastReq.Header.Get("X-Grok-Client-Mode"))
	require.Equal(t, "grok-user-id", upstream.lastReq.Header.Get("X-UserID"))
	require.Equal(t, "grok-user@example.com", upstream.lastReq.Header.Get("X-Email"))
}

// TestBuildUpstreamModelsRequestGrokOAuthDoesNotSendIdentityToCustomBase
// selects a custom relay for an OAuth record. The models path must use that
// root while user ID and email headers remain absent.
func TestBuildUpstreamModelsRequestGrokOAuthDoesNotSendIdentityToCustomBase(t *testing.T) {
	t.Parallel()

	svc := &ModelCatalogue{
		Options:    upstreamModelSyncTestConfig(),
		GrokTokens: &providercore.GrokTokenSource{Policy: providercore.GrokProviderRefreshPolicy()},
	}
	req, err := svc.buildUpstreamModelsRequest(context.Background(), grokOAuthModelSyncTestProvider("https://relay.example/v1"))
	require.NoError(t, err)
	require.Equal(t, "https://relay.example/v1/models", req.URL.String())
	require.Empty(t, req.Header.Get("X-UserID"))
	require.Empty(t, req.Header.Get("X-Email"))
}

// TestFetchUpstreamSupportedModelsDoesNotExposeUpstreamBody returns HTTP 502
// with a secret marker in its JSON body. Raw and safe diagnostics must omit
// that marker, and the typed error must retain its kind and safe HTTP status.
func TestFetchUpstreamSupportedModelsDoesNotExposeUpstreamBody(t *testing.T) {
	t.Parallel()

	upstream := &modelCatalogueTransportFixture{resp: &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"SECRET_TOKEN should not be exposed"}`)),
	}}
	svc := &ModelCatalogue{
		Transport: upstream,
		Options:   upstreamModelSyncTestConfig(),
	}

	_, err := svc.FetchUpstreamSupportedModels(context.Background(), &providercore.Record{
		ID:       8,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "openai-key",
			"base_url": "https://openai.example.com/v1",
		},
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "SECRET_TOKEN")

	var syncErr *providercore.UpstreamModelSyncError
	require.True(t, errors.As(err, &syncErr))
	require.Equal(t, providercore.UpstreamModelSyncErrorUpstream, syncErr.Kind)
	require.NotContains(t, syncErr.SafeMessage(), "SECRET_TOKEN")
	require.Contains(t, syncErr.SafeMessage(), "HTTP 502")
}

// grokOAuthModelSyncTestProvider creates an OAuth catalogue record with future
// expiry and account hints. A nonblank supplied root is stored as given; blank
// input leaves platform-root selection to the builder.
func grokOAuthModelSyncTestProvider(baseURL string) *providercore.Record {
	credentials := map[string]any{
		"access_token":  "oauth-access-token",
		"refresh_token": "oauth-refresh-token",
		"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"sub":           "grok-user-id",
		"email":         "grok-user@example.com",
	}
	if strings.TrimSpace(baseURL) != "" {
		credentials["base_url"] = baseURL
	}
	return &providercore.Record{
		ID:          10,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Credentials: credentials,
	}
}

// upstreamModelSyncTestConfig supplies operator URL validation, an eight-MiB
// body limit, and the default Codex manifest endpoint. Both ordinary and Grok
// operator roots use the same validator in these fixtures.
func upstreamModelSyncTestConfig() ModelCatalogueOptions {
	policy := egress.OperatorURLPolicy{}
	return ModelCatalogueOptions{ValidateURL: policy.Validate, OperatorValidator: policy.Validate, BodyLimit: 8 << 20, CodexModelsURL: DefaultCodexModelsURL}
}

// DoWithTLS captures the request pointer and returns the configured response
// directly. The query caller owns body cleanup, and the fixture ignores proxy,
// concurrency, and selected TLS profile.
func (f *modelCatalogueTransportFixture) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	f.lastReq = req
	return f.resp, nil
}

// TestJevModelSyncUsesNames prepares a native Jev catalogue request and parses
// name-based entries. The endpoint and bearer key must match the platform
// defaults, and IDs must be sorted from complete names.
func TestJevModelSyncUsesNames(t *testing.T) {
	service := &ModelCatalogue{Options: upstreamModelSyncTestConfig()}
	value := &providercore.Record{Platform: providercore.PlatformJev, Type: providercore.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}
	request, err := service.buildUpstreamModelsRequest(context.Background(), value)
	require.NoError(t, err)
	require.Equal(t, "https://api.typesafe.ai/v1/models", request.URL.String())
	require.Equal(t, "Bearer test", request.Header.Get("Authorization"))
	ids, err := extractJevModelIDs([]byte(`{"models":[{"name":"jev-preview","description":"preview"},{"name":"jev-latest","release_date":"2026-09-15"}]}`))
	require.NoError(t, err)
	require.Equal(t, []string{"jev-latest", "jev-preview"}, ids)
}
