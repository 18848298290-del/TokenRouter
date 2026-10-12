package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaysession "github.com/TokenFlux/TokenRouter/internal/gateway/session"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	groktestkit "github.com/TokenFlux/TokenRouter/internal/upstream/grok/testkit"
)

const (
	grokRateLimitRepeatCooldown = 10 * time.Minute
)

type grokProtocolSSEFrame struct {
	event string
	data  []byte
}

func TestIsGrokModelSpecificFreeUsage(t *testing.T) {
	require.True(t, providercore.IsGrokModelSpecificFreeUsage(
		"you've used all the included free usage for model grok-4.5", "grok-4.5"))
	require.True(t, providercore.IsGrokModelSpecificFreeUsage("模型额度用完 grok-4.3", "grok-4.3"))
	require.False(t, providercore.IsGrokModelSpecificFreeUsage("free usage exhausted", "grok-4.5"))
}

func TestGrokStickyAffinitySeed_ScopesByModel(t *testing.T) {
	a := gatewaysession.GrokStickyAffinitySeed("session-1", []byte(`{"model":"grok-4.5"}`))
	b := gatewaysession.GrokStickyAffinitySeed("session-1", []byte(`{"model":"grok-4.3"}`))
	c := gatewaysession.GrokStickyAffinitySeed("session-1", []byte(`{"model":"grok-4.5"}`))
	require.NotEqual(t, a, b)
	require.Equal(t, a, c)
	require.Contains(t, a, "grok-affinity:v1:")
}

func TestApplyGrokUpstreamFailure_ModelSpecificFreeUsage(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{providers: repo})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9109, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	body := []byte(`{"error":{"code":"subscription:free-usage-exhausted","message":"You've used all the included free usage for model grok-4.5. Usage resets over a rolling 24-hour window."}}`)

	svc.handleGrokProviderUpstreamError(context.Background(), provider, 400, nil, body)

	require.Zero(t, repo.tempUnschedCalls, "model-scoped free usage must not cool sibling models")
	require.True(t, providercore.IsGrokModelQuotaBlocked(provider.Record.ID, "grok-4.5", time.Now()))
	require.False(t, providercore.IsGrokModelQuotaBlocked(provider.Record.ID, "grok-4.3", time.Now()))
}

func TestApplyGrokUpstreamFailure_SpendingLimitRemainsRecoverable(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{providers: repo})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 9110, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	body := []byte(`{"code":"personal-team-blocked:spending-limit","error":"spending limit reached"}`)

	svc.handleGrokProviderUpstreamError(context.Background(), provider, 403, nil, body)

	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Zero(t, repo.tempUnschedCalls)
	// A missing billing-period snapshot uses a short probe cooldown that can recover.
	require.WithinDuration(t, time.Now().Add(10*time.Minute), repo.lastRateLimitResetAt, 2*time.Second)
}

func TestForwardGrokResponses_PropagatesSearchCountFromJSON(t *testing.T) {
	body := []byte(`{"model":"grok","input":"search something","tools":[{"type":"web_search"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	provider := gatewaytestkit.HealthyGrokOAuthProvider(9901, "access-token")
	repo := &grokFixtureProviders{providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}}
	upstreamBody := `{
		"id":"resp_search_bill",
		"object":"response",
		"model":"grok-4.5",
		"status":"completed",
		"output":[
			{"type":"web_search_call","id":"ws1","call_id":"c1","status":"completed"},
			{"type":"x_search_call","id":"xs1","call_id":"c2"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}
		],
		"usage":{"input_tokens":10,"output_tokens":5}
	}`
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(upstreamBody))),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok", false, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, result.SearchCount, "Grok Responses must surface search tool calls for surcharge billing")
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
}

func TestForwardGrokResponses_PropagatesSearchCountFromSSE(t *testing.T) {
	body := []byte(`{"model":"grok","input":"search","tools":[{"type":"web_search"}],"stream":true}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	provider := gatewaytestkit.HealthyGrokOAuthProvider(9902, "access-token")
	repo := &grokFixtureProviders{providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider}}
	// Count the same call_id once across item.done and response.completed.
	sse := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"web_search_call\",\"id\":\"ws1\",\"call_id\":\"c1\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_s\",\"status\":\"completed\",\"output\":[{\"type\":\"web_search_call\",\"id\":\"ws1\",\"call_id\":\"c1\"}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(sse))),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok", true, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.SearchCount, "stream SearchCount must be wired and deduped")
}

func TestForwardGrokResponsesCodexAdditionalToolsUsesMixedCacheIntent(t *testing.T) {
	body := []byte(`{
		"model":"grok-4.5",
		"stream":false,
		"prompt_cache_key":"codex-session",
		"input":[
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"function","name":"lookup","description":"look up a key","parameters":{"type":"object"}},
				{"type":"function","name":"web_search","description":"search","parameters":{"type":"object"}},
				{"type":"custom","name":"apply_patch"},
				{"type":"namespace","name":"collaboration"}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		]
	}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("X-TokenRouter-Grok-Client-Tool-Cache", "prefer-cache")
	c.Set("api_key", &apikey.APIKey{ID: 4501})

	provider := gatewaytestkit.HealthyGrokOAuthProvider(4501, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	repo := &grokQuotaProviderRepo{
		grokFixtureProviders: &grokFixtureProviders{
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_codex_lite","object":"response","model":"grok-4.5","status":"completed",
			"output":[],"usage":{"input_tokens":10,"output_tokens":1}
		}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok-4.5", false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_codex_lite", result.ResponseID)
	require.False(t, gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools")`).Exists())
	tools := gjson.GetBytes(upstream.lastBody, "tools").Array()
	require.Len(t, tools, 4)
	require.Equal(t, "function", tools[0].Get("type").String())
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "function", tools[2].Get("type").String())
	require.Equal(t, "apply_patch", tools[2].Get("name").String())
	require.Equal(t, "x_search", tools[3].Get("type").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "tool_choice").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="custom")`).Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="namespace")`).Exists())
	identity := gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String()
	require.NotEmpty(t, identity)
	require.Equal(t, identity, upstream.lastReq.Header.Get(GrokConversationIDHeader))
	require.Empty(t, upstream.lastReq.Header.Get("X-TokenRouter-Grok-Client-Tool-Cache"))
}

func TestForwardGrokResponsesClaudeDesktopClientToolsUseCacheRoute(t *testing.T) {
	firstBody := []byte(`{
		"model":"grok-4.5","stream":false,"instructions":"You are Claude Desktop.",
		"tools":[
			{"type":"function","name":"Read","parameters":{"type":"object"}},
			{"type":"function","name":"Edit","parameters":{"type":"object"}},
			{"type":"function","name":"WebSearch","parameters":{"type":"object"}},
			{"type":"function","name":"mcp__workspace__bash","parameters":{"type":"object"}}
		],
		"input":[{"role":"user","content":[{"type":"input_text","text":"first turn"}]}]
	}`)
	secondBody := []byte(`{
		"model":"grok-4.5","stream":false,"instructions":"You are Claude Desktop.",
		"tools":[
			{"type":"function","name":"Read","parameters":{"type":"object"}},
			{"type":"function","name":"Edit","parameters":{"type":"object"}},
			{"type":"function","name":"WebSearch","parameters":{"type":"object"}},
			{"type":"function","name":"mcp__workspace__bash","parameters":{"type":"object"}}
		],
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"first turn"}]},
			{"role":"assistant","content":[{"type":"output_text","text":"first answer"}]},
			{"role":"user","content":[{"type":"input_text","text":"second turn"}]}
		]
	}`)

	provider := gatewaytestkit.HealthyGrokOAuthProvider(4504, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	repo := &grokQuotaProviderRepo{
		grokFixtureProviders: &grokFixtureProviders{
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
		},
	}
	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"resp_claude_desktop_1","object":"response","model":"grok-4.5","status":"completed",
				"output":[],"usage":{"input_tokens":30000,"output_tokens":10,"input_tokens_details":{"cached_tokens":0}}
			}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"resp_claude_desktop_2","object":"response","model":"grok-4.5","status":"completed",
				"output":[],"usage":{"input_tokens":30100,"output_tokens":12,"input_tokens_details":{"cached_tokens":28672}}
			}`)),
		},
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	newContext := func(body []byte) *gin.Context {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		c.Request.Header.Set("User-Agent", "claude-cli/2.1.215 (external, claude-desktop-3p, agent-sdk/0.3.215)")
		c.Request.Header.Set("X-App", "cli")
		c.Request.Header.Set("anthropic-client-platform", "desktop_app")
		c.Request.Header.Set("X-Claude-Code-Session-Id", "claude-desktop-session")
		c.Set("api_key", &apikey.APIKey{ID: 4504})
		return c
	}

	first, err := svc.Grok.ForwardResponses(context.Background(), newContext(firstBody), provider, firstBody, "grok-4.5", false, time.Now())
	require.NoError(t, err)
	second, err := svc.Grok.ForwardResponses(context.Background(), newContext(secondBody), provider, secondBody, "grok-4.5", false, time.Now())
	require.NoError(t, err)

	require.Equal(t, 0, first.Usage.CacheReadInputTokens)
	require.Equal(t, 28672, second.Usage.CacheReadInputTokens)
	require.Len(t, upstream.bodies, 2)
	require.Len(t, upstream.requests, 2)
	for i := range upstream.bodies {
		tools := gjson.GetBytes(upstream.bodies[i], "tools").Array()
		require.Len(t, tools, 6)
		require.Equal(t, "Read", tools[0].Get("name").String())
		require.Equal(t, "Edit", tools[1].Get("name").String())
		require.Equal(t, "WebSearch", tools[2].Get("name").String())
		require.Equal(t, "mcp__workspace__bash", tools[3].Get("name").String())
		require.Equal(t, "web_search", tools[4].Get("type").String())
		require.Equal(t, "x_search", tools[5].Get("type").String())
		require.False(t, gjson.GetBytes(upstream.bodies[i], "tool_choice").Exists())
		require.Empty(t, upstream.requests[i].Header.Get("X-App"))
		require.Empty(t, upstream.requests[i].Header.Get("anthropic-client-platform"))
		require.Empty(t, upstream.requests[i].Header.Get("X-Claude-Code-Session-Id"))
	}
	firstIdentity := gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String()
	secondIdentity := gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String()
	require.NotEmpty(t, firstIdentity)
	require.Equal(t, firstIdentity, secondIdentity)
	require.Equal(t, firstIdentity, upstream.requests[0].Header.Get(GrokConversationIDHeader))
	require.Equal(t, secondIdentity, upstream.requests[1].Header.Get(GrokConversationIDHeader))
}

func TestForwardGrokResponsesCompactRoundTrip(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"compact this"}]}],"metadata":{"large_id":9007199254740993},"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 71031,
			Name:        "grok-compact-api-key",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 2,
			Credentials: map[string]any{
				"api_key":  "xai-test-key",
				"base_url": "https://api.x.ai/v1",
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_grok_compact",
			"object":"response",
			"status":"completed",
			"model":"grok-4.5",
			"output":[
				{"type":"reasoning","summary":[],"encrypted_content":"compact-state"},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"compact summary"}]}
			],
			"usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17}
		}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok-4.5", false, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream)
	require.Equal(t, "resp_grok_compact", result.ResponseID)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Equal(t, "grok-4.5", result.BillingModel)
	require.Equal(t, grok.DefaultResponsesModel, result.UpstreamModel)
	require.Equal(t, grok.DefaultResponsesModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.Equal(t, "reasoning.encrypted_content", gjson.GetBytes(upstream.lastBody, "include.0").String())
	require.Equal(t, "compact this", gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "input.1.content.0.text").String(), "Primary Request and Intent")
	// Remove OpenAI metadata before forwarding to Grok Responses.
	require.False(t, gjson.GetBytes(upstream.lastBody, "metadata").Exists())
	require.Equal(t, "compaction", gjson.Get(recorder.Body.String(), "output.0.type").String())
	require.Equal(t, "compact-state", gjson.Get(recorder.Body.String(), "output.0.encrypted_content").String())
	require.Equal(t, "compact summary", gjson.Get(recorder.Body.String(), "output.0.summary.0.text").String())
}

func TestForwardGrokResponsesStreamingDefaultsEmptyModelTo45AndSnapshots(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"input":"hi","stream":true,"reasoning_effort":"high"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("OpenAI-Beta", "responses=experimental")
	c.Set("api_key", &apikey.APIKey{ID: 5201})

	provider := gatewaytestkit.HealthyGrokOAuthProvider(52, "access-token")
	repo := &grokQuotaProviderRepo{
		grokFixtureProviders: &grokFixtureProviders{
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{52: provider},
		},
	}
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_grok","model":"grok-4.3","usage":{"input_tokens":5,"output_tokens":3,"input_tokens_details":{"cached_tokens":2}}}}`,
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"Xai-Request-Id":                 []string{"xai-stream-req"},
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"8"},
			"X-Ratelimit-Limit-Tokens":       []string{"1000"},
			"X-Ratelimit-Remaining-Tokens":   []string{"990"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "", true, time.Now())
	require.NoError(t, err)
	require.Equal(t, grok.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "responses=experimental", upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String(), upstream.lastReq.Header.Get(GrokConversationIDHeader))
	require.Equal(t, "web_search", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(upstream.lastBody, "tools.1.type").String())
	require.Equal(t, "none", gjson.GetBytes(upstream.lastBody, "tool_choice").String())
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.True(t, result.Stream)
	require.Equal(t, "resp_grok", result.ResponseID)
	require.Equal(t, "xai-stream-req", result.RequestID)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, 2, result.Usage.CacheReadInputTokens)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, recorder.Body.String(), "response.output_text.delta")
	require.NotNil(t, repo.updates[52]["grok_usage_snapshot"])
}

func TestForwardGrokResponsesAPIKeyUsesXAIResponses(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","input":"hi","metadata":{"session_id":"abc"},"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 53,
			Name:        "grok-api-key",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 2,
			Credentials: map[string]any{
				"api_key":  "xai-test-key",
				"base_url": "https://api.x.ai/v1",
			},
		},
	}
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_grok_api_key","model":"grok-4.5","usage":{"input_tokens":2,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok-4.5", true, time.Now())
	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer xai-test-key", upstream.lastReq.Header.Get("Authorization"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.NotEqual(t, grok.DefaultGrokUpstreamUserAgent(), upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "metadata").Exists())
	require.Equal(t, "resp_grok_api_key", result.ResponseID)
	require.Equal(t, 2, result.Usage.InputTokens)
	require.Equal(t, 1, result.Usage.OutputTokens)
}

func TestForwardGrokResponsesUsesMetadataSessionForCacheIdentityWithoutForwardingMetadata(t *testing.T) {
	firstBody := []byte(`{"model":"grok-4.5","input":"first turn","metadata":{"user_id":"{\"session_id\":\"metadata-session\"}"},"stream":false}`)
	secondBody := []byte(`{"model":"grok-4.5","input":"different second turn","metadata":{"user_id":"{\"session_id\":\"metadata-session\"}"},"stream":false}`)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5401,
			Name:        "grok-api-key",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 2,
			Credentials: map[string]any{
				"api_key":  "xai-test-key",
				"base_url": "https://api.x.ai/v1",
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_first","object":"response","model":"grok-4.6","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_second","object":"response","model":"grok-4.6","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":1}}`)),
		},
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	newContext := func(body []byte) *gin.Context {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		c.Set("api_key", &apikey.APIKey{ID: 5401})
		return c
	}

	_, err := svc.Grok.ForwardResponses(context.Background(), newContext(firstBody), provider, firstBody, "grok-4.5", false, time.Now())
	require.NoError(t, err)
	_, err = svc.Grok.ForwardResponses(context.Background(), newContext(secondBody), provider, secondBody, "grok-4.5", false, time.Now())
	require.NoError(t, err)
	require.Len(t, upstream.bodies, 2)

	firstIdentity := gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String()
	secondIdentity := gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String()
	require.NotEmpty(t, firstIdentity)
	require.Equal(t, firstIdentity, secondIdentity)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "metadata").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "metadata").Exists())
}

func TestForwardGrokResponsesRetriesInvalidEncryptedContentOnce(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{
		"model":"grok-4.5",
		"previous_response_id":"resp_valid_history",
		"input":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"keep this summary"}],"encrypted_content":"encrypted-reasoning"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
		],
		"metadata":{"large_id":9007199254740993},
		"stream":false
	}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 4535})

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 4535,
			Name:        "grok-api-key",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 2,
			Credentials: map[string]any{
				"api_key":  "same-token",
				"base_url": "https://api.x.ai/v1",
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusBadRequest,
			Header: http.Header{
				"Content-Type":   []string{"application/json"},
				"Xai-Request-Id": []string{"recoverable-first"},
			},
			Body: io.NopCloser(strings.NewReader(`{"code":"invalid-argument","error":"Could not decrypt the provided encrypted_content. Ensure the value is unmodified."}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":   []string{"application/json"},
				"Xai-Request-Id": []string{"recovered-second"},
			},
			Body: io.NopCloser(strings.NewReader(`{"id":"resp_recovered","object":"response","model":"grok-4.5","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}`)),
		},
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok-4.5", false, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_recovered", result.ResponseID)
	require.Equal(t, "recovered-second", result.RequestID)
	require.Len(t, upstream.requests, 2)
	require.Len(t, upstream.bodies, 2)

	require.Equal(t, "reasoning", gjson.GetBytes(upstream.bodies[0], "input.0.type").String())
	require.Equal(t, "resp_valid_history", gjson.GetBytes(upstream.bodies[0], "previous_response_id").String())
	require.Equal(t, "encrypted-reasoning", gjson.GetBytes(upstream.bodies[0], "input.0.encrypted_content").String())
	require.Equal(t, "reasoning", gjson.GetBytes(upstream.bodies[1], "input.0.type").String())
	require.Equal(t, "resp_valid_history", gjson.GetBytes(upstream.bodies[1], "previous_response_id").String())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists())
	require.Equal(t, "keep this summary", gjson.GetBytes(upstream.bodies[1], "input.0.summary.0.text").String())
	require.Equal(t, "message", gjson.GetBytes(upstream.bodies[1], "input.1.type").String())
	require.False(t, gjson.GetBytes(upstream.bodies[0], "metadata").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "metadata").Exists())

	firstIdentity := gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String()
	secondIdentity := gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String()
	require.NotEmpty(t, firstIdentity)
	require.Equal(t, firstIdentity, secondIdentity)
	for _, req := range upstream.requests {
		require.Equal(t, "Bearer same-token", req.Header.Get("Authorization"))
		require.Equal(t, firstIdentity, req.Header.Get(GrokConversationIDHeader))
	}
	require.Equal(t, billing.StatusActive, provider.Record.Status)
	_, hasUpstreamErrors := c.Get(OpsUpstreamErrorsKey)
	require.False(t, hasUpstreamErrors)
	_, hasTerminalStatus := c.Get(OpsUpstreamStatusCodeKey)
	require.False(t, hasTerminalStatus)
}

func TestForwardGrokResponsesInvalidEncryptedContentRecoveryDoesNotOvermatch(t *testing.T) {
	matchingError := `{"code":"invalid-argument","error":"Could not decrypt the provided encrypted_content."}`
	tests := []struct {
		name         string
		requestBody  string
		responseBody string
	}{
		{
			name:         "different top-level code",
			requestBody:  `{"model":"grok-4.5","input":[{"type":"reasoning","encrypted_content":"cipher"}],"stream":false}`,
			responseBody: `{"code":"bad-request","error":"Could not decrypt the provided encrypted_content."}`,
		},
		{
			name:         "message does not mention decryption",
			requestBody:  `{"model":"grok-4.5","input":[{"type":"reasoning","encrypted_content":"cipher"}],"stream":false}`,
			responseBody: `{"code":"invalid-argument","error":"The provided encrypted_content is invalid."}`,
		},
		{
			name:         "request has no encrypted reasoning",
			requestBody:  `{"model":"grok-4.5","input":[{"type":"message","role":"user","content":"hi"}],"stream":false}`,
			responseBody: matchingError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := []byte(tt.requestBody)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 4536,
					Name:        "grok-api-key",
					Platform:    capability.PlatformGrok,
					Type:        capability.ProviderTypeAPIKey,
					Concurrency: 1,
					Credentials: map[string]any{"api_key": "token", "base_url": "https://api.x.ai/v1"},
				},
			}
			upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(tt.responseBody)),
			}}}
			svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

			result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok-4.5", false, time.Now())
			require.Nil(t, result)
			require.Error(t, err)
			require.Len(t, upstream.requests, 1)
			require.Len(t, upstream.bodies, 1)
		})
	}
}

func TestForwardGrokResponsesInvalidEncryptedContentRecoveryNestedErrorShape(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","input":[{"type":"reasoning","encrypted_content":"cipher"},{"type":"message","role":"user","content":"hi"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 4538,
			Name:        "grok-api-key",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "token", "base_url": "https://api.x.ai/v1"},
		},
	}
	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"code":"invalid-argument","error":{"message":"Could not decrypt the provided encrypted_content."}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_ok","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
		},
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok-4.5", false, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 2)
	require.True(t, gjson.GetBytes(upstream.bodies[0], "input.0.encrypted_content").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists())
}

func TestForwardGrokResponsesInvalidEncryptedContentRetryFailureIsTerminal(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","input":[{"type":"reasoning","encrypted_content":"cipher"},{"type":"message","role":"user","content":"hi"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 4537,
			Name:        "grok-api-key",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "same-token", "base_url": "https://api.x.ai/v1"},
		},
	}
	newInvalidEncryptedResponse := func(requestID string) *http.Response {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header: http.Header{
				"Content-Type":   []string{"application/json"},
				"Xai-Request-Id": []string{requestID},
			},
			Body: io.NopCloser(strings.NewReader(`{"code":"invalid-argument","error":"Could not decrypt the provided encrypted_content."}`)),
		}
	}
	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{
		newInvalidEncryptedResponse("recoverable-first"),
		newInvalidEncryptedResponse("terminal-second"),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok-4.5", false, time.Now())
	require.Nil(t, result)
	require.Error(t, err)
	require.Len(t, upstream.requests, 2)
	require.Len(t, upstream.bodies, 2)
	require.True(t, gjson.GetBytes(upstream.bodies[0], "input.0.encrypted_content").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], `input.#(type=="reasoning")`).Exists())

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*ops.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.NotEmpty(t, events)
	for _, event := range events {
		require.NotEqual(t, "recoverable-first", event.UpstreamRequestID)
	}
	require.Equal(t, http.StatusBadRequest, c.GetInt(OpsUpstreamStatusCodeKey))
}

func TestForwardGrokResponsesNonStreamingUsesCacheIdentityAndCachedUsage(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","input":"hi","stream":false,"tools":[{"type":"namespace","name":"client_tools"}],"tool_choice":{"type":"namespace","name":"client_tools"}}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &apikey.APIKey{ID: 5202})

	provider := gatewaytestkit.HealthyGrokOAuthProvider(56, "access-token")
	observedResetAt := time.Now().Add(-time.Second).UTC().Truncate(time.Second)
	observedLimitedAt := observedResetAt.Add(-grokRateLimitRepeatCooldown)
	provider.Record.RateLimitedAt = &observedLimitedAt
	provider.Record.RateLimitResetAt = &observedResetAt
	repo := &grokQuotaProviderRepo{
		grokFixtureProviders: &grokFixtureProviders{
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{56: provider},
		},
		recoveryClearResult: true,
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-non-stream-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_grok_non_stream","object":"response","model":"grok-4.3","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9,"input_tokens_details":{"cached_tokens":4}}}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok-4.5", false, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream)
	require.Equal(t, "resp_grok_non_stream", result.ResponseID)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, 4, result.Usage.CacheReadInputTokens)
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	identity := gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String()
	require.NotEmpty(t, identity)
	require.Equal(t, identity, upstream.lastReq.Header.Get(GrokConversationIDHeader))
	// Client tool declarations suppress cache-tool injection even when capability filtering removes those tools.
	require.False(t, gjson.GetBytes(upstream.lastBody, "tools").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "tool_choice").Exists())
	require.Equal(t, "resp_grok_non_stream", gjson.Get(recorder.Body.String(), "id").String())
	require.Equal(t, 1, repo.recoveryClearCalls)
	require.Equal(t, observedLimitedAt, repo.recoveryObservedAt)
	require.Equal(t, observedResetAt, repo.recoveryObservedReset)
}

// TestForwardGrokResponsesFreeFunctionToolsUseCacheCapableMixedRoute checks platform-tool injection for Free OAuth
// function-tool requests.
func TestForwardGrokResponsesFreeFunctionToolsUseCacheCapableMixedRoute(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{
		"model":"grok-4.5","input":"look up alpha","stream":false,
		"tools":[
			{"type":"function","name":"lookup","parameters":{"type":"object"}},
			{"type":"function","name":"web_search","parameters":{"type":"object"}}
		],
		"tool_choice":"auto"
	}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 5204})

	provider := gatewaytestkit.HealthyGrokOAuthProvider(60, "access-token")
	provider.Record.Credentials["subscription_tier"] = "free"
	repo := &grokQuotaProviderRepo{
		grokFixtureProviders: &grokFixtureProviders{
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{60: provider},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_grok_tools","object":"response","model":"grok-4.5","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":1}}`,
		)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok-4.5", false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	tools := gjson.GetBytes(upstream.lastBody, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "function", tools[0].Get("type").String())
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
	require.Equal(t, "auto", gjson.GetBytes(upstream.lastBody, "tool_choice").String())
}

func TestForwardGrokResponsesFailoverKeepsCacheIdentityAcrossProviders(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","input":[{"role":"user","content":"stable prefix"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 5203})

	newProvider := func(id int64, token string) *gatewayprovider.ExecutionProvider {
		provider := gatewaytestkit.HealthyGrokOAuthProvider(id, token)
		provider.Record.Name = fmt.Sprintf("grok-%d", id)
		return provider
	}
	firstProvider := newProvider(58, "access-token-a")
	secondProvider := newProvider(59, "access-token-b")
	repo := &grokQuotaProviderRepo{
		grokFixtureProviders: &grokFixtureProviders{
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{58: firstProvider, 59: secondProvider},
		},
	}
	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"temporary"}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_after_failover","object":"response","model":"grok-4.3","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":1}}`)),
		},
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	_, err := svc.Grok.ForwardResponses(context.Background(), c, firstProvider, body, "grok-4.5", false, time.Now())
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)

	result, err := svc.Grok.ForwardResponses(context.Background(), c, secondProvider, body, "grok-4.5", false, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 2)
	require.Len(t, upstream.bodies, 2)
	firstIdentity := gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String()
	secondIdentity := gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String()
	require.NotEmpty(t, firstIdentity)
	require.Equal(t, firstIdentity, secondIdentity)
	require.Equal(t, firstIdentity, upstream.requests[0].Header.Get(GrokConversationIDHeader))
	require.Equal(t, secondIdentity, upstream.requests[1].Header.Get(GrokConversationIDHeader))
	require.Equal(t, "Bearer access-token-a", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer access-token-b", upstream.requests[1].Header.Get("Authorization"))
}

func TestForwardGrokResponsesRejectsMappedImageModelWithClientError(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"image-alias","input":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"image-alias": "grok-imagine-image-quality"},
			},
		},
	}

	result, err := newResponsesFixture(responsesFixtureInputs{}).Grok.ForwardResponses(
		context.Background(), c, provider, body, "image-alias", false, time.Now(),
	)

	require.ErrorContains(t, err, "use /v1/images/generations instead")
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
	require.Equal(t, "model", gjson.GetBytes(recorder.Body.Bytes(), "error.param").String())
	require.Contains(t, gjson.GetBytes(recorder.Body.Bytes(), "error.message").String(), "grok-imagine-image-quality")
}

func TestForwardGrokResponsesClientToolNameConflictReturns400(t *testing.T) {
	body := []byte(`{
		"model":"grok","stream":false,"input":"hello",
		"tools":[
			{"type":"custom","name":"duplicate"},
			{"type":"function","name":"duplicate","parameters":{"type":"object"}}
		]
	}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	upstream := &auxiliaryHTTPRecorder{}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := grokProtocolAPIKeyProvider(7101)

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok", false, time.Now())

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
	require.Equal(t, "tools", gjson.Get(recorder.Body.String(), "error.param").String())
	require.Contains(t, gjson.Get(recorder.Body.String(), "error.message").String(), "conflicts")
	require.Empty(t, upstream.requests, "an ambiguous request must not reach xAI")
}

func TestForwardGrokResponsesMalformedToolSearchOutputReturns400BeforeUpstream(t *testing.T) {
	body := []byte(`{
		"model":"grok","stream":false,
		"tools":[{"type":"tool_search"}],
		"input":[{"type":"tool_search_output","status":"completed"}]
	}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	upstream := &auxiliaryHTTPRecorder{}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := grokProtocolAPIKeyProvider(7103)

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok", false, time.Now())

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
	require.Equal(t, "tools", gjson.Get(recorder.Body.String(), "error.param").String())
	require.Contains(t, gjson.Get(recorder.Body.String(), "error.message").String(), "call_id")
	require.Empty(t, upstream.requests, "malformed lowered output must not reach xAI")
}

func TestForwardGrokResponsesOAuthRestoresClientToolsNonStreaming(t *testing.T) {
	body := groktestkit.ClientToolsRequest(false)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &apikey.APIKey{ID: 7102})

	provider := grokProtocolOAuthProvider(7102)
	repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"protocol-oauth"},
		},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_protocol_oauth","object":"response","model":"grok-4.5","status":"completed",
			"output":[
				{"type":"function_call","id":"item_custom","call_id":"call_custom","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}","namespace":"must_not_leak"},
				{"type":"function_call","id":"item_search","call_id":"call_search","name":"tool_search","arguments":"{\"query\":\"github\"}"},
				{"type":"function_call","id":"item_namespace","call_id":"call_namespace","name":"collaboration__send_message","arguments":"{\"target\":\"root\"}"}
			],
			"usage":{"input_tokens":9,"output_tokens":3,"total_tokens":12}
		}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok", false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream)
	require.Equal(t, "resp_protocol_oauth", result.ResponseID)
	require.Equal(t, grok.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer oauth-protocol-token", upstream.lastReq.Header.Get("Authorization"))
	assertGrokProtocolRequestLowered(t, upstream.lastBody)

	response := recorder.Body.Bytes()
	require.Equal(t, "custom_tool_call", gjson.GetBytes(response, "output.0.type").String())
	require.Equal(t, "*** Begin Patch", gjson.GetBytes(response, "output.0.input").String())
	require.False(t, gjson.GetBytes(response, "output.0.arguments").Exists())
	require.False(t, gjson.GetBytes(response, "output.0.namespace").Exists())
	require.Equal(t, "tool_search_call", gjson.GetBytes(response, "output.1.type").String())
	require.Equal(t, "client", gjson.GetBytes(response, "output.1.execution").String())
	require.Equal(t, "github", gjson.GetBytes(response, "output.1.arguments.query").String())
	require.False(t, gjson.GetBytes(response, "output.1.name").Exists())
	require.Equal(t, "function_call", gjson.GetBytes(response, "output.2.type").String())
	require.Equal(t, "collaboration", gjson.GetBytes(response, "output.2.namespace").String())
	require.Equal(t, "send_message", gjson.GetBytes(response, "output.2.name").String())
}

func TestForwardGrokResponsesAPIKeyRestoresClientToolsFromSSEForNonStreamingRequest(t *testing.T) {
	body := groktestkit.ClientToolsRequest(false)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"text/event-stream"},
			"Xai-Request-Id": []string{"protocol-api-key-sse-nonstream"},
		},
		Body: io.NopCloser(strings.NewReader(grokProtocolUpstreamSSE())),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := grokProtocolAPIKeyProvider(7104)

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok", false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream)
	require.Equal(t, "resp_protocol_stream", result.ResponseID)
	assertGrokProtocolRequestLowered(t, upstream.lastBody)

	response := recorder.Body.Bytes()
	require.True(t, json.Valid(response))
	require.Equal(t, "custom_tool_call", gjson.GetBytes(response, "output.0.type").String())
	require.Equal(t, "*** Begin Patch", gjson.GetBytes(response, "output.0.input").String())
	require.Equal(t, "tool_search_call", gjson.GetBytes(response, "output.1.type").String())
	require.Equal(t, "client", gjson.GetBytes(response, "output.1.execution").String())
	require.Equal(t, "collaboration", gjson.GetBytes(response, "output.2.namespace").String())
	require.Equal(t, "send_message", gjson.GetBytes(response, "output.2.name").String())
}

func TestForwardGrokResponsesAPIKeyRestoresClientToolsStreaming(t *testing.T) {
	body := groktestkit.ClientToolsRequest(true)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"text/event-stream"},
			"Xai-Request-Id": []string{"protocol-api-key"},
		},
		Body: io.NopCloser(strings.NewReader(grokProtocolUpstreamSSE())),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := grokProtocolAPIKeyProvider(7103)

	result, err := svc.Grok.ForwardResponses(context.Background(), c, provider, body, "grok", true, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, "resp_protocol_stream", result.ResponseID)
	require.Equal(t, "https://api.x.ai/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer xai-protocol-key", upstream.lastReq.Header.Get("Authorization"))
	assertGrokProtocolRequestLowered(t, upstream.lastBody)

	frames := parseGrokProtocolSSEFrames(t, recorder.Body.String())
	require.NotEmpty(t, frames)
	for index, frame := range frames {
		require.Equal(t, frame.event, gjson.GetBytes(frame.data, "type").String(), "SSE event field must follow the restored data.type")
		require.Equal(t, 40+index, int(gjson.GetBytes(frame.data, "sequence_number").Int()), "sequence_number must be continuous after suppressed and expanded events")
	}

	created := requireGrokProtocolFrame(t, frames, "response.created", "", "")
	require.True(t, gjson.GetBytes(created.data, "upstream_extension.preserved").Bool())
	customAdded := requireGrokProtocolFrame(t, frames, "response.output_item.added", "item.type", "custom_tool_call")
	require.Equal(t, "apply_patch", gjson.GetBytes(customAdded.data, "item.name").String())
	customInputDelta := requireGrokProtocolFrame(t, frames, "response.custom_tool_call_input.delta", "", "")
	require.Equal(t, "*** Begin Patch", gjson.GetBytes(customInputDelta.data, "delta").String())
	customInputDone := requireGrokProtocolFrame(t, frames, "response.custom_tool_call_input.done", "", "")
	require.Equal(t, "*** Begin Patch", gjson.GetBytes(customInputDone.data, "input").String())
	customDone := requireGrokProtocolFrame(t, frames, "response.output_item.done", "item.type", "custom_tool_call")
	require.Equal(t, "*** Begin Patch", gjson.GetBytes(customDone.data, "item.input").String())

	namespaceAdded := requireGrokProtocolFrame(t, frames, "response.output_item.added", "item.namespace", "collaboration")
	require.Equal(t, "send_message", gjson.GetBytes(namespaceAdded.data, "item.name").String())
	namespaceDone := requireGrokProtocolFrame(t, frames, "response.output_item.done", "item.namespace", "collaboration")
	require.Equal(t, "send_message", gjson.GetBytes(namespaceDone.data, "item.name").String())
	namespaceArgumentsDone := requireGrokProtocolFrame(t, frames, "response.function_call_arguments.done", "name", "send_message")
	require.Equal(t, "response.function_call_arguments.done", gjson.GetBytes(namespaceArgumentsDone.data, "type").String())
	require.False(t, gjson.GetBytes(namespaceArgumentsDone.data, "namespace").Exists())

	searchAdded := requireGrokProtocolFrame(t, frames, "response.output_item.added", "item.type", "tool_search_call")
	require.Equal(t, "client", gjson.GetBytes(searchAdded.data, "item.execution").String())
	searchDone := requireGrokProtocolFrame(t, frames, "response.output_item.done", "item.type", "tool_search_call")
	require.Equal(t, "github", gjson.GetBytes(searchDone.data, "item.arguments.query").String())

	for _, frame := range frames {
		itemID := gjson.GetBytes(frame.data, "item_id").String()
		if itemID == "item_custom" || itemID == "item_search" {
			require.NotContains(t, frame.event, "function_call_arguments", "client-only proxy argument events must not leak")
		}
	}
	completed := requireGrokProtocolFrame(t, frames, "response.completed", "", "")
	require.Equal(t, "custom_tool_call", gjson.GetBytes(completed.data, "response.output.0.type").String())
	require.Equal(t, "tool_search_call", gjson.GetBytes(completed.data, "response.output.1.type").String())
	require.Equal(t, "collaboration", gjson.GetBytes(completed.data, "response.output.2.namespace").String())
}

func grokProtocolOAuthProvider(id int64) *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id, Name: "grok-oauth-protocol", Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth,
			Status: billing.StatusActive, Schedulable: true, Concurrency: 1,
			Credentials: map[string]any{
				"access_token": "oauth-protocol-token", "refresh_token": "refresh-token",
				"expires_at": time.Now().Add(2 * providercore.GrokTokenRefreshSkew).UTC().Format(time.RFC3339),
				"base_url":   grok.DefaultCLIBaseURL, "subscription_tier": "supergrok",
			},
		},
	}
}

func grokProtocolAPIKeyProvider(id int64) *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id, Name: "grok-api-key-protocol", Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey,
			Status: billing.StatusActive, Schedulable: true, Concurrency: 1,
			Credentials: map[string]any{"api_key": "xai-protocol-key", "base_url": "https://api.x.ai/v1"},
		},
	}
}

func assertGrokProtocolRequestLowered(t *testing.T, body []byte) {
	t.Helper()
	require.True(t, json.Valid(body))
	require.False(t, gjson.GetBytes(body, `tools.#(type=="custom")`).Exists())
	require.False(t, gjson.GetBytes(body, `tools.#(type=="namespace")`).Exists())
	require.False(t, gjson.GetBytes(body, `tools.#(type=="tool_search")`).Exists())
	require.True(t, gjson.GetBytes(body, `tools.#(name=="apply_patch")`).Exists())
	require.True(t, gjson.GetBytes(body, `tools.#(name=="tool_search")`).Exists())
	require.True(t, gjson.GetBytes(body, `tools.#(name=="collaboration__send_message")`).Exists())
	require.Equal(t, "function", gjson.GetBytes(body, "tool_choice.type").String())
	require.Equal(t, "apply_patch", gjson.GetBytes(body, "tool_choice.name").String())
	require.Equal(t, "function_call", gjson.GetBytes(body, "input.0.type").String())
	require.Equal(t, "function_call_output", gjson.GetBytes(body, "input.1.type").String())
	require.Equal(t, "function_call", gjson.GetBytes(body, "input.2.type").String())
	require.Equal(t, "tool_search", gjson.GetBytes(body, "input.2.name").String())
	require.Equal(t, "function_call_output", gjson.GetBytes(body, "input.3.type").String())
	require.Equal(t, "collaboration__send_message", gjson.GetBytes(body, "input.4.name").String())
	require.False(t, gjson.GetBytes(body, "input.4.namespace").Exists())
}

func grokProtocolUpstreamSSE() string {
	events := []string{
		`{"type":"response.created","sequence_number":40,"response":{"id":"resp_protocol_stream","model":"grok-4.5"},"upstream_extension":{"preserved":true}}`,
		`{"type":"response.output_item.added","sequence_number":41,"output_index":0,"item":{"type":"function_call","id":"item_custom","call_id":"call_custom","name":"apply_patch","arguments":"","status":"in_progress"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":42,"output_index":0,"item_id":"item_custom","delta":"{\"input\":\"*** Begin"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":43,"output_index":0,"item_id":"item_custom","delta":" Patch\"}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":44,"output_index":0,"item_id":"item_custom","call_id":"call_custom","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"}`,
		`{"type":"response.output_item.done","sequence_number":45,"output_index":0,"item":{"type":"function_call","id":"item_custom","call_id":"call_custom","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}","status":"completed"}}`,
		`{"type":"response.output_item.added","sequence_number":46,"output_index":1,"item":{"type":"function_call","id":"item_namespace","call_id":"call_namespace","name":"collaboration__send_message","arguments":"","status":"in_progress"}}`,
		`{"type":"response.function_call_arguments.done","sequence_number":47,"output_index":1,"item_id":"item_namespace","call_id":"call_namespace","name":"collaboration__send_message","arguments":"{\"target\":\"root\"}"}`,
		`{"type":"response.output_item.done","sequence_number":48,"output_index":1,"item":{"type":"function_call","id":"item_namespace","call_id":"call_namespace","name":"collaboration__send_message","arguments":"{\"target\":\"root\"}","status":"completed"}}`,
		`{"type":"response.output_item.added","sequence_number":49,"output_index":2,"item":{"type":"function_call","id":"item_search","call_id":"call_search","name":"tool_search","arguments":"","status":"in_progress"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":50,"output_index":2,"item_id":"item_search","delta":"{\"query\":\"github\"}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":51,"output_index":2,"item_id":"item_search","call_id":"call_search","name":"tool_search","arguments":"{\"query\":\"github\"}"}`,
		`{"type":"response.output_item.done","sequence_number":52,"output_index":2,"item":{"type":"function_call","id":"item_search","call_id":"call_search","name":"tool_search","arguments":"{\"query\":\"github\"}","status":"completed"}}`,
		`{"type":"response.completed","sequence_number":53,"response":{"id":"resp_protocol_stream","object":"response","model":"grok-4.5","status":"completed","output":[{"type":"function_call","id":"item_custom","call_id":"call_custom","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"},{"type":"function_call","id":"item_search","call_id":"call_search","name":"tool_search","arguments":"{\"query\":\"github\"}"},{"type":"function_call","id":"item_namespace","call_id":"call_namespace","name":"collaboration__send_message","arguments":"{\"target\":\"root\"}"}],"usage":{"input_tokens":11,"output_tokens":4,"total_tokens":15}}}`,
	}
	var out strings.Builder
	for _, event := range events {
		typ := gjson.Get(event, "type").String()
		fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", typ, event)
	}
	return out.String()
}

func parseGrokProtocolSSEFrames(t *testing.T, body string) []grokProtocolSSEFrame {
	t.Helper()
	var frames []grokProtocolSSEFrame
	event := ""
	for rawLine := range strings.SplitSeq(body, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if value, ok := openai.ExtractSSEEventLine(line); ok {
			event = strings.TrimSpace(value)
			continue
		}
		data, ok := openai.ExtractSSEDataLine(line)
		if !ok || strings.TrimSpace(data) == "[DONE]" {
			continue
		}
		require.NotEmpty(t, event, "every data frame from this upstream should retain an event field")
		require.JSONEq(t, data, data)
		frames = append(frames, grokProtocolSSEFrame{event: event, data: []byte(data)})
		event = ""
	}
	return frames
}

func requireGrokProtocolFrame(t *testing.T, frames []grokProtocolSSEFrame, eventType, path, value string) grokProtocolSSEFrame {
	t.Helper()
	for _, frame := range frames {
		if frame.event != eventType {
			continue
		}
		if path == "" || gjson.GetBytes(frame.data, path).String() == value {
			return frame
		}
	}
	t.Fatalf("missing SSE frame event=%q %s=%q", eventType, path, value)
	return grokProtocolSSEFrame{}
}
