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

// Standalone and hosted search use different outgoing protocols but share
// search billing and recovery rules. These fixtures inspect request identity,
// PAT metadata backfill, conversion output, provider-error writes, and the
// original response-body closure when captured failure bytes are replayed for
// health handling.

// alphaResponseBody counts closure attempts on the original upstream search
// body.
type alphaResponseBody struct {
	io.ReadCloser     // original wrapped response reader
	closes        int // observed body closure attempts
}

// alphaSearchProviderStateRepo records search health writes and persisted PAT
// metadata.
type alphaSearchProviderStateRepo struct {
	gatewayprovider.ExecutionProviderStore // unused embedded repository methods

	setErrorCalls int    // persistent provider-error write count
	lastError     string // last provider-error message written
	// updatedCredentials retains the top-level copied credential update
	// from the last fixture write. Nested values remain shared with the
	// supplied map.
	updatedCredentials map[string]any
}

// Close counts the fixture cleanup and returns the wrapped body closure error.
func (b *alphaResponseBody) Close() error { b.closes++; return b.ReadCloser.Close() }

// TestAlphaSearchFailoverClosesOriginalResponse returns a rate-limit response
// whose body tracks closure. It checks typed retry evidence, unwritten client
// output, and one closure of the originally acquired body before return.
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

// SetError records the attempted provider error text and increments its
// counter.
func (r *alphaSearchProviderStateRepo) SetError(_ context.Context, _ int64, errorMsg string) error {
	r.setErrorCalls++
	r.lastError = errorMsg
	return nil
}

// UpdateCredentials retains a shallow top-level copy of credential updates for
// assertions. Nested credential values remain shared with the supplied map. The
// fixture returns nil for every write and records no independent durable state.
func (r *alphaSearchProviderStateRepo) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.updatedCredentials = querycache.ShallowMap(credentials)
	return nil
}

// alphaSearchResponsesSSE builds text, citation, and completion events for
// hosted-search conversion.
func alphaSearchResponsesSSE(output string) string {
	return "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":` + strconv.Quote(output) + `}` + "\n\n" +
		"event: response.output_text.annotation.added\n" +
		`data: {"type":"response.output_text.annotation.added","annotation":{"type":"url_citation","url":"https://example.com/news","title":"Example News"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":` + strconv.Quote(output) + `}]}]}}` + "\n\n"
}

// TestForwardAlphaSearchOAuthPreservesWire sends standalone OAuth search with a
// matched TLS identity rule. It checks unchanged sanitized-compatible payload,
// query forwarding, provider-scoped metadata, TLS profile, result identity, and
// one per-call search billing unit.
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

// TestForwardAlphaSearchPATUsesResponsesWebSearchFallback supplies PAT search
// input and Responses SSE output. It checks hosted tool conversion, filtered
// capabilities, provider identity, mapped search JSON/citations, endpoint
// metadata, and one successful search-call unit.
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

// TestForwardAlphaSearchPATBackfillsMissingChatGPTAccountMetadata starts a PAT
// without account identity and serves validation metadata locally. It checks
// one discovery call, outgoing account/FedRAMP headers, request credential
// updates, and persistence of the discovered PAT identity.
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

// TestForwardAlphaSearchAPIKeyMapsModelAndPassesThroughError returns a
// nonretryable 400 from a mapped compatible search target. It checks outgoing
// model and commands, upstream error passthrough, and a nil completion result
// despite nil returned error.
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
	// This handled HTTP failure supplies no completion result, leaving the
	// per-call billing adapter without a successful search unit despite a
	// nil execution error.
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.JSONEq(t, upstreamBody, recorder.Body.String())
	require.Equal(t, "https://compat.example/v4/alpha/search", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "upstream-5.6", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "commands.search_query").IsArray())
}

// TestForwardAlphaSearchReturnsFailoverBeforeWriting returns a retryable
// API-key 429. It checks status-based failover evidence, empty access-state
// metadata, default search target, and an unwritten response body available for
// outer provider recovery.
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

// TestForwardAlphaSearchSetupToken429CarriesSameProviderRetryWindow returns 429
// with Retry-After for a setup token. It checks same-provider retry
// eligibility, one-second delay, bounded deadline, response request ID, and
// uncommitted client output.
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

// TestForwardAlphaSearchAccessStateUsesTypedFailover returns a
// deactivated-workspace error from standalone OAuth search. It checks
// provider-auth classification and client-safe recovery metadata while
// retaining the Codex target and unwritten output.
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

// TestForwardAlphaSearchPATFallbackAccessStateUsesTypedFailover returns an
// account-disabled error on hosted PAT search. It checks provider-auth
// recovery, Responses target, request correlation, and absence of client output
// before the returned failover.
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

// assertOpenAIAlphaSearchAccessStateFailover checks typed provider-auth
// recovery and client-safe output metadata. It also checks the upstream request
// ID and disallows same-provider retry for these supplied search access-state
// fixtures.
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

// TestForwardAlphaSearchUnauthorizedDoesNotMarkProviderError supplies an
// at-prefixed token without an auth-mode marker and returns 401. It checks
// failover evidence, zero provider-error writes, and an unwritten client
// response after search execution.
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
				// Keep auth_mode absent so the at-prefixed
				// token itself selects PAT compatibility
				// handling, covering credential records that
				// lack an explicit authentication-mode marker.
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

// TestForwardAlphaSearchPATResponsesFallbackUnauthorizedDoesNotMarkProviderError
// returns 401 for explicit PAT hosted search. It checks Responses/SSE request
// construction, beta negotiation, retry evidence, zero provider-error writes,
// and uncommitted client output.
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

// TestForwardAlphaSearchAPIKeyEndpointNotFoundFailsOver returns 404 from a
// compatible API-key search endpoint. It checks failover evidence, zero
// persistent provider-error writes, and empty client output so another target
// can be selected.
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

// TestForwardAlphaSearchOAuthNotFoundPassesThrough returns 404 from standalone
// OAuth search. It checks the upstream JSON/status reaches the client and the
// call returns nil error with no completion result for search billing.
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

// TestShouldApplyOpenAIAlphaSearchProviderErrorSideEffects compares
// authentication, endpoint, forbidden, and rate-limit statuses. It checks
// 401/404/405 skip effects, while 403 and 429 permit the shared search
// provider-health path.
func TestShouldApplyOpenAIAlphaSearchProviderErrorSideEffects(t *testing.T) {
	require.False(t, media.AlphaProviderErrorSideEffects(http.StatusUnauthorized))
	require.False(t, media.AlphaProviderErrorSideEffects(http.StatusNotFound))
	require.False(t, media.AlphaProviderErrorSideEffects(http.StatusMethodNotAllowed))
	require.True(t, media.AlphaProviderErrorSideEffects(http.StatusForbidden))
	require.True(t, media.AlphaProviderErrorSideEffects(http.StatusTooManyRequests))
}

// TestOpenAIAlphaSearchSchedulingModelUsesCanonicalProviderMapping configures
// one client-to-provider alias. It checks health feedback uses the mapped
// canonical name for that alias and retains an unmapped model unchanged.
func TestOpenAIAlphaSearchSchedulingModelUsesCanonicalProviderMapping(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Credentials: map[string]any{
		"model_mapping": map[string]any{"client-visible": "canonical-upstream"},
	}}}
	require.Equal(t, "canonical-upstream", openAIAlphaSearchSchedulingModel(provider, "client-visible"))
	require.Equal(t, "unmapped", openAIAlphaSearchSchedulingModel(provider, "unmapped"))
}

// TestSanitizeOpenAIAlphaSearchBody_RemovesResponsesOnlyFields supplies storage
// and prompt-cache fields alongside a search command. It checks sanitization
// removes those Responses fields while retaining the query text in Alpha Search
// input.
func TestSanitizeOpenAIAlphaSearchBody_RemovesResponsesOnlyFields(t *testing.T) {
	body := []byte(`{"id":"search-session","store":false,"prompt_cache_key":"cache","commands":{"search_query":[{"q":"news"}]}}`)

	normalized, err := openai.SanitizeOpenAIAlphaSearchBody(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(normalized, "store").Exists())
	require.False(t, gjson.GetBytes(normalized, "prompt_cache_key").Exists())
	require.Equal(t, "news", gjson.GetBytes(normalized, "commands.search_query.0.q").String())
}

// TestIsOpenAIAlphaSearchEndpointUnsupported compares missing-endpoint statuses
// on API-key, OAuth, and absent providers. It checks 404/405 select API-key
// endpoint recovery, while 400, OAuth 404, and missing providers do not.
func TestIsOpenAIAlphaSearchEndpointUnsupported(t *testing.T) {
	apiKey := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	oauth := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}

	require.True(t, isOpenAIAlphaSearchEndpointUnsupported(apiKey, http.StatusNotFound))
	require.True(t, isOpenAIAlphaSearchEndpointUnsupported(apiKey, http.StatusMethodNotAllowed))
	require.False(t, isOpenAIAlphaSearchEndpointUnsupported(apiKey, http.StatusBadRequest))
	require.False(t, isOpenAIAlphaSearchEndpointUnsupported(oauth, http.StatusNotFound))
	require.False(t, isOpenAIAlphaSearchEndpointUnsupported(nil, http.StatusNotFound))
}
