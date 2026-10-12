package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type codexProviderIdentityRepoStub struct {
	gatewayprovider.ExecutionProviderStore

	provider *gatewayprovider.ExecutionProvider
}

func TestOpenAIAgentIdentityPassthroughKeepsSessionAndPromptCacheHeaders(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 24,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"auth_mode":          providercore.OpenAIAuthModeAgentIdentity,
				"agent_runtime_id":   key.RuntimeID,
				"agent_private_key":  privateKey,
				"task_id":            key.TaskID,
				"chatgpt_account_id": "provider-agent-passthrough",
			},
		},
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","instructions":"Reply OK","input":[],"stream":true,"prompt_cache_key":"cache-agent"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("session_id", "client-session")
	c.Request.Header.Set("conversation_id", "client-conversation")
	c.Request.Header.Set("Authorization", "Bearer inbound-must-not-forward")

	svc := newResponsesFixture(responsesFixtureInputs{})
	req, err := svc.Requests.BuildPassthrough(context.Background(), c, provider, body, "")
	require.NoError(t, err)
	require.Equal(t, "AgentAssertion", strings.SplitN(req.Header.Get("Authorization"), " ", 2)[0])
	require.Equal(t, "provider-agent-passthrough", req.Header.Get("chatgpt-account-id"))
	require.NotEqual(t, "client-session", req.Header.Get("session_id"))
	require.NotEqual(t, "client-conversation", req.Header.Get("conversation_id"))
	require.Equal(t, openai.IsolateOpenAIUpstreamSessionID(0, provideradapter.CodexIdentityNamespace(provider.View()), "client-session"), req.Header.Get("session_id"))
	require.Equal(t, openai.IsolateOpenAIUpstreamSessionID(0, provideradapter.CodexIdentityNamespace(provider.View()), "client-conversation"), req.Header.Get("conversation_id"))
	requestBody, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Contains(t, string(requestBody), `"prompt_cache_key":"cache-agent"`)

	// Compare the same OAuth request to check session isolation and prompt caching across authentication modes.
	oauthProvider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 26,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"chatgpt_account_id": "provider-agent-passthrough",
			},
		},
	}
	oauthRecorder := httptest.NewRecorder()
	oauthContext, _ := gin.CreateTestContext(oauthRecorder)
	oauthContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	oauthContext.Request.Header.Set("session_id", "client-session")
	oauthContext.Request.Header.Set("conversation_id", "client-conversation")
	oauthReq, err := svc.Requests.BuildPassthrough(context.Background(), oauthContext, oauthProvider, body, "oauth-token")
	require.NoError(t, err)
	require.Equal(t, oauthReq.Header.Get("session_id"), req.Header.Get("session_id"))
	require.Equal(t, oauthReq.Header.Get("conversation_id"), req.Header.Get("conversation_id"))
}

func TestOpenAIAgentIdentityErrorRedactionDoesNotLeakCredentialValues(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 25,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"auth_mode":         providercore.OpenAIAuthModeAgentIdentity,
				"agent_runtime_id":  key.RuntimeID,
				"agent_private_key": privateKey,
				"task_id":           key.TaskID,
				"access_token":      key.RuntimeID + "-oauth-value",
			},
		},
	}
	svc := newResponsesFixture(responsesFixtureInputs{})
	oauthValue := provider.View().GetCredential("access_token")
	redacted := svc.Requests.Identity.Redact(context.Background(), provider, []byte(`{"message":"runtime-test task-test `+oauthValue+` AgentAssertion abc123"}`))
	require.NotContains(t, string(redacted), key.RuntimeID)
	require.NotContains(t, string(redacted), key.TaskID)
	require.NotContains(t, string(redacted), oauthValue)
	require.NotContains(t, string(redacted), "AgentAssertion abc123")
	require.Contains(t, string(redacted), "[redacted]")
}

func TestOpenAIAuthenticationHeadersPreserveOAuthPATAndAPIKeyBearerModes(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	tests := []struct {
		name     string
		provider *gatewayprovider.ExecutionProvider
		token    string
	}{
		{name: "oauth", provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}, token: "oauth-runtime-token"},
		{name: "personal access token", provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"auth_mode": providercore.OpenAIAuthModePersonalAccessToken}}}, token: "pat-runtime-token"},
		{name: "api key", provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}, token: "api-key-runtime-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers, err := svc.Requests.Identity.Headers(context.Background(), tt.provider, tt.token)
			require.NoError(t, err)
			require.Equal(t, "Bearer "+tt.token, headers.Get("Authorization"))
		})
	}
}

func newFingerprintExecutionProvider(id int64, extra map[string]any) *gatewayprovider.ExecutionProvider {
	if providercore.CodexFingerprintModeRequiresSeed(providercore.CodexFingerprintModeFromExtra(extra)) {
		if extra == nil {
			extra = make(map[string]any)
		}
		if _, exists := extra[providercore.CodexFingerprintSeedExtraKey]; !exists {
			extra[providercore.CodexFingerprintSeedExtraKey] = testCodexFingerprintSeed
		}
	}
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeOAuth,
			Extra:    extra,
		},
	}
}

func TestBuildUpstreamRequestOpenAIPassthrough_AppliesStagedFingerprint(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	// Check outbound passthrough headers with fingerprint unification enabled (#5610).
	provider := newFingerprintExecutionProvider(2001, map[string]any{
		"openai_oauth_passthrough": true,
		"codex_fingerprint_mode":   "session",
	})

	c := newFingerprintStageTestContext(t)
	c.Request.Header.Set("session_id", "real-client-session")
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color")
	c.Request.Header.Set("originator", "codex_cli_rs")
	c.Request.Header.Set("x-codex-turn-metadata", `{"installation_id":"real-install","session_id":"real-session","sandbox":"seatbelt"}`)

	// Reproduce passthrough parsing and staging with the default session mode.
	ids := provideradapter.CodexFingerprintIDsFromRequest(provider.View(), c.Request.Header)
	require.NotNil(t, ids)
	StageCodexFingerprintIDs(c, ids)

	body := []byte(`{"model":"gpt-5.6-sol","input":[],"stream":true}`)
	req, err := svc.Requests.BuildPassthrough(context.Background(), c, provider, body, "test-token")
	require.NoError(t, err)

	assert.Equal(t, ids.SessionID, req.Header.Get("session_id"), "session 模式下出站 session_id 应为提供商级收敛值")
	assert.Equal(t, ids.InstallationID, req.Header.Get("x-codex-installation-id"))
	assert.Equal(t, ids.WindowID, req.Header.Get("x-codex-window-id"))
	assert.Equal(t, ids.ThreadID, req.Header.Get("x-client-request-id"))
	turnMetadata := req.Header.Get("x-codex-turn-metadata")
	require.NotEmpty(t, turnMetadata)
	assert.Contains(t, turnMetadata, ids.SessionID, "turn-metadata JSON 中的 session_id 应被收敛")
	assert.Contains(t, turnMetadata, `"sandbox":"seatbelt"`, "turn-metadata 未指定字段应原样保留")
}

func TestBuildUpstreamRequestOpenAIPassthrough_OffModeKeepsIsolatedSession(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := newFingerprintExecutionProvider(2002, map[string]any{
		"openai_oauth_passthrough": true,
		"codex_fingerprint_mode":   "off",
	})

	c := newFingerprintStageTestContext(t)
	c.Request.Header.Set("session_id", "real-client-session")
	c.Request.Header.Set("originator", "codex_cli_rs")

	ids := provideradapter.CodexFingerprintIDsFromRequest(provider.View(), c.Request.Header)
	require.Nil(t, ids)
	StageCodexFingerprintIDs(c, ids)

	body := []byte(`{"model":"gpt-5.6-sol","input":[],"stream":true}`)
	req, err := svc.Requests.BuildPassthrough(context.Background(), c, provider, body, "test-token")
	require.NoError(t, err)

	assert.NotEmpty(t, req.Header.Get("session_id"))
	assert.NotEqual(t, openai.ResolveConvergedSessionID(testCodexFingerprintSeed), req.Header.Get("session_id"), "off 模式不得收敛 session_id")
	assert.Empty(t, req.Header.Get("x-codex-window-id"))
}

func (s *codexProviderIdentityRepoStub) GetByID(_ context.Context, _ int64) (*gatewayprovider.ExecutionProvider, error) {
	return s.provider, nil
}

func TestCodexProviderIdentitySourceResolvesShadowAndOverwritesFailoverContext(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	parentID := int64(11)
	parent := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: parentID, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{
		"chatgpt_account_id": "team-provider",
		"chatgpt_user_id":    "user-1",
	}}}
	shadow := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 111, ParentProviderID: &parentID, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	service := newResponsesFixture(responsesFixtureInputs{providers: &codexProviderIdentityRepoStub{provider: parent}})

	resolved, err := PrepareCodexIdentity(context.Background(), c, service.Requests.Providers, shadow)
	require.NoError(t, err)
	require.Same(t, parent.View(), resolved)
	require.Same(t, parent.View(), CodexIdentityRecord(c, shadow.View()))

	req, err := service.Requests.Build(
		context.Background(), c, shadow,
		[]byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`),
		"token", true, "client-session", true,
	)
	require.NoError(t, err)
	require.Equal(t, openai.IsolateOpenAIUpstreamSessionID(0, provideradapter.CodexIdentityNamespace(parent.View()), "client-session"), req.Header.Get("session_id"))

	next := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 19, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{
		"chatgpt_account_id": "other-provider",
		"chatgpt_user_id":    "user-2",
	}}}
	resolved, err = PrepareCodexIdentity(context.Background(), c, service.Requests.Providers, next)
	require.NoError(t, err)
	require.Same(t, next.View(), resolved)
	require.Same(t, next.View(), CodexIdentityRecord(c, shadow.View()))
}

func TestBuildUpstreamRequestNamespacesCodexIdentityByOAuthProvider(t *testing.T) {
	svc := newResponsesFixture(responsesFixtureInputs{})
	body := []byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`)

	build := func(providerID int64, chatgptAccountID string) http.Header {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		c.Set("api_key", &apikey.APIKey{ID: 77})
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.0")
		c.Request.Header.Set("x-codex-installation-id", "client-installation")
		c.Request.Header.Set("x-codex-window-id", "client-window")
		c.Request.Header.Set("session-id", "client-session")
		c.Request.Header.Set("thread-id", "client-thread")
		c.Request.Header.Set("x-client-request-id", "client-request")
		c.Request.Header.Set("x-codex-turn-metadata", `{"installation_id":"client-installation","session_id":"client-session","thread_id":"client-thread","turn_id":"client-turn","window_id":"client-window"}`)

		provider := &gatewayprovider.ExecutionProvider{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: providerID,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Credentials: map[string]any{
					"chatgpt_account_id": chatgptAccountID,
				},
			},
		}
		req, err := svc.Requests.Build(
			context.Background(), c, provider, body, "oauth-token", true, "client-session", true,
		)
		require.NoError(t, err)
		return req.Header
	}

	first := build(11, "chatgpt-provider-11")
	firstAgain := build(11, "chatgpt-provider-11")
	second := build(19, "chatgpt-provider-19")

	identityHeaders := []string{
		"x-codex-installation-id",
		"x-codex-window-id",
		"session-id",
		"session_id",
		"conversation_id",
		"thread-id",
		"x-client-request-id",
		"x-codex-turn-metadata",
	}
	checked := 0
	for _, header := range identityHeaders {
		if first.Get(header) == "" && second.Get(header) == "" {
			continue
		}
		checked++
		require.NotEmpty(t, first.Get(header), header)
		require.Equal(t, first.Get(header), firstAgain.Get(header), "same provider must retain stable identity: %s", header)
		require.NotEqual(t, first.Get(header), second.Get(header), "provider failover must rotate upstream identity: %s", header)
	}
	require.GreaterOrEqual(t, checked, 5, "test must exercise the real outbound identity surface")
}

func TestOpenAIOAuthCompactHTTPBuildersUsePreservedServiceTierInRoutingHint(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"type":"message","role":"user","content":"hello"}],
		"service_tier":"priority",
		"stream":true
	}`)
	normalized, changed, err := openai.NormalizeOpenAICompactRequestBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "priority", gjson.GetBytes(normalized, "service_tier").String())

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"chatgpt_account_id": "test-provider",
			},
		},
	}
	svc := newResponsesFixture(responsesFixtureInputs{})

	tests := []struct {
		name  string
		build func(*gin.Context) (*http.Request, error)
	}{
		{
			name: "ordinary",
			build: func(c *gin.Context) (*http.Request, error) {
				return svc.Requests.Build(
					context.Background(), c, provider, normalized, "test-token",
					false, "", true,
				)
			},
		},
		{
			name: "passthrough",
			build: func(c *gin.Context) (*http.Request, error) {
				return svc.Requests.BuildPassthrough(
					context.Background(), c, provider, normalized, "test-token",
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(
				http.MethodPost,
				"/v1/responses/compact",
				bytes.NewReader(normalized),
			)

			req, buildErr := tt.build(c)
			require.NoError(t, buildErr)
			require.Equal(
				t,
				"model=gpt-5.6-sol;tier=priority",
				req.Header.Get("x-codex-routing-hint"),
			)
			require.Equal(t, "priority", gjson.GetBytes(normalized, "service_tier").String())
		})
	}
}

func TestOpenAIInvalidBaseURLWhenAllowlistDisabled(t *testing.T) {
	cfg := &responsesFixtureOptions{Request: OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{Enabled: false}}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"base_url": "://invalid-url"},
		},
	}

	_, err := svc.Requests.Build(c.Request.Context(), c, provider, []byte("{}"), "token", false, "", false)
	if err == nil {
		t.Fatalf("expected error for invalid base_url when allowlist disabled")
	}
}

func TestOpenAIBuildUpstreamRequestOpenAIPassthroughPreservesCompactPath(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader([]byte(`{"model":"gpt-5"}`)))

	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Type: capability.ProviderTypeOAuth}}

	req, err := svc.Requests.BuildPassthrough(c.Request.Context(), c, provider, []byte(`{"model":"gpt-5"}`), "token")
	require.NoError(t, err)
	require.Equal(t, ChatgptCodexURL+"/compact", req.URL.String())
	require.Equal(t, "application/json", req.Header.Get("Accept"))
	require.Equal(t, openai.CodexCLIVersion, req.Header.Get("Version"))
	require.Empty(t, req.Header.Get("OpenAI-Beta"), "Codex OAuth HTTP must not synthesize the legacy responses beta header")
	require.NotEmpty(t, req.Header.Get("Session_Id"))
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(req.Context()))
}

func TestOpenAIBuildUpstreamRequestOpenAIPassthroughPreservesExplicitAPIKeyBetaHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5"}`)))
	c.Request.Header.Set("OpenAI-Beta", "api-key-specific-beta")

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{Request: OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{Enabled: false}}}})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}

	req, err := svc.Requests.BuildPassthrough(c.Request.Context(), c, provider, []byte(`{"model":"gpt-5"}`), "token")
	require.NoError(t, err)
	require.Equal(t, "api-key-specific-beta", req.Header.Get("OpenAI-Beta"), "OAuth-only backport must not alter API-key passthrough headers")
}

func TestOpenAIBuildUpstreamRequestOpenAIPassthroughDoesNotPropagateInternalRequestID(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5"}`)))
	c.Request.Header.Set("X-TokenRouter-Request-ID", "internal-request-123")

	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	req, err := svc.Requests.BuildPassthrough(c.Request.Context(), c, provider, []byte(`{"model":"gpt-5"}`), "token")
	require.NoError(t, err)
	require.Empty(t, req.Header.Get("X-TokenRouter-Request-ID"))
}

func TestOpenAIBuildUpstreamRequestCompactForcesJSONAcceptForOAuth(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader([]byte(`{"model":"gpt-5"}`)))

	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{"chatgpt_account_id": "chatgpt-acc"},
		},
	}

	req, err := svc.Requests.Build(c.Request.Context(), c, provider, []byte(`{"model":"gpt-5"}`), "token", false, "", true)
	require.NoError(t, err)
	require.Equal(t, ChatgptCodexURL+"/compact", req.URL.String())
	require.Equal(t, "application/json", req.Header.Get("Accept"))
	require.Equal(t, openai.CodexCLIVersion, req.Header.Get("Version"))
	require.Empty(t, req.Header.Get("OpenAI-Beta"), "Codex OAuth HTTP must not synthesize the legacy responses beta header")
	require.NotEmpty(t, req.Header.Get("Session_Id"))
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(req.Context()))
}

func TestOpenAIBuildUpstreamRequestOAuthMessagesBridgeUsesSessionOnly(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","prompt_cache_key":"anthropic-metadata-session-1","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"<tokenrouter-claude-code-todo-guard>"}]},{"type":"message","role":"user","content":"hello"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("OpenAI-Beta", "responses=experimental")
	c.Request.Header.Set("originator", "codex_cli_rs")

	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{"chatgpt_account_id": "chatgpt-acc"},
		},
	}

	req, err := svc.Requests.Build(c.Request.Context(), c, provider, body, "token", true, "anthropic-metadata-session-1", false)
	require.NoError(t, err)
	require.NotEmpty(t, req.Header.Get("Session_Id"))
	require.Empty(t, req.Header.Get("Conversation_Id"))
	require.Empty(t, req.Header.Get("OpenAI-Beta"))
	require.Empty(t, req.Header.Get("originator"))
}

func TestOpenAIBuildUpstreamRequestPreservesCompactPathForAPIKeyBaseURL(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/responses/compact", bytes.NewReader([]byte(`{"model":"gpt-5"}`)))

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{Request: OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{Enabled: false}}}})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Type: capability.ProviderTypeAPIKey,
			Platform:    capability.PlatformOpenAI,
			Credentials: map[string]any{"base_url": "https://example.com/v1"},
		},
	}

	req, err := svc.Requests.Build(c.Request.Context(), c, provider, []byte(`{"model":"gpt-5"}`), "token", false, "", false)
	require.NoError(t, err)
	require.Equal(t, "https://example.com/v1/responses/compact", req.URL.String())
}

func TestOpenAIBuildUpstreamRequestOAuthOfficialClientOriginatorCompatibility(t *testing.T) {
	// The upstream requires originator to match the leading token of the final User-Agent (issue #3901).
	// Derive originator from the final UA; an unrecognized identity uses the default Codex TUI identity.
	tests := []struct {
		name           string
		userAgent      string
		originator     string
		wantOriginator string
		wantUA         string
	}{
		{name: "official ua pairs originator", userAgent: "Codex Desktop/1.2.3", wantOriginator: "Codex Desktop", wantUA: "Codex Desktop/1.2.3"},
		{
			name:           "mismatched originator repaired from ua",
			userAgent:      "codex-tui/0.140.2 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.140.2)",
			originator:     "codex_cli_rs",
			wantOriginator: "codex-tui",
			wantUA:         "codex-tui/0.140.2 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.140.2)",
		},
		{name: "official originator without ua falls back to default identity", originator: "codex_vscode", wantOriginator: openai.CodexDefaultOriginator, wantUA: openai.CodexCLIUserAgent},
		{name: "third-party ua masked to default identity", userAgent: "luna/1.2.0", wantOriginator: openai.CodexDefaultOriginator, wantUA: openai.CodexCLIUserAgent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5"}`)))
			if tt.userAgent != "" {
				c.Request.Header.Set("User-Agent", tt.userAgent)
			}
			if tt.originator != "" {
				c.Request.Header.Set("originator", tt.originator)
			}

			svc := newResponsesFixture(responsesFixtureInputs{})
			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, Type: capability.ProviderTypeOAuth,
					Credentials: map[string]any{"chatgpt_account_id": "chatgpt-acc"},
				},
			}

			isCodexCLI := openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator"))
			req, err := svc.Requests.Build(c.Request.Context(), c, provider, []byte(`{"model":"gpt-5"}`), "token", false, "", isCodexCLI)
			require.NoError(t, err)
			require.Equal(t, tt.wantOriginator, req.Header.Get("originator"))
			require.Equal(t, tt.wantUA, req.Header.Get("User-Agent"))
		})
	}
}

func TestOpenAIBuildUpstreamRequestUsesTLSRouterUpstreamHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5"}`)))
	c.Request.Header.Set("User-Agent", "opencode/1.0")
	c.Request.Header.Set("originator", "opencode")

	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"chatgpt_account_id": "chatgpt-acc",
				"user_agent":         "codex-tui/9.8.0 provider-fallback",
			},
		},
	}
	routerMatch := egress.TLSFingerprintRouterMatchResult{
		Matched:                 true,
		RouterID:                9,
		RuleName:                "Codex TUI",
		TLSFingerprintProfileID: 7,
		UpstreamUserAgent:       "codex-tui/9.9.0 (Mac OS X 15.5; arm64) iTerm (codex-tui; 9.9.0)",
		UpstreamOriginator:      "codex-tui",
	}

	req, err := svc.Requests.Build(c.Request.Context(), c, provider, []byte(`{"model":"gpt-5"}`), "token", false, "", false, routerMatch)
	require.NoError(t, err)
	require.Equal(t, routerMatch.UpstreamUserAgent, req.Header.Get("User-Agent"))
	require.Equal(t, routerMatch.UpstreamOriginator, req.Header.Get("originator"))
}

func TestOpenAIBuildUpstreamRequestRouterEmptyUAUsesProviderFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5"}`)))
	c.Request.Header.Set("User-Agent", "opencode/1.0")

	svc := newResponsesFixture(responsesFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"chatgpt_account_id": "chatgpt-acc",
				"user_agent":         "codex-tui/9.8.0 provider-fallback",
			},
		},
	}

	req, err := svc.Requests.Build(c.Request.Context(), c, provider, []byte(`{"model":"gpt-5"}`), "token", false, "", false, egress.TLSFingerprintRouterMatchResult{Matched: true})
	require.NoError(t, err)
	require.Equal(t, "codex-tui/9.8.0 provider-fallback", req.Header.Get("User-Agent"))
	require.Equal(t, "codex-tui", req.Header.Get("originator"))
}

func TestOpenAIBuildUpstreamRequestOpenAIPassthroughForwardsResponsesLiteHeader(t *testing.T) {
	c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.98.0")
	c.Request.Header.Set(media.ResponsesLiteHeader, "true")

	svc := newOpenAIImageGenerationControlTestService(&auxiliaryHTTPRecorder{})
	req, err := svc.Requests.BuildPassthrough(
		c.Request.Context(),
		c,
		newOpenAIImageGenerationControlTestProvider(),
		[]byte(`{"model":"gpt-5.4","input":"write code"}`),
		"test-token",
	)

	require.NoError(t, err)
	require.Equal(t, "true", req.Header.Get(media.ResponsesLiteHeader))
}

func TestOpenCodeSessionForwardedByResponsesBuildersAfterProviderOverride(t *testing.T) {
	svc := openCodeSessionTestService()
	provider := openCodeSessionTestProvider("https://opencode.ai/zen/v1")
	body := []byte(`{"model":"gpt-5","input":"hello"}`)

	tests := []struct {
		name  string
		build func(*gin.Context) (*http.Request, error)
	}{
		{
			name: "normal responses",
			build: func(c *gin.Context) (*http.Request, error) {
				return svc.Requests.Build(context.Background(), c, provider, body, "token", false, "", false)
			},
		},
		{
			name: "passthrough responses",
			build: func(c *gin.Context) (*http.Request, error) {
				return svc.Requests.BuildPassthrough(context.Background(), c, provider, body, "token")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newOpenCodeSessionTestContext(t, "conversation-456")
			req, err := tt.build(c)
			require.NoError(t, err)
			requireSingleOpenCodeSessionHeader(t, req.Header, "conversation-456")
		})
	}
}

func TestOpenCodeSessionMissingCallerValueKeepsExistingOverrideBehavior(t *testing.T) {
	svc := openCodeSessionTestService()
	provider := openCodeSessionTestProvider("https://opencode.ai/zen/v1")
	c := newOpenCodeSessionTestContext(t, "")

	req, err := svc.Requests.Build(
		context.Background(), c, provider,
		[]byte(`{"model":"gpt-5","input":"hello"}`), "token", false, "", false,
	)
	require.NoError(t, err)
	require.Equal(t, "fixed-provider-value", anthropic.GetHeaderRaw(req.Header, "x-opencode-session"))
}

func TestOpenCodeSessionIsNotForwardedToOtherUpstreams(t *testing.T) {
	svc := openCodeSessionTestService()
	body := []byte(`{"model":"gpt-5","input":"hello"}`)

	for _, baseURL := range []string{
		"https://api.openai.com/v1",
		"https://opencode.ai.evil.example/v1",
		"https://api.opencode.ai/v1",
	} {
		t.Run(baseURL, func(t *testing.T) {
			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformOpenAI,
					Type:        capability.ProviderTypeAPIKey,
					Credentials: map[string]any{"base_url": baseURL},
				},
			}
			c := newOpenCodeSessionTestContext(t, "private-conversation")
			req, err := svc.Requests.Build(context.Background(), c, provider, body, "token", false, "", false)
			require.NoError(t, err)
			require.Empty(t, req.Header.Get("X-OpenCode-Session"))
		})
	}
}
