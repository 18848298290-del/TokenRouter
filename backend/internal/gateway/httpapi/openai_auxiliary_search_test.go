package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type alphaResponseBody struct {
	io.ReadCloser
	closes int
}

type alphaSearchProviderStateRepo struct {
	gatewayprovider.ExecutionProviderStore

	setErrorCalls      int
	lastError          string
	updatedCredentials map[string]any
}

func (b *alphaResponseBody) Close() error { b.closes++; return b.ReadCloser.Close() }

// TestAlphaSearchFailoverClosesOriginalResponse checks that retryable responses are closed before returning
// for failover.
func TestAlphaSearchFailoverClosesOriginalResponse(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	closedBody := &alphaResponseBody{ReadCloser: io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`))}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       closedBody,
	}}
	service := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 8,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
		},
	}

	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body)

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Empty(t, failoverErr.Stage)
	require.Empty(t, failoverErr.Scope)
	require.Empty(t, failoverErr.Reason)
	require.Zero(t, failoverErr.ClientStatusCode)
	require.Empty(t, failoverErr.ClientMessage)
	require.Nil(t, failoverErr.ResponseHeaders)
	require.Equal(t, openAIPlatformAlphaSearchURL, upstream.lastReq.URL.String())
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())

	require.Equal(t, 1, closedBody.closes, "上游响应体必须在本次返回前释放")
}

func (r *alphaSearchProviderStateRepo) SetError(_ context.Context, _ int64, errorMsg string) error {
	r.setErrorCalls++
	r.lastError = errorMsg
	return nil
}

func (r *alphaSearchProviderStateRepo) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.updatedCredentials = querycache.ShallowMap(credentials)
	return nil
}

func alphaSearchResponsesSSE(output string) string {
	return "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":` + strconv.Quote(output) + `}` + "\n\n" +
		"event: response.output_text.annotation.added\n" +
		`data: {"type":"response.output_text.annotation.added","annotation":{"type":"url_citation","url":"https://example.com/news","title":"Example News"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":` + strconv.Quote(output) + `}]}]}}` + "\n\n"
}

// TestForwardAlphaSearchOAuthPreservesWire checks alpha payload passthrough with User-Agent and TLS routing.
func TestForwardAlphaSearchOAuthPreservesWire(t *testing.T) {
	body := []byte(`{
		"id":"search-session",
		"model":"gpt-5.6-sol",
		"reasoning":{"effort":"max","context":"all_turns"},
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"latest news"}]}],
		"commands":{"search_query":[{"q":"OpenAI news","recency":1}]},
		"settings":{"allowed_callers":["direct"],"external_web_access":true},
		"max_output_tokens":2000,
		"future_field":{"keep":true}
	}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search?feature=standalone", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", openai.CodexCLIUserAgent)
	c.Request.Header.Set("Originator", "codex_cli_rs")
	c.Request.Header.Set("Version", "0.144.1")
	c.Request.Header.Set("X-Codex-Turn-Metadata", `{"session_id":"search-session","turn_id":"search-turn"}`)

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"encrypted_output":"ciphertext","output":"search result"}`)),
	}}
	service := newAuxiliaryFixture(auxiliaryFixtureInputs{
		transport: upstream,
		profiles:  &egressadapter.TLSProfiles{},
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 42,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-provider",
			},
			Extra: map[string]any{"enable_tls_fingerprint": true},
		},
	}

	const routedUA = "codex-tui/9.9.9 (Mac OS X 14.0; arm64) iTerm (codex-tui; 9.9.9)"
	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body, egress.TLSFingerprintRouterMatchResult{
		Matched:                 true,
		UpstreamUserAgent:       routedUA,
		UpstreamOriginator:      "codex-tui",
		TLSFingerprintProfileID: 0,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.WebSearchCalls)
	require.Equal(t, "gpt-5.6-sol", result.Model)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"encrypted_output":"ciphertext","output":"search result"}`, recorder.Body.String())
	require.Equal(t, chatgptCodexAlphaSearchURL+"?feature=standalone", upstream.lastReq.URL.String())
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
	require.Equal(t, "Bearer oauth-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "chatgpt-provider", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "0.144.1", upstream.lastReq.Header.Get("Version"))
	require.Equal(t, routedUA, upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "codex-tui", upstream.lastReq.Header.Get("Originator"))
	require.Empty(t, upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.NotNil(t, upstream.lastTLSProfile)
	require.Equal(t, "Built-in Default (Node.js 24.x)", upstream.lastTLSProfile.Name)
	require.Equal(t, openai.ScopeCodexProviderIdentityValue(provideradapter.CodexIdentityNamespace(provider.View()), 0, "session", "search-session"), gjson.Get(upstream.lastReq.Header.Get("X-Codex-Turn-Metadata"), "session_id").String())
	require.Equal(t, openai.ScopeCodexProviderIdentityValue(provideradapter.CodexIdentityNamespace(provider.View()), 0, "turn", "search-turn"), gjson.Get(upstream.lastReq.Header.Get("X-Codex-Turn-Metadata"), "turn_id").String())
	require.JSONEq(t, string(body), string(upstream.lastBody))
}

func TestForwardAlphaSearchPATUsesResponsesWebSearchFallback(t *testing.T) {
	body := []byte(`{
		"id":"search-session",
		"model":"gpt-5.6-sol",
		"commands":{"search_query":[{"q":"OpenAI news"}]},
		"prompt_cache_key":"responses-cache-key",
		"prompt_cache_retention":"24h"
	}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", openai.CodexCLIUserAgent)
	c.Request.Header.Set("Originator", "codex_cli_rs")
	c.Request.Header.Set("Version", "0.144.1")
	c.Request.Header.Set("OpenAI-Beta", "responses=experimental")
	c.Request.Header.Set("Accept-Language", "zh-CN")
	c.Request.Header.Set("Authorization", "Bearer client-token")
	c.Request.Header.Set("Session_ID", "session-client")
	c.Request.Header.Set("Conversation_ID", "conversation-client")
	c.Request.Header.Set("X-Codex-Beta-Features", "feature-a")
	c.Request.Header.Set("X-Codex-Turn-State", "turn-state")
	c.Request.Header.Set(media.ResponsesLiteHeaderKey, "true")
	c.Request.Header.Set("X-Codex-Turn-Metadata", `{"turn_id":"turn-1"}`)

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"req-search"}},
		Body:       io.NopCloser(strings.NewReader(alphaSearchResponsesSSE("search result"))),
	}}
	service := newAuxiliaryFixture(auxiliaryFixtureInputs{
		transport: upstream,
		profiles:  &egressadapter.TLSProfiles{},
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 43,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":               "at-test-token",
				"auth_mode":                  providercore.OpenAIAuthModePersonalAccessToken,
				"chatgpt_account_id":         "chatgpt-provider",
				"chatgpt_account_is_fedramp": true,
			},
			Extra: map[string]any{"enable_tls_fingerprint": true},
		},
	}

	const routedUA = "codex-tui/9.9.9 (Mac OS X 14.0; arm64) iTerm (codex-tui; 9.9.9)"
	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body, egress.TLSFingerprintRouterMatchResult{
		Matched:                 true,
		UpstreamUserAgent:       routedUA,
		UpstreamOriginator:      "codex-tui",
		TLSFingerprintProfileID: 0,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.WebSearchCalls)
	require.Equal(t, "/v1/responses", result.UpstreamEndpoint)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"output":"search result","results":[{"type":"text_result","ref_id":"turn0search0","url":"https://example.com/news","title":"Example News"}]}`, recorder.Body.String())
	require.Equal(t, ChatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "Bearer at-test-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "chatgpt-provider", upstream.lastReq.Header.Get("ChatGPT-Account-ID"))
	require.Equal(t, "true", upstream.lastReq.Header.Get("X-OpenAI-Fedramp"))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "responses=experimental", upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.Equal(t, "0.144.1", upstream.lastReq.Header.Get("Version"))
	require.Equal(t, openai.ScopeCodexProviderIdentityValue(provideradapter.CodexIdentityNamespace(provider.View()), 0, "turn", "turn-1"), gjson.Get(upstream.lastReq.Header.Get("X-Codex-Turn-Metadata"), "turn_id").String())
	require.Equal(t, routedUA, upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "codex-tui", upstream.lastReq.Header.Get("Originator"))
	require.NotNil(t, upstream.lastTLSProfile)
	require.Equal(t, "Built-in Default (Node.js 24.x)", upstream.lastTLSProfile.Name)
	require.Equal(t, openai.ScopeCodexProviderIdentityValue(provideradapter.CodexIdentityNamespace(provider.View()), 0, "turn", "turn-1"), gjson.Get(upstream.lastReq.Header.Get("X-Codex-Turn-Metadata"), "turn_id").String())
	require.Empty(t, upstream.lastReq.Header.Get("X-Codex-Beta-Features"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Codex-Turn-State"))
	require.Empty(t, upstream.lastReq.Header.Get(media.ResponsesLiteHeaderKey))
	require.Empty(t, upstream.lastReq.Header.Get("Accept-Language"))
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_retention").Exists())
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.Equal(t, "web_search", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String(), `"search_query"`)
}

func TestForwardAlphaSearchPATBackfillsMissingChatGPTAccountMetadata(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"OpenAI news"}]}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	var whoamiCalls int32
	whoamiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&whoamiCalls, 1)
		require.Equal(t, "Bearer at-test-token", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"email":"pat@example.com",
			"chatgpt_user_id":"user-123",
			"chatgpt_account_id":"acct-123",
			"chatgpt_plan_type":"plus",
			"chatgpt_account_is_fedramp":true
		}`))
	}))
	defer whoamiServer.Close()

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"output":"search result"}`)),
	}}
	oauthService := providercore.NewOpenAIAuthorization(providercore.NewOpenAISessionStore(), provideradapter.OpenAIAuthorizationOptions(&provideradapter.OpenAIAuthorizationDependencies{WhoamiURL: whoamiServer.URL}))
	t.Cleanup(func() { require.NoError(t, oauthService.StopContext(context.Background())) })
	oauthService.Start()
	repo := &alphaSearchProviderStateRepo{}
	service := newAuxiliaryFixture(auxiliaryFixtureInputs{
		transport:     upstream,
		credentials:   &providercore.OpenAIExecutionCredentials{OpenAI: (&providercore.OpenAITokenSource{Metrics: &providercore.OpenAITokenMetricsStore{}, Policy: providercore.OpenAIProviderRefreshPolicy(), Debug: slog.Debug, Warn: slog.Warn}).GetAccessToken},
		authorization: oauthService,
		store:         repo,
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 45,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token": "at-test-token",
				"auth_mode":    providercore.OpenAIAuthModePersonalAccessToken,
			},
		},
	}

	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, int32(1), atomic.LoadInt32(&whoamiCalls))
	require.Equal(t, "acct-123", upstream.lastReq.Header.Get("ChatGPT-Account-ID"))
	require.Equal(t, "true", upstream.lastReq.Header.Get("X-OpenAI-Fedramp"))
	require.Equal(t, "acct-123", provider.Record.Credentials["chatgpt_account_id"])
	require.Equal(t, "user-123", provider.Record.Credentials["chatgpt_user_id"])
	require.Equal(t, providercore.OpenAIAuthModePersonalAccessToken, provider.Record.Credentials["auth_mode"])
	require.Equal(t, "acct-123", repo.updatedCredentials["chatgpt_account_id"])
	require.Equal(t, providercore.OpenAIAuthModePersonalAccessToken, repo.updatedCredentials["auth_mode"])
}

// TestForwardAlphaSearchAPIKeyMapsModelAndPassesThroughError checks model mapping and non-retryable error
// passthrough.
func TestForwardAlphaSearchAPIKeyMapsModelAndPassesThroughError(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/alpha/search", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := `{"error":{"type":"invalid_request_error","message":"bad search"}}`
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	service := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 7,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "https://compat.example/v4",
				"model_mapping": map[string]any{
					"gpt-5.6-sol": "upstream-5.6",
				},
			},
		},
	}

	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body)

	require.NoError(t, err)
	// A passed-through error returns no result, so per-search billing is skipped.
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.JSONEq(t, upstreamBody, recorder.Body.String())
	require.Equal(t, "https://compat.example/v4/alpha/search", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "upstream-5.6", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "commands.search_query").IsArray())
}

// TestForwardAlphaSearchReturnsFailoverBeforeWriting checks that retryable errors return before writing a
// response.
func TestForwardAlphaSearchReturnsFailoverBeforeWriting(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	service := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 8,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "sk-test",
			},
		},
	}

	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body)

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Empty(t, failoverErr.Stage)
	require.Empty(t, failoverErr.Scope)
	require.Empty(t, failoverErr.Reason)
	require.Zero(t, failoverErr.ClientStatusCode)
	require.Empty(t, failoverErr.ClientMessage)
	require.Nil(t, failoverErr.ResponseHeaders)
	require.Equal(t, openAIPlatformAlphaSearchURL, upstream.lastReq.URL.String())
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
}

func TestForwardAlphaSearchSetupToken429CarriesSameProviderRetryWindow(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Retry-After":  []string{"1"},
			"X-Request-Id": []string{"req_alpha_oauth_429"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"rate limited"}}`)),
	}}
	service := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 81,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeSetupToken,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-provider",
			},
		},
	}
	startedAt := time.Now()

	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body)

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameProvider)
	require.Equal(t, time.Second, failoverErr.SameProviderRetryDelay)
	require.WithinDuration(t, startedAt.Add(providercore.RuntimeRetryWindow), failoverErr.SameProviderRetryDeadline, time.Second)
	require.Equal(t, "req_alpha_oauth_429", http.Header(failoverErr.ResponseHeaders).Get("x-request-id"))
	require.False(t, c.Writer.Written())
}

func TestForwardAlphaSearchAccessStateUsesTypedFailover(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req_alpha_access_state"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"code":"deactivated_workspace","message":"Workspace is deactivated"}}`)),
	}}
	service := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 11,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-provider",
			},
		},
	}

	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body)

	require.Nil(t, result)
	assertOpenAIAlphaSearchAccessStateFailover(t, err, "req_alpha_access_state")
	require.Equal(t, chatgptCodexAlphaSearchURL, upstream.lastReq.URL.String())
	require.False(t, c.Writer.Written())
}

func TestForwardAlphaSearchPATFallbackAccessStateUsesTypedFailover(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"req_alpha_pat_access_state"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"code":"account_disabled","message":"Provider is disabled"}}`)),
	}}
	service := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 12,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "at-test-token",
				"auth_mode":          providercore.OpenAIAuthModePersonalAccessToken,
				"chatgpt_account_id": "chatgpt-provider",
			},
		},
	}

	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body)

	require.Nil(t, result)
	assertOpenAIAlphaSearchAccessStateFailover(t, err, "req_alpha_pat_access_state")
	require.Equal(t, ChatgptCodexURL, upstream.lastReq.URL.String())
	require.False(t, c.Writer.Written())
}

func assertOpenAIAlphaSearchAccessStateFailover(t *testing.T, err error, requestID string) {
	t.Helper()
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusForbidden, failoverErr.StatusCode)
	require.Equal(t, forwardcore.GatewayFailureStageProviderAuth, failoverErr.Stage)
	require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
	require.Equal(t, forwardcore.OpenAIUpstreamAccessStateReason, failoverErr.Reason)
	require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
	require.Equal(t, http.StatusBadGateway, failoverErr.ClientStatusCode)
	require.Equal(t, "Upstream access is temporarily unavailable, please retry later", failoverErr.ClientMessage)
	require.False(t, failoverErr.RetryableOnSameProvider)
	require.Equal(t, requestID, http.Header(failoverErr.ResponseHeaders).Get("x-request-id"))
}

func TestForwardAlphaSearchUnauthorizedDoesNotMarkProviderError(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"detail":"Unauthorized"}`)),
	}}
	repo := &alphaSearchProviderStateRepo{}

	service := newAuxiliaryFixture(auxiliaryFixtureInputs{
		transport: upstream,
		store:     repo,
		observer:  gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo}),
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 44,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				// Omit auth_mode to cover PATs imported as ordinary OAuth credentials.
				"access_token":       "at-test-token",
				"chatgpt_account_id": "chatgpt-provider",
			},
		},
	}

	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body)

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusUnauthorized, failoverErr.StatusCode)
	require.Zero(t, repo.setErrorCalls)
	require.Empty(t, repo.lastError)
	require.False(t, c.Writer.Written())
}

func TestForwardAlphaSearchPATResponsesFallbackUnauthorizedDoesNotMarkProviderError(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"detail":"Unauthorized"}`)),
	}}
	repo := &alphaSearchProviderStateRepo{}

	service := newAuxiliaryFixture(auxiliaryFixtureInputs{
		transport: upstream,
		store:     repo,
		observer:  gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo}),
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 46,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "at-test-token",
				"auth_mode":          providercore.OpenAIAuthModePersonalAccessToken,
				"chatgpt_account_id": "chatgpt-provider",
			},
		},
	}

	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body)

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusUnauthorized, failoverErr.StatusCode)
	require.Equal(t, ChatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "responses=experimental", upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.Zero(t, repo.setErrorCalls)
	require.Empty(t, repo.lastError)
	require.False(t, c.Writer.Written())
}

// TestForwardAlphaSearchAPIKeyEndpointNotFoundFailsOver checks failover when an API-key search endpoint
// returns 404.
// The provider remains schedulable so a mixed group can try another candidate.
func TestForwardAlphaSearchAPIKeyEndpointNotFoundFailsOver(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Not Found"}}`)),
	}}
	repo := &alphaSearchProviderStateRepo{}

	service := newAuxiliaryFixture(auxiliaryFixtureInputs{
		transport: upstream,
		store:     repo,
		observer:  gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo}),
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 9,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "https://relay.example",
			},
		},
	}

	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body)

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusNotFound, failoverErr.StatusCode)
	require.Zero(t, repo.setErrorCalls)
	require.Empty(t, repo.lastError)
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
}

// TestForwardAlphaSearchOAuthNotFoundPassesThrough checks passthrough of a ChatGPT search endpoint 404.
func TestForwardAlphaSearchOAuthNotFoundPassesThrough(t *testing.T) {
	body := []byte(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", bytes.NewReader(body))

	upstreamBody := `{"detail":"Not Found"}`
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	service := newAuxiliaryFixture(auxiliaryFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 10,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-provider",
			},
		},
	}

	result, err := service.ForwardAlphaSearch(context.Background(), c, provider, body)

	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.JSONEq(t, upstreamBody, recorder.Body.String())
}

func TestShouldApplyOpenAIAlphaSearchProviderErrorSideEffects(t *testing.T) {
	require.False(t, media.AlphaProviderErrorSideEffects(http.StatusUnauthorized))
	require.False(t, media.AlphaProviderErrorSideEffects(http.StatusNotFound))
	require.False(t, media.AlphaProviderErrorSideEffects(http.StatusMethodNotAllowed))
	require.True(t, media.AlphaProviderErrorSideEffects(http.StatusForbidden))
	require.True(t, media.AlphaProviderErrorSideEffects(http.StatusTooManyRequests))
}

func TestOpenAIAlphaSearchSchedulingModelUsesCanonicalProviderMapping(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Credentials: map[string]any{
		"model_mapping": map[string]any{"client-visible": "canonical-upstream"},
	}}}
	require.Equal(t, "canonical-upstream", openAIAlphaSearchSchedulingModel(provider, "client-visible"))
	require.Equal(t, "unmapped", openAIAlphaSearchSchedulingModel(provider, "unmapped"))
}

func TestSanitizeOpenAIAlphaSearchBody_RemovesResponsesOnlyFields(t *testing.T) {
	body := []byte(`{"id":"search-session","store":false,"prompt_cache_key":"cache","commands":{"search_query":[{"q":"news"}]}}`)

	normalized, err := openai.SanitizeOpenAIAlphaSearchBody(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(normalized, "store").Exists())
	require.False(t, gjson.GetBytes(normalized, "prompt_cache_key").Exists())
	require.Equal(t, "news", gjson.GetBytes(normalized, "commands.search_query.0.q").String())
}

func TestIsOpenAIAlphaSearchEndpointUnsupported(t *testing.T) {
	apiKey := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	oauth := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}

	require.True(t, isOpenAIAlphaSearchEndpointUnsupported(apiKey, http.StatusNotFound))
	require.True(t, isOpenAIAlphaSearchEndpointUnsupported(apiKey, http.StatusMethodNotAllowed))
	require.False(t, isOpenAIAlphaSearchEndpointUnsupported(apiKey, http.StatusBadRequest))
	require.False(t, isOpenAIAlphaSearchEndpointUnsupported(oauth, http.StatusNotFound))
	require.False(t, isOpenAIAlphaSearchEndpointUnsupported(nil, http.StatusNotFound))
}
