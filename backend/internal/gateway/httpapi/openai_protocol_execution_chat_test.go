package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	openaicore "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestAdaptiveProtocolRoutesChatCompletionsToNativeChat captures standard Chat
// input for an adaptive Zhipu target. It checks the configured Chat URL,
// messages array and absence of Responses input before fixture transport
// failure.
func TestAdaptiveProtocolRoutesChatCompletionsToNativeChat(t *testing.T) {
	body := []byte(`{"model":"glm-4.7","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	upstream := &auxiliaryHTTPRecorder{err: errors.New("stop after capture")}
	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := adaptiveProtocolTestProvider(capability.PlatformZhipu, map[string]any{
		providercore.APIProtocolChatCompletions: "http://chat.example",
		providercore.APIProtocolAnthropic:       "http://anthropic.example",
	})

	_, err := svc.Text.Chat(context.Background(), adaptiveProtocolTestContext("/v1/chat/completions", body), provider, body, "", "")
	require.Error(t, err)
	require.Equal(t, "http://chat.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").IsArray())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
}

// TestAdaptiveProtocolRoutesResponsesShapedChatToNativeResponses captures
// Responses-shaped input at the Chat entry for adaptive DeepSeek. It checks
// the native Responses URL and retained input shape before fixture transport
// failure.
func TestAdaptiveProtocolRoutesResponsesShapedChatToNativeResponses(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4","input":"hello","max_output_tokens":32,"stream":false}`)
	upstream := &auxiliaryHTTPRecorder{err: errors.New("stop after capture")}
	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := adaptiveProtocolTestProvider(capability.PlatformDeepseek, map[string]any{
		providercore.APIProtocolChatCompletions: "http://chat.example",
		providercore.APIProtocolAnthropic:       "http://anthropic.example",
		providercore.APIProtocolResponses:       "http://responses.example",
	})

	_, err := svc.Text.Chat(context.Background(), adaptiveProtocolTestContext("/v1/chat/completions", body), provider, body, "", "")
	require.Error(t, err)
	require.Equal(t, "http://responses.example/responses", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
}

// TestAdaptiveProtocolConvertsResponsesShapedChatForChatOnlyProvider supplies
// Responses-shaped input to adaptive Zhipu without native Responses. It checks
// the Chat URL, converted messages and removed input field before transport
// failure.
func TestAdaptiveProtocolConvertsResponsesShapedChatForChatOnlyProvider(t *testing.T) {
	body := []byte(`{"model":"glm-4.7","input":"hello","max_output_tokens":32,"stream":false}`)
	upstream := &auxiliaryHTTPRecorder{err: errors.New("stop after capture")}
	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := adaptiveProtocolTestProvider(capability.PlatformZhipu, map[string]any{
		providercore.APIProtocolChatCompletions: "http://chat.example",
		providercore.APIProtocolAnthropic:       "http://anthropic.example",
	})

	_, err := svc.Text.Chat(context.Background(), adaptiveProtocolTestContext("/v1/chat/completions", body), provider, body, "", "")
	require.Error(t, err)
	require.Equal(t, "http://chat.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").IsArray())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
}

// TestAdaptiveProtocolRoutesKimiResponsesShapedChatToNativeResponses supplies
// Responses-shaped Chat-entry input to adaptive Kimi. It checks the configured
// versioned Responses URL and retained input shape before fixture transport
// failure.
func TestAdaptiveProtocolRoutesKimiResponsesShapedChatToNativeResponses(t *testing.T) {
	body := []byte(`{"model":"k3-256k","input":"hello","max_output_tokens":32,"stream":false}`)
	upstream := &auxiliaryHTTPRecorder{err: errors.New("stop after capture")}
	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := adaptiveProtocolTestProvider(capability.PlatformKimi, map[string]any{
		providercore.APIProtocolChatCompletions: "http://chat.example",
		providercore.APIProtocolAnthropic:       "http://anthropic.example",
		providercore.APIProtocolResponses:       "http://responses.example/v1",
	})

	_, err := svc.Text.Chat(context.Background(), adaptiveProtocolTestContext("/v1/chat/completions", body), provider, body, "", "")
	require.Error(t, err)
	require.Equal(t, "http://responses.example/v1/responses", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
}

// TestForwardAsChatCompletions_ServiceTierFastNormalizedToPriorityUpstream
// captures a fast alias before an upstream HTTP 400. It checks the outgoing
// priority tier and the expected rejection after capture.
func TestForwardAsChatCompletions_ServiceTierFastNormalizedToPriorityUpstream(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"service_tier":"fast","stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid-chat-st"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop"}}`)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}, transport: upstream, readers: newHTTPReadersFixture(&gatewaytestkit.FastPolicySettingsRepo{Values: map[string]string{}}, nil)})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 21,
			Name:        "openai-compatible",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-compatible"},
			Extra:       map[string]any{},
		},
	}

	_, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.5")
	require.Error(t, err) // The upstream returns 400 after the recorder captures the request.
	require.NotNil(t, upstream.lastBody)
	require.Equal(t, "priority", gjson.GetBytes(upstream.lastBody, "service_tier").String(),
		"client alias fast must reach upstream as priority")
}

// TestForwardAsChatCompletions_ServiceTierPriorityPreservedUpstream captures a
// priority request before an upstream HTTP 400. It checks the outgoing
// priority value and the expected fixture rejection after request
// construction.
func TestForwardAsChatCompletions_ServiceTierPriorityPreservedUpstream(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"service_tier":"priority","stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid-chat-st2"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop"}}`)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}, transport: upstream, readers: newHTTPReadersFixture(&gatewaytestkit.FastPolicySettingsRepo{Values: map[string]string{}}, nil)})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 2,
			Name:        "openai-compatible",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-compatible"},
			Extra:       map[string]any{},
		},
	}

	_, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.5")
	require.Error(t, err)
	require.NotNil(t, upstream.lastBody)
	require.Equal(t, "priority", gjson.GetBytes(upstream.lastBody, "service_tier").String())
}

// TestForwardAsChatCompletions_KeepsOutboundAndObservedServiceTiersSeparate
// sends fast while the terminal Response declares default. It checks requested
// priority in the result, observed default attribution and default in buffered
// client output.
func TestForwardAsChatCompletions_KeepsOutboundAndObservedServiceTiersSeparate(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"service_tier":"fast","stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	streamPayload := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_c1\",\"object\":\"response\",\"model\":\"gpt-5.5\",\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"it_1\",\"output_index\":0,\"delta\":\"hi\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_c1\",\"object\":\"response\",\"model\":\"gpt-5.5\",\"status\":\"completed\",\"service_tier\":\"default\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n" +
		"data: [DONE]\n\n"

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid-chat-echo"}},
		Body:       io.NopCloser(strings.NewReader(streamPayload)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{Request: OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{Enabled: false}}}, transport: upstream, readers: newHTTPReadersFixture(&gatewaytestkit.FastPolicySettingsRepo{Values: map[string]string{}}, nil)})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 21,
			Name:        "openai-compatible",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-compatible"},
			Extra:       map[string]any{},
			Status:      billingcore.StatusActive,
			Schedulable: true,
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.5")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.ServiceTier)
	require.Equal(t, "priority", *result.ServiceTier)
	require.Equal(t, "default", result.UpstreamResponseServiceTier)
	// Buffered Chat conversion reports the observed default tier to the client.
	require.Contains(t, rec.Body.String(), `"service_tier":"default"`)
	require.NotContains(t, rec.Body.String(), `"service_tier":"priority"`)
}

// TestOpenAIAgentIdentityChatRecoveryKeepsAutoDerivedSessionIsolationStable
// returns invalid-task errors around a local registration exchange. It checks
// two captured requests with the same derived cache key and isolated session
// header after recovery.
func TestOpenAIAgentIdentityChatRecoveryKeepsAutoDerivedSessionIsolationStable(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 52, Name: "agent-identity", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
			Status: billingcore.StatusActive, Schedulable: true, Concurrency: 1,
			Credentials: map[string]any{
				"auth_mode":         providercore.OpenAIAuthModeAgentIdentity,
				"agent_runtime_id":  key.RuntimeID,
				"agent_private_key": privateKey,
				"task_id":           "task-cache-old",
			},
			Extra: map[string]any{},
		},
	}
	repo := &agentIdentityForwardRepo{provider: provider}
	// A local registration endpoint returns the replacement task identity; close
	// it after both captured attempts so recovery uses only this fixture exchange.
	registerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Discard handler write errors in this fixture. Later assertions inspect
		// captured retry identities, rather than propagation of a registration
		// response write failure.
		_, _ = io.WriteString(w, `{"task_id":"task-cache-new"}`)
	}))
	defer registerServer.Close()

	invalidTask := func() *http.Response {
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))}
	}
	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{invalidTask(), invalidTask()}}
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}, providers: repo, registerTaskURL: registerServer.URL, transport: upstream})
	body := []byte(`{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 99})

	_, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.4")
	require.Error(t, err)
	require.Len(t, upstream.requests, 2)
	firstKey := gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String()
	secondKey := gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String()
	require.NotEmpty(t, firstKey)
	require.Equal(t, firstKey, secondKey)
	require.Equal(t, upstreamcore.GenerateSessionUUID(openai.IsolateOpenAIUpstreamSessionID(99, provideradapter.CodexIdentityNamespace(CodexIdentityRecord(c, provider.View())), firstKey)), upstream.requests[0].Header.Get("session_id"))
	require.Equal(t, upstream.requests[0].Header.Get("session_id"), upstream.requests[1].Header.Get("session_id"))
}

// TestForwardAsChatCompletions_OpenAICompatibleRawUsageGuard enumerates native
// Chat model and usage shapes, including mapped aliases. It checks successful
// forwarding for admitted fixtures and pre-output missing-Grok-usage recovery
// with request and Ops identity for guarded fixtures.
func TestForwardAsChatCompletions_OpenAICompatibleRawUsageGuard(t *testing.T) {
	tests := []struct {
		name             string // fixture case name
		model            string // client request model
		upstreamResponse string // upstream JSON response
		// optional provider model mapping
		modelMapping map[string]any
		wantGuarded  bool // expected missing-usage rejection
	}{
		{
			name:             "Grok response without usage",
			model:            "grok-4.5",
			upstreamResponse: `{"id":"resp_missing","object":"chat.completion","model":"grok-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantGuarded:      true,
		},
		{
			name:             "namespaced Grok response without usage",
			model:            "x-ai/grok-4.5",
			upstreamResponse: `{"id":"resp_namespaced","object":"chat.completion","model":"x-ai/grok-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantGuarded:      true,
		},
		{
			name:             "Grok response with aggregate usage passes",
			model:            "grok-4.5",
			upstreamResponse: `{"id":"resp_usage","object":"chat.completion","model":"grok-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`,
			wantGuarded:      false,
		},
		{
			name:             "Grok alias mapped to non-Grok remains unchanged",
			model:            "grok-alias",
			upstreamResponse: `{"id":"resp_mapped","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			modelMapping:     map[string]any{"grok-alias": "gpt-5.4"},
			wantGuarded:      false,
		},
		{
			name:             "Grok response with detail-only usage",
			model:            "grok-4.5",
			upstreamResponse: `{"id":"resp_detail_only","object":"chat.completion","model":"grok-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"input_tokens_details":{"text_tokens":9,"image_tokens":2},"output_tokens_details":{"image_tokens":1}}}`,
			wantGuarded:      true,
		},
		{
			name:             "non-Grok response without usage remains unchanged",
			model:            "gpt-5.4",
			upstreamResponse: `{"id":"resp_openai","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			wantGuarded:      false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			body := []byte(`{"model":"` + testCase.model + `","messages":[{"role":"user","content":"hello"}],"stream":false}`)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

			upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"rid-openai-compatible"}},
				Body:       io.NopCloser(strings.NewReader(testCase.upstreamResponse)),
			}}
			service := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
			provider := rawChatCompletionsTestProvider()
			provider.Record.Name = "openai-compatible"
			provider.Record.Extra = map[string]any{"openai_responses_probe_status": "unsupported"}
			if testCase.modelMapping != nil {
				provider.Record.Credentials["model_mapping"] = testCase.modelMapping
			}

			result, err := service.Text.Chat(context.Background(), c, provider, body, "", "")

			if !testCase.wantGuarded {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, c.Writer.Written())
				return
			}
			require.Nil(t, result)
			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.Equal(t, "grok_missing_usage", gjson.GetBytes(failoverErr.ResponseBody, "error.code").String())
			require.Equal(t, "rid-openai-compatible", http.Header(failoverErr.ResponseHeaders).Get("x-request-id"))
			require.False(t, c.Writer.Written())
			require.Empty(t, recorder.Body.String())
			rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			events, ok := rawEvents.([]*ops.OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.NotEmpty(t, events)
			require.Equal(t, capability.PlatformGrok, events[len(events)-1].Platform)
		})
	}
}

// TestForwardAsChatCompletions_PreserveClientProtocolUsesVersionedChatURL
// forwards native Chat to a versioned configured base. It checks URL joining,
// TLS-selected User-Agent, request shape, observed usage and buffered answer
// output.
func TestForwardAsChatCompletions_PreserveClientProtocolUsesVersionedChatURL(t *testing.T) {
	body := []byte(`{"model":"glm-4.5-air","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_raw_chat"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"chatcmpl_1","object":"chat.completion","model":"glm-4.5-air","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
			)),
		},
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()
	provider.Record.Credentials["base_url"] = "https://open.bigmodel.cn/api/paas/v4"

	tlsMatch := egress.TLSFingerprintRouterMatchResult{Matched: true, UpstreamUserAgent: "router-agent"}
	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "", tlsMatch)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, "ok", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://open.bigmodel.cn/api/paas/v4/chat/completions", upstream.requests[0].URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.Equal(t, "router-agent", upstream.requests[0].Header.Get("User-Agent"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"content":"ok"`)
}

// TestForwardAsChatCompletions_UnknownModelWithoutMessagesDispatchKeepsRequestedModel
// captures an unknown OAuth model before model-not-found rejection. It checks
// the requested model remains outgoing and the client receives the fixture
// HTTP 400.
func TestForwardAsChatCompletions_UnknownModelWithoutMessagesDispatchKeepsRequestedModel(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt6","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_chat_unknown_model"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"model not found"}}`)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream, options: &responsesFixtureOptions{Request: OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{Enabled: false}}}})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, "gpt6", gjson.GetBytes(upstream.lastBody, "model").String())
	require.NotEqual(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestForwardAsChatCompletions_APIKeyPropagatesPromptCacheKeyInResponsesBody
// forces API-key Chat through Responses with an explicit cache seed. It checks
// outgoing key/model, endpoint observation, authorization and isolated session
// header before rejection.
func TestForwardAsChatCompletions_APIKeyPropagatesPromptCacheKeyInResponsesBody(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &apikey.APIKey{ID: 99})
	SetActualOpenAIUpstreamEndpoint(c, "/v1/chat/completions")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_chat_prompt_cache"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop before response parsing"}}`)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream, options: &responsesFixtureOptions{Request: OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{Enabled: false}}}})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 2,
			Name:        "openai-compatible",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-compatible",
			},
			Extra: map[string]any{
				"openai_text_route_mode": "force_responses",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "cache-key-123", "gpt-5.4")
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, "cache-key-123", gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "https://api.openai.com/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "/v1/responses", GetActualOpenAIUpstreamEndpoint(c))
	require.Equal(t, "Bearer sk-compatible", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, upstreamcore.GenerateSessionUUID(upstreamcore.IsolateSessionID(99, "cache-key-123")), upstream.lastReq.Header.Get("session_id"))
}

// TestForwardAsChatCompletions_APIKeyResponsesRecordsThirdPartyMaxEffort
// forces DeepSeek Chat input with max effort through Responses. It checks the
// endpoint and outgoing reasoning control plus returned max attribution.
func TestForwardAsChatCompletions_APIKeyResponsesRecordsThirdPartyMaxEffort(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"max","stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_deepseek_max","object":"response","model":"deepseek-v4-flash","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream, options: &responsesFixtureOptions{Request: OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{Enabled: false}}}})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 23,
			Name:        "deepseek-compatible",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-compatible"},
			Extra: map[string]any{
				"openai_text_route_mode": "force_responses",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://api.openai.com/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "max", *result.ReasoningEffort)
}

// TestForwardAsChatCompletions_APIKeyResponsesDoesNotRecordDroppedNestedEffort
// supplies nested reasoning.effort to Chat conversion. It checks absence of
// outgoing reasoning and returned effort after the successful fixture
// response.
func TestForwardAsChatCompletions_APIKeyResponsesDoesNotRecordDroppedNestedEffort(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// Supply nested effort that Chat conversion discards, then inspect outgoing
	// controls and result attribution to catch accidental restoration from client bytes.
	body := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}],"reasoning":{"effort":"max"},"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_deepseek_default","object":"response","model":"deepseek-v4-flash","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream, options: &responsesFixtureOptions{Request: OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{Enabled: false}}}})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 23,
			Name:        "deepseek-compatible",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-compatible"},
			Extra: map[string]any{
				"openai_text_route_mode": "force_responses",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning").Exists())
	require.Nil(t, result.ReasoningEffort)
}

// TestForwardAsChatCompletions_TransportErrorFailsOver injects EOF after one
// captured Responses request. It checks absent result, a typed 502 recovery
// error and no hard client body before outer retry handling.
func TestForwardAsChatCompletions_TransportErrorFailsOver(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{
		err: errors.New(`Post "https://api.openai.com/v1/responses": EOF`),
	}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream, options: &responsesFixtureOptions{Request: OpenAIRequestOptions{URLPolicy: egress.OperatorURLPolicy{Enabled: false}}}})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 22,
			Name:        "openai-compatible",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key": "sk-compatible",
			},
			Extra: map[string]any{
				"openai_text_route_mode": "force_responses",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.4")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "transport error must trigger provider failover")
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, 0, rec.Body.Len(), "service must not write a hard 502 before handler can fail over")
}

// TestForwardAsChatCompletions_APIKeyAutoDerivesStableIsolatedPromptCacheKey
// compares appended history on one key with the same history on another. It
// checks stable first-key seeds and session headers, plus distinct other-key
// values.
func TestForwardAsChatCompletions_APIKeyAutoDerivesStableIsolatedPromptCacheKey(t *testing.T) {
	response := func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop before response parsing"}}`)),
		}
	}
	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{response(), response(), response()}}
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}, transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 2, Name: "openai-compatible", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-compatible"},
			Extra: map[string]any{
				"openai_text_route_mode": "force_responses",
			},
		},
	}
	firstBody := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hello"}],"stream":false}`)
	appendedBody := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"continue"}],"stream":false}`)

	forward := func(apiKeyID int64, body []byte) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("api_key", &apikey.APIKey{ID: apiKeyID})
		result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.4")
		require.Error(t, err)
		require.Nil(t, result)
	}

	forward(99, firstBody)
	forward(99, appendedBody)
	forward(100, appendedBody)

	firstKey := gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String()
	appendedKey := gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String()
	otherTenantKey := gjson.GetBytes(upstream.bodies[2], "prompt_cache_key").String()
	require.NotEmpty(t, firstKey)
	require.Equal(t, firstKey, appendedKey)
	require.NotEqual(t, firstKey, otherTenantKey)
	require.Equal(t, upstreamcore.GenerateSessionUUID(firstKey), upstream.requests[0].Header.Get("session_id"))
	require.Equal(t, upstream.requests[0].Header.Get("session_id"), upstream.requests[1].Header.Get("session_id"))
	require.Equal(t, upstreamcore.GenerateSessionUUID(otherTenantKey), upstream.requests[2].Header.Get("session_id"))
	require.NotEqual(t, upstream.requests[1].Header.Get("session_id"), upstream.requests[2].Header.Get("session_id"))
}

// TestForwardAsChatCompletions_ResponsesShapeDoesNotAutoDerivePromptCacheKey
// compares unrelated Responses-shaped bodies and one explicit cache seed. It
// checks absent automatic keys/headers and isolated propagation of the
// explicit seed.
func TestForwardAsChatCompletions_ResponsesShapeDoesNotAutoDerivePromptCacheKey(t *testing.T) {
	response := func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop before response parsing"}}`)),
		}
	}
	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{response(), response(), response()}}
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}, transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 2, Name: "openai-compatible", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-compatible"},
			Extra: map[string]any{
				"openai_text_route_mode": "force_responses",
			},
		},
	}
	firstBody := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_text","text":"first unrelated input"}]}],"stream":false}`)
	secondBody := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_text","text":"second unrelated input"}]}],"stream":false}`)

	forward := func(body []byte, promptCacheKey string) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		c.Set("api_key", &apikey.APIKey{ID: 99})
		result, err := svc.Text.Chat(context.Background(), c, provider, body, promptCacheKey, "gpt-5.4")
		require.Error(t, err)
		require.Nil(t, result)
	}

	forward(firstBody, "")
	forward(secondBody, "")
	forward(secondBody, "explicit-responses-key")

	require.False(t, gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").Exists())
	require.Empty(t, upstream.requests[0].Header.Get("session_id"))
	require.False(t, gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").Exists())
	require.Empty(t, upstream.requests[1].Header.Get("session_id"))
	require.Equal(t, "explicit-responses-key", gjson.GetBytes(upstream.bodies[2], "prompt_cache_key").String())
	require.Equal(t, upstreamcore.GenerateSessionUUID(upstreamcore.IsolateSessionID(99, "explicit-responses-key")), upstream.requests[2].Header.Get("session_id"))
}

// TestForwardAsChatCompletions_OAuthDoesNotInjectDefaultInstructions captures
// an OAuth Chat request without system content. It checks the Codex target,
// present empty instructions and absence of the listed default instruction
// text.
func TestForwardAsChatCompletions_OAuthDoesNotInjectDefaultInstructions(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_chat_no_default_instructions"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop before response parsing"}}`)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}, transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 3,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.4")
	require.Error(t, err)
	require.Nil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, ChatgptCodexURL, upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "instructions").Exists())
	require.Equal(t, "", gjson.GetBytes(upstream.lastBody, "instructions").String())
	require.NotContains(t, string(upstream.lastBody), "Communicate with the user by streaming thinking")
}

// forwardOAuthChatCompletionsForUpstreamBody captures one OAuth request before
// a deliberate HTTP 400. It asserts the expected error, absent result and
// nonempty body, then returns the recorder-owned outgoing bytes for focused
// request assertions.
func forwardOAuthChatCompletionsForUpstreamBody(t *testing.T, body []byte) []byte {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_chat_system_promotion"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop before response parsing"}}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}, transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 4,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.4")
	require.Error(t, err)
	require.Nil(t, result)
	require.NotEmpty(t, upstream.lastBody)
	return upstream.lastBody
}

// TestForwardAsChatCompletions_OAuthPromotesSystemMessageWithoutDuplication
// converts text-only system and user messages for OAuth. It checks one
// instructions occurrence and a remaining user-only input array after
// promotion.
func TestForwardAsChatCompletions_OAuthPromotesSystemMessageWithoutDuplication(t *testing.T) {
	const systemPrompt = "Unique system prefix for token accounting."
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"` + systemPrompt + `"},{"role":"user","content":"hello"}],"stream":false}`)

	upstreamBody := forwardOAuthChatCompletionsForUpstreamBody(t, body)

	require.Equal(t, systemPrompt, gjson.GetBytes(upstreamBody, "instructions").String())
	require.Equal(t, int64(1), gjson.GetBytes(upstreamBody, "input.#").Int())
	require.Equal(t, "user", gjson.GetBytes(upstreamBody, "input.0.role").String())
	require.Equal(t, 1, strings.Count(string(upstreamBody), systemPrompt))
}

// TestForwardAsChatCompletions_OAuthJsonObjectKeepsSystemMessageInInput
// requests JSON_OBJECT with text-only system content. It checks instructions
// plus retained developer input and two occurrences of that fixture system
// text.
func TestForwardAsChatCompletions_OAuthJsonObjectKeepsSystemMessageInInput(t *testing.T) {
	const systemPrompt = "Return JSON only."
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"` + systemPrompt + `"},{"role":"user","content":"symbol data"}],"response_format":{"type":" JSON_OBJECT "},"stream":false}`)

	upstreamBody := forwardOAuthChatCompletionsForUpstreamBody(t, body)

	require.Equal(t, systemPrompt, gjson.GetBytes(upstreamBody, "instructions").String())
	require.Equal(t, int64(2), gjson.GetBytes(upstreamBody, "input.#").Int())
	require.Equal(t, "developer", gjson.GetBytes(upstreamBody, "input.0.role").String())
	require.Equal(t, systemPrompt, gjson.GetBytes(upstreamBody, "input.0.content").String())
	require.Equal(t, 2, strings.Count(string(upstreamBody), systemPrompt))
}

// TestForwardAsChatCompletions_OAuthKeepsMixedSystemContentInInput supplies
// system text and an image together. It checks promoted instructions and
// retained developer content with the converted input-image URL.
func TestForwardAsChatCompletions_OAuthKeepsMixedSystemContentInInput(t *testing.T) {
	const systemPrompt = "Inspect this reference image."
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":[{"type":"text","text":"` + systemPrompt + `"},{"type":"image_url","image_url":{"url":"https://example.com/reference.png"}}]},{"role":"user","content":"hello"}],"stream":false}`)

	upstreamBody := forwardOAuthChatCompletionsForUpstreamBody(t, body)

	require.Equal(t, systemPrompt, gjson.GetBytes(upstreamBody, "instructions").String())
	require.Equal(t, int64(2), gjson.GetBytes(upstreamBody, "input.#").Int())
	require.Equal(t, "developer", gjson.GetBytes(upstreamBody, "input.0.role").String())
	require.Equal(t, int64(2), gjson.GetBytes(upstreamBody, "input.0.content.#").Int())
	require.Equal(t, "input_image", gjson.GetBytes(upstreamBody, "input.0.content.1.type").String())
	require.Equal(t, "https://example.com/reference.png", gjson.GetBytes(upstreamBody, "input.0.content.1.image_url").String())
}

// TestForwardAsChatCompletions_ClientDisconnectDrainsUpstreamUsage rejects
// downstream writes before terminal Responses usage arrives. It checks
// successful returned observations with the fixture input, output and
// cached-token quantities after draining.
func TestForwardAsChatCompletions_ClientDisconnectDrainsUpstreamUsage(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Writer = &openAIChatFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":5,"total_tokens":16,"input_tokens_details":{"cached_tokens":4}}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_disconnect"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.1")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Equal(t, 4, result.Usage.CacheReadInputTokens)
}

// TestForwardAsChatCompletions_BufferedContextWindowResponseFailedReturnsErrorWithoutFailover
// supplies a buffered context-window failure terminal. It checks no result, no
// failover wrapper and a written JSON 502 carrying the failure message.
func TestForwardAsChatCompletions_BufferedContextWindowResponseFailedReturnsErrorWithoutFailover(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"large prompt"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"id":"resp_failed","object":"response","model":"gpt-5.5","status":"failed","output":[],"error":{"code":"upstream_error","message":"input exceeds the context window"}}}`,
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_failed_buffered"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.5")
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.True(t, c.Writer.Written())
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "input exceeds the context window")
}

// TestForwardAsChatCompletions_StreamContextWindowResponseFailedReturnsErrorWithoutFailover
// supplies a pre-content context-window terminal to a streaming request. It
// checks a returned observation, JSON 502, no failover wrapper and no DONE
// marker.
func TestForwardAsChatCompletions_StreamContextWindowResponseFailedReturnsErrorWithoutFailover(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// Cross the large-request buffering threshold so converted preamble stays
	// pending until this fixture's terminal failure classification.
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"` + strings.Repeat("large prompt ", 6000) + `"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_failed","model":"gpt-5.5","status":"in_progress","output":[]}}`,
		"",
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"id":"resp_failed","object":"response","model":"gpt-5.5","status":"failed","output":[],"error":{"code":"upstream_error","message":"input exceeds the context window"}}}`,
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_failed_stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.5")
	require.Error(t, err)
	require.NotNil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.True(t, c.Writer.Written())
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.Contains(t, rec.Body.String(), "input exceeds the context window")
	require.NotContains(t, rec.Body.String(), "[DONE]")
}

// TestForwardAsChatCompletions_StreamBareErrorAfterOutputDoesNotFailOver
// follows a text delta with a bare error. It checks partial result, retained
// content and failure message, absent DONE and no provider failover wrapper.
func TestForwardAsChatCompletions_StreamBareErrorAfterOutputDoesNotFailOver(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}`,
		"",
		`event: error`,
		`data: {"type":"error","error":{"type":"server_error","code":"server_error","message":"temporary upstream failure"}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1, Name: "openai-oauth", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Concurrency: 1,
			Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.5")

	require.Error(t, err)
	require.NotNil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Contains(t, rec.Body.String(), "partial")
	require.Contains(t, rec.Body.String(), "temporary upstream failure")
	require.NotContains(t, rec.Body.String(), "[DONE]")
}

// TestForwardAsChatCompletions_StreamCyberPolicyNoFailover supplies a
// cyber-policy failed terminal after the Responses preamble. It checks the
// request risk marker, absence of failover wrapping and the client error/code
// and DONE fields.
func TestForwardAsChatCompletions_StreamCyberPolicyNoFailover(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// Cross the large-request buffering threshold so converted preamble stays
	// pending until this fixture's terminal failure classification.
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"` + strings.Repeat("large prompt ", 6000) + `"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_cyber","model":"gpt-5.5","status":"in_progress","output":[]}}`,
		"",
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"id":"resp_cyber","object":"response","model":"gpt-5.5","status":"failed","output":[],"error":{"code":"cyber_policy","message":"flagged for cyber policy"}}}`,
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_cyber"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	_, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.5")
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "cyber must NOT trigger failover")
	require.NotNil(t, GetOpsCyberPolicy(c), "cyber mark must be set")
	respBody := rec.Body.String()
	require.Contains(t, respBody, `"error"`)
	require.Contains(t, respBody, `"cyber_policy"`)
	require.Contains(t, respBody, "data: [DONE]")
}

// TestForwardAsChatCompletions_StreamsUsageWithoutClientStreamOptions supplies
// terminal usage despite no client stream_options field. It checks returned
// token quantities and their converted Chat usage fields in downstream output.
func TestForwardAsChatCompletions_StreamsUsageWithoutClientStreamOptions(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":13,"output_tokens":7,"total_tokens":20,"input_tokens_details":{"cached_tokens":5}}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_usage_no_stream_options"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.1")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 13, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 5, result.Usage.CacheReadInputTokens)

	responseBody := rec.Body.String()
	require.Contains(t, responseBody, `"usage"`)
	require.Contains(t, responseBody, `"prompt_tokens":13`)
	require.Contains(t, responseBody, `"completion_tokens":7`)
	require.Contains(t, responseBody, `"cached_tokens":5`)
}

// TestForwardAsChatCompletions_StreamsTopLevelTerminalUsage places usage
// beside the terminal response object in SSE. It checks returned input, output
// and cache observations plus their downstream Chat usage representation.
func TestForwardAsChatCompletions_StreamsTopLevelTerminalUsage(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_top","model":"gpt-5.4","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_top","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]},"usage":{"input_tokens":21,"output_tokens":9,"total_tokens":30,"input_tokens_details":{"cached_tokens":4}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_top_level_usage"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.1")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 21, result.Usage.InputTokens)
	require.Equal(t, 9, result.Usage.OutputTokens)
	require.Equal(t, 4, result.Usage.CacheReadInputTokens)

	responseBody := rec.Body.String()
	require.Contains(t, responseBody, `"usage"`)
	require.Contains(t, responseBody, `"prompt_tokens":21`)
	require.Contains(t, responseBody, `"completion_tokens":9`)
	require.Contains(t, responseBody, `"cached_tokens":4`)
}

// TestForwardAsChatCompletions_BufferedTopLevelTerminalUsage places usage
// beside the terminal Response for buffered conversion. It checks the observed
// token quantities and matching Chat JSON usage fields.
func TestForwardAsChatCompletions_BufferedTopLevelTerminalUsage(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_top_buffered","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]},"usage":{"input_tokens":18,"output_tokens":6,"total_tokens":24,"input_tokens_details":{"cached_tokens":3}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_buffered_top_level_usage"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.1")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 18, result.Usage.InputTokens)
	require.Equal(t, 6, result.Usage.OutputTokens)
	require.Equal(t, 3, result.Usage.CacheReadInputTokens)

	responseBody := rec.Body.String()
	require.Contains(t, responseBody, `"usage"`)
	require.Contains(t, responseBody, `"prompt_tokens":18`)
	require.Contains(t, responseBody, `"completion_tokens":6`)
	require.Contains(t, responseBody, `"cached_tokens":3`)
}

// TestForwardAsChatCompletions_TerminalUsageWithoutUpstreamCloseReturns blocks
// reads after a terminal and rejects client writes. It checks timely return
// and usage even though the fixture supplies no EOF before executor cleanup.
func TestForwardAsChatCompletions_TerminalUsageWithoutUpstreamCloseReturns(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Writer = &openAIChatFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := []byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":17,"output_tokens":8,"total_tokens":25,"input_tokens_details":{"cached_tokens":6}}}}` + "\n\n")
	// Keep reads blocked after terminal bytes to distinguish protocol completion
	// from EOF; deferred closure releases the fixture if the bounded wait fails.
	upstreamStream := gatewaytestkit.NewBlockingReadCloser(upstreamBody)
	defer func() {
		require.NoError(t, upstreamStream.Close())
	}()
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_terminal_no_close"}},
		Body:       upstreamStream,
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	// forwardResult carries one forwarding observation and error from the worker
	// to this test.
	type forwardResult struct {
		// result and err are captured by the same forwarding worker and published
		// together in one channel value, so assertions inspect the observation
		// associated with that operation's returned failure.
		// forwarding result observation
		result *forwardcore.OpenAIResult
		err    error // forwarding operation error
	}
	// Publish through a buffered result so the worker can finish even if the
	// wait times out; deferred source closure releases any remaining blocked read.
	resultCh := make(chan forwardResult, 1)
	go func() {
		result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.1")
		resultCh <- forwardResult{result: result, err: err}
	}()

	select {
	case got := <-resultCh:
		require.NoError(t, got.err)
		require.NotNil(t, got.result)
		require.Equal(t, 17, got.result.Usage.InputTokens)
		require.Equal(t, 8, got.result.Usage.OutputTokens)
		require.Equal(t, 6, got.result.Usage.CacheReadInputTokens)
	case <-time.After(time.Second):
		require.Fail(t, "ForwardAsChatCompletions should return after terminal usage event even if upstream keeps the connection open")
	}
}

// TestForwardAsChatCompletions_EventNamedTerminalWithoutUpstreamCloseReturns
// supplies event names when data omits type and then blocks reads. It checks
// timely completion, returned usage and converted text without requiring EOF
// from that blocking fixture.
func TestForwardAsChatCompletions_EventNamedTerminalWithoutUpstreamCloseReturns(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := []byte(strings.Join([]string{
		`event: response.created`,
		`data: {"response":{"id":"resp_1","model":"gpt-5.4","status":"in_progress","output":[]}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"delta":"ok"}`,
		``,
		`event: response.completed`,
		`data: {"response":{"id":"resp_1","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":17,"output_tokens":8,"total_tokens":25,"input_tokens_details":{"cached_tokens":6}}}}`,
		``,
		``,
	}, "\n"))
	// Keep reads blocked after terminal bytes to distinguish protocol completion
	// from EOF; deferred closure releases the fixture if the bounded wait fails.
	upstreamStream := gatewaytestkit.NewBlockingReadCloser(upstreamBody)
	defer func() {
		require.NoError(t, upstreamStream.Close())
	}()
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_event_named_terminal"}},
		Body:       upstreamStream,
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	// forwardResult carries one forwarding observation and error from the worker
	// to this test.
	type forwardResult struct {
		// result and err are captured by the same forwarding worker and published
		// together in one channel value, so assertions inspect the observation
		// associated with that operation's returned failure.
		// forwarding result observation
		result *forwardcore.OpenAIResult
		err    error // forwarding operation error
	}
	// Publish through a buffered result so the worker can finish even if the
	// wait times out; deferred source closure releases any remaining blocked read.
	resultCh := make(chan forwardResult, 1)
	go func() {
		result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.1")
		resultCh <- forwardResult{result: result, err: err}
	}()

	select {
	case got := <-resultCh:
		require.NoError(t, got.err)
		require.NotNil(t, got.result)
		require.Equal(t, 17, got.result.Usage.InputTokens)
		require.Equal(t, 8, got.result.Usage.OutputTokens)
		require.Equal(t, 6, got.result.Usage.CacheReadInputTokens)
		require.Contains(t, rec.Body.String(), `"content":"ok"`)
	case <-time.After(time.Second):
		require.Fail(t, "ForwardAsChatCompletions should use SSE event names when data payloads omit type")
	}
}

// TestForwardAsChatCompletions_EventTypeDoesNotLeakAcrossFrames mixes named
// and data-typed SSE frames across blank boundaries. It checks successful text
// forwarding and DONE after the fixture terminal.
func TestForwardAsChatCompletions_EventTypeDoesNotLeakAcrossFrames(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`event: response.created`,
		`data: {"response":{"id":"resp_1","model":"gpt-5.4","status":"in_progress","output":[]}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		``,
		`event: response.completed`,
		`data: {"response":{"id":"resp_1","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":17,"output_tokens":8,"total_tokens":25,"input_tokens_details":{"cached_tokens":6}}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_event_boundary"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.1")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"content":"ok"`)
	require.Contains(t, rec.Body.String(), `data: [DONE]`)
}

// TestForwardAsChatCompletions_BufferedTerminalWithoutUpstreamCloseReturns
// blocks source reads after a completed terminal for nonstreaming Chat. It
// checks timely return, observed usage and a stop finish reason in buffered
// output.
func TestForwardAsChatCompletions_BufferedTerminalWithoutUpstreamCloseReturns(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := []byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":17,"output_tokens":8,"total_tokens":25,"input_tokens_details":{"cached_tokens":6}}}}` + "\n\n")
	// Keep reads blocked after terminal bytes to distinguish protocol completion
	// from EOF; deferred closure releases the fixture if the bounded wait fails.
	upstreamStream := gatewaytestkit.NewBlockingReadCloser(upstreamBody)
	defer func() {
		require.NoError(t, upstreamStream.Close())
	}()
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_buffered_terminal_no_close"}},
		Body:       upstreamStream,
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	// forwardResult carries one forwarding observation and error from the worker
	// to this test.
	type forwardResult struct {
		// result and err are captured by the same forwarding worker and published
		// together in one channel value, so assertions inspect the observation
		// associated with that operation's returned failure.
		// forwarding result observation
		result *forwardcore.OpenAIResult
		err    error // forwarding operation error
	}
	// Publish through a buffered result so the worker can finish even if the
	// wait times out; deferred source closure releases any remaining blocked read.
	resultCh := make(chan forwardResult, 1)
	go func() {
		result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.1")
		resultCh <- forwardResult{result: result, err: err}
	}()

	select {
	case got := <-resultCh:
		require.NoError(t, got.err)
		require.NotNil(t, got.result)
		require.Equal(t, 17, got.result.Usage.InputTokens)
		require.Equal(t, 8, got.result.Usage.OutputTokens)
		require.Equal(t, 6, got.result.Usage.CacheReadInputTokens)
		require.Contains(t, rec.Body.String(), `"finish_reason":"stop"`)
	case <-time.After(time.Second):
		require.Fail(t, "ForwardAsChatCompletions buffered response should return after terminal usage event even if upstream keeps the connection open")
	}
}

// TestForwardAsChatCompletions_DoneSentinelWithoutTerminalReturnsError
// supplies only the DONE sentinel to converted Chat. It checks a
// missing-terminal error with a returned observation whose input and output
// counts are zero.
func TestForwardAsChatCompletions_DoneSentinelWithoutTerminalReturnsError(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := "data: [DONE]\n\n"
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_missing_terminal"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing terminal event")
	require.NotNil(t, result)
	require.Zero(t, result.Usage.InputTokens)
	require.Zero(t, result.Usage.OutputTokens)
}

// TestForwardAsChatCompletions_UpstreamRequestIgnoresClientCancel cancels the
// client before converted forwarding and supplies a completed fixture
// response. It checks success and an uncancelled context on the captured
// request.
func TestForwardAsChatCompletions_UpstreamRequestIgnoresClientCancel(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	reqCtx, cancel := context.WithCancel(context.Background())
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(reqCtx)
	c.Request.Header.Set("Content-Type", "application/json")
	// Cancel before conversion to inspect detachment on the captured upstream
	// request. The fixture supplies a completed response independently of that
	// client context.
	cancel()

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_ctx"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":       "oauth-token",
				"chatgpt_account_id": "chatgpt-acc",
			},
		},
	}

	result, err := svc.Text.Chat(reqCtx, c, provider, body, "", "gpt-5.1")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.NoError(t, upstream.lastReq.Context().Err())
}

// TestForwardGrokChatViaResponsesNonStreamingCachesAndReturnsChat bridges a
// cached Grok Chat request into Responses. It checks isolated cache/header
// identity, request tools and shape, quota snapshot, observed usage and
// buffered Chat answer.
func TestForwardGrokChatViaResponsesNonStreamingCachesAndReturnsChat(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hi"}],"stream":false,"prompt_cache_key":"stable-session","tools":[],"functions":null,"tool_choice":"none"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grok.GrokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 7101})

	provider := grokChatBridgeTestProvider(71)
	repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}}
	upstream := &auxiliaryHTTPRecorder{resp: grokChatBridgeCompletedResponse("resp_grok_chat_cache", 9856)}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, grok.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, grok.GrokChatResponsesEndpoint, result.UpstreamEndpoint)
	require.Equal(t, "grok-4.5", result.BillingModel)
	require.Equal(t, "grok-4.5", result.UpstreamModel)
	require.Equal(t, 9908, result.Usage.InputTokens)
	require.Equal(t, 12, result.Usage.OutputTokens)
	require.Equal(t, 9856, result.Usage.CacheReadInputTokens)

	identity := gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String()
	require.NotEmpty(t, identity)
	require.NotEqual(t, "stable-session", identity)
	require.Equal(t, identity, upstream.lastReq.Header.Get(GrokConversationIDHeader))
	require.Equal(t, "web_search", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(upstream.lastBody, "tools.1.type").String())
	require.Equal(t, "none", gjson.GetBytes(upstream.lastBody, "tool_choice").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "system", gjson.GetBytes(upstream.lastBody, "input.0.role").String())
	require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "input.1.role").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "instructions").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "include").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Exists())

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "cached ok", gjson.Get(recorder.Body.String(), "choices.0.message.content").String())
	require.Equal(t, int64(9856), gjson.Get(recorder.Body.String(), "usage.prompt_tokens_details.cached_tokens").Int())
	require.NotNil(t, repo.updates[provider.Record.ID]["grok_usage_snapshot"])
}

// TestForwardGrokChatViaResponsesNonStreamingRejectsCompletedResponseWithoutUsage
// supplies successful Grok content with no terminal usage. It checks
// missing-usage recovery, copied request identity and absence of committed
// buffered client output.
func TestForwardGrokChatViaResponsesNonStreamingRejectsCompletedResponseWithoutUsage(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}],"stream":false,"prompt_cache_key":"stable-session"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grok.GrokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 7102})

	provider := grokChatBridgeTestProvider(72)
	repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}}
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_missing_usage","object":"response","model":"grok-4.5","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}}`,
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "Xai-Request-Id": []string{"rid-responses-missing-usage"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	service := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := service.Text.Chat(context.Background(), c, provider, body, "", "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, "grok_missing_usage", gjson.GetBytes(failoverErr.ResponseBody, "error.code").String())
	require.Equal(t, "rid-responses-missing-usage", http.Header(failoverErr.ResponseHeaders).Get("x-request-id"))
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
}

// TestForwardGrokChatImageWithoutCacheIdentityUsesResponses supplies image
// content without a cache seed. It checks native Responses dispatch, converted
// text/image parts and absent cache-key and conversation-header values.
func TestForwardGrokChatImageWithoutCacheIdentityUsesResponses(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QQ=="}}]}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grok.GrokChatRawEndpoint, bytes.NewReader(body))

	provider := grokChatBridgeTestProvider(711)
	repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}}
	upstream := &auxiliaryHTTPRecorder{resp: grokChatBridgeCompletedResponse("resp_grok_chat_image", 0)}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, grok.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, grok.GrokChatResponsesEndpoint, result.UpstreamEndpoint)
	require.Equal(t, "input_text", gjson.GetBytes(upstream.lastBody, "input.0.content.0.type").String())
	require.Equal(t, "what is this", gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())
	require.Equal(t, "input_image", gjson.GetBytes(upstream.lastBody, "input.0.content.1.type").String())
	require.Equal(t, "data:image/png;base64,QQ==", gjson.GetBytes(upstream.lastBody, "input.0.content.1.image_url").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
	require.Empty(t, upstream.lastReq.Header.Get(GrokConversationIDHeader))
}

// TestForwardGrokChatViaResponsesCodeBuddyUsesStableConversationHeader changes
// request/message IDs and tool content across one CodeBuddy conversation. It
// checks the same derived identity and its outgoing cache/header values for
// both turns.
func TestForwardGrokChatViaResponsesCodeBuddyUsesStableConversationHeader(t *testing.T) {
	const conversationID = "codebuddy-session-42"
	tests := []struct {
		name      string // fixture case name
		requestID string // changing request identifier
		messageID string // changing message identifier
		body      []byte // client request body
	}{
		{
			name:      "first turn",
			requestID: "request-one",
			messageID: "message-one",
			body:      []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"first question"}],"stream":false,"tools":[]}`),
		},
		{
			name:      "later turn with tools",
			requestID: "request-two",
			messageID: "message-two",
			body:      []byte(`{"model":"grok-4.5","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"different later question"}],"stream":false,"tools":[{"type":"function","function":{"name":"lookup","description":"Lookup a value","parameters":{"type":"object","properties":{"key":{"type":"string"}}}}}],"tool_choice":"auto"}`),
		},
	}

	var stableIdentity string
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, grok.GrokChatRawEndpoint, bytes.NewReader(tt.body))
			c.Request.Header.Set(CodeBuddyConversationHeader, conversationID)
			c.Request.Header.Set("X-Conversation-Request-ID", tt.requestID)
			c.Request.Header.Set("X-Conversation-Message-ID", tt.messageID)
			c.Request.Header.Set("X-Request-ID", "generic-"+tt.requestID)
			c.Set("api_key", &apikey.APIKey{ID: 7111})

			identity := ResolveGrokCacheIdentity(c, tt.body, "", "grok-4.5")
			require.NotEmpty(t, identity)
			if index == 0 {
				stableIdentity = identity
			} else {
				require.Equal(t, stableIdentity, identity)
			}
			require.NotContains(t, identity, conversationID)

			provider := grokChatBridgeTestProvider(int64(711 + index))
			repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
				providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
			}}
			upstream := &auxiliaryHTTPRecorder{resp: grokChatBridgeCompletedResponse("resp_codebuddy_"+strconv.Itoa(index), 4096)}
			svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

			result, err := svc.Text.Chat(context.Background(), c, provider, tt.body, "", "")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, grok.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
			require.Equal(t, grok.GrokChatResponsesEndpoint, result.UpstreamEndpoint)
			require.Equal(t, identity, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
			require.Equal(t, identity, upstream.lastReq.Header.Get(GrokConversationIDHeader))
		})
	}
}

// TestForwardGrokChatViaResponsesTraeToolHistoryKeepsCacheRoute appends
// tool-call history to a free-tier Grok cache request. It checks stable
// identity, Responses dispatch and retained function declarations, control
// flags and paired tool input/output.
func TestForwardGrokChatViaResponsesTraeToolHistoryKeepsCacheRoute(t *testing.T) {
	firstTurnBody := []byte(`{"model":"grok-4.5","messages":[{"role":"system","content":"Be concise"},{"role":"user","content":"Find alpha"}],"stream":false,"prompt_cache_key":"trae-session","tools":[{"type":"function","function":{"name":"lookup","description":"Lookup a value","parameters":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]},"strict":false}}],"tool_choice":"auto","parallel_tool_calls":true}`)
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"system","content":"Be concise"},{"role":"user","content":"Find alpha"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_lookup","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"alpha\"}"}}]},{"role":"tool","tool_call_id":"call_lookup","content":"{\"value\":\"ok\"}"},{"role":"user","content":"Summarize"}],"stream":false,"prompt_cache_key":"trae-session","tools":[{"type":"function","function":{"name":"lookup","description":"Lookup a value","parameters":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]},"strict":false}}],"tool_choice":"auto","parallel_tool_calls":true}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grok.GrokChatRawEndpoint, bytes.NewReader(body))
	c.Request.Header.Set("X-TokenRouter-Grok-Client-Tool-Cache", "prefer-cache")
	c.Set("api_key", &apikey.APIKey{ID: 7151})

	provider := grokChatBridgeTestProvider(715)
	provider.Record.Credentials["subscription_tier"] = "free"
	repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}}
	upstream := &auxiliaryHTTPRecorder{resp: grokChatBridgeCompletedResponse("resp_grok_chat_trae", 8192)}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	firstTurnIdentity := ResolveGrokCacheIdentity(c, firstTurnBody, "", "grok-4.5")
	extendedTurnIdentity := ResolveGrokCacheIdentity(c, body, "", "grok-4.5")
	require.NotEmpty(t, firstTurnIdentity)
	require.Equal(t, firstTurnIdentity, extendedTurnIdentity)

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, grok.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, grok.GrokChatResponsesEndpoint, result.UpstreamEndpoint)
	require.Equal(t, extendedTurnIdentity, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, extendedTurnIdentity, upstream.lastReq.Header.Get(GrokConversationIDHeader))

	tools := gjson.GetBytes(upstream.lastBody, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "function", tools[0].Get("type").String())
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "Lookup a value", tools[0].Get("description").String())
	require.Equal(t, "string", tools[0].Get("parameters.properties.key.type").String())
	require.True(t, tools[0].Get("strict").Exists())
	require.False(t, tools[0].Get("strict").Bool())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
	require.Equal(t, "auto", gjson.GetBytes(upstream.lastBody, "tool_choice").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "parallel_tool_calls").Bool())

	require.Equal(t, "function_call", gjson.GetBytes(upstream.lastBody, "input.2.type").String())
	require.Equal(t, "call_lookup", gjson.GetBytes(upstream.lastBody, "input.2.call_id").String())
	require.Equal(t, "lookup", gjson.GetBytes(upstream.lastBody, "input.2.name").String())
	require.Equal(t, `{"key":"alpha"}`, gjson.GetBytes(upstream.lastBody, "input.2.arguments").String())
	require.Equal(t, "function_call_output", gjson.GetBytes(upstream.lastBody, "input.3.type").String())
	require.Equal(t, "call_lookup", gjson.GetBytes(upstream.lastBody, "input.3.call_id").String())
	require.Equal(t, `{"value":"ok"}`, gjson.GetBytes(upstream.lastBody, "input.3.output").String())
}

// TestForwardGrokChatViaResponsesTraeCompatibilityFieldsKeepCacheRoute
// supplies JSON format, instructions, effort placeholders and tool reasoning
// history. It checks cache stability, transformed compatibility fields,
// retained tool pairing and cached-token output.
func TestForwardGrokChatViaResponsesTraeCompatibilityFieldsKeepCacheRoute(t *testing.T) {
	firstTurnBody := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"Find alpha"}],"instructions":"Return concise JSON","stream":false,"response_format":{"type":"json_object"},"service_tier":"fast","stop":null,"reasoning_effort":null,"tools":[{"type":"function","function":{"name":"lookup","description":"Lookup a value","parameters":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}}}],"tool_choice":"auto","parallel_tool_calls":true}`)
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"Find alpha"},{"role":"assistant","content":null,"reasoning_content":"I should use lookup","tool_calls":[{"index":0,"id":"call_lookup","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"alpha\"}"}}]},{"role":"tool","tool_call_id":"call_lookup","content":"{\"value\":\"ok\"}"},{"role":"user","content":"Summarize"}],"instructions":"Return concise JSON","stream":false,"response_format":{"type":"json_object"},"service_tier":"fast","stop":null,"reasoning_effort":null,"tools":[{"type":"function","function":{"name":"lookup","description":"Lookup a value","parameters":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}}}],"tool_choice":"auto","parallel_tool_calls":true}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grok.GrokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 7161})

	provider := grokChatBridgeTestProvider(716)
	provider.Record.Credentials["subscription_tier"] = "free"
	repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}}
	upstream := &auxiliaryHTTPRecorder{resp: grokChatBridgeCompletedResponse("resp_grok_chat_trae_compat", 12288)}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	firstTurnIdentity := ResolveGrokCacheIdentity(c, firstTurnBody, "", "grok-4.5")
	extendedTurnIdentity := ResolveGrokCacheIdentity(c, body, "", "grok-4.5")
	require.NotEmpty(t, firstTurnIdentity)
	require.Equal(t, firstTurnIdentity, extendedTurnIdentity)

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, grok.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, grok.GrokChatResponsesEndpoint, result.UpstreamEndpoint)
	require.Equal(t, 12288, result.Usage.CacheReadInputTokens)
	require.Equal(t, extendedTurnIdentity, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, extendedTurnIdentity, upstream.lastReq.Header.Get(GrokConversationIDHeader))
	require.Equal(t, "Return concise JSON", gjson.GetBytes(upstream.lastBody, "instructions").String())
	require.Equal(t, "json_object", gjson.GetBytes(upstream.lastBody, "text.format.type").String())
	require.Equal(t, "priority", gjson.GetBytes(upstream.lastBody, "service_tier").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "stop").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning").Exists())
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "input.1.content").String(), "<thinking>I should use lookup</thinking>")
	require.Equal(t, "function_call", gjson.GetBytes(upstream.lastBody, "input.2.type").String())
	require.Equal(t, "function_call_output", gjson.GetBytes(upstream.lastBody, "input.3.type").String())

	tools := gjson.GetBytes(upstream.lastBody, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
	require.Equal(t, int64(12288), gjson.Get(recorder.Body.String(), "usage.prompt_tokens_details.cached_tokens").Int())
}

// TestForwardGrokChatViaResponsesStreamingPropagatesCachedUsage bridges Grok
// SSE to Chat with cached usage. It checks the Responses endpoint, stream
// result, converted text, cached count and DONE marker.
func TestForwardGrokChatViaResponsesStreamingPropagatesCachedUsage(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grok.GrokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 7201})

	provider := grokChatBridgeTestProvider(72)
	repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}}
	upstream := &auxiliaryHTTPRecorder{resp: grokChatBridgeCompletedResponse("resp_grok_chat_stream", 4096)}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, grok.GrokChatResponsesEndpoint, result.UpstreamEndpoint)
	require.Equal(t, 4096, result.Usage.CacheReadInputTokens)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, recorder.Body.String(), `"content":"cached ok"`)
	require.Contains(t, recorder.Body.String(), `"cached_tokens":4096`)
	require.Contains(t, recorder.Body.String(), "data: [DONE]")
}

// TestForwardGrokChatRuntimeGateFallsBackToRaw compares missing cache identity
// and a non-cache-capable mapped model. It checks native Chat dispatch,
// selected model, absent injected tools and buffered answer for each fixture.
func TestForwardGrokChatRuntimeGateFallsBackToRaw(t *testing.T) {
	tests := []struct {
		name         string // fixture case name
		setAPIKey    bool   // bind downstream API key
		mappedModel  string // configured mapped model
		wantUpstream string // expected upstream model
	}{
		{name: "missing cache identity", wantUpstream: "grok-4.5"},
		{name: "non cache capable mapped model", setAPIKey: true, mappedModel: "grok-4.3", wantUpstream: "grok-4.3"},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}],"stream":false}`)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, grok.GrokChatRawEndpoint, bytes.NewReader(body))
			if tt.setAPIKey {
				c.Set("api_key", &apikey.APIKey{ID: int64(7301 + index)})
			}

			provider := grokChatBridgeTestProvider(int64(73 + index))
			if tt.mappedModel != "" {
				provider.Record.Credentials["model_mapping"] = map[string]any{"grok-4.5": tt.mappedModel}
			}
			repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
				providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
			}}
			upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"chat_raw","object":"chat.completion","model":"` + tt.wantUpstream + `","choices":[{"index":0,"message":{"role":"assistant","content":"raw ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
				)),
			}}
			svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

			result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, grok.DefaultCLIBaseURL+"/chat/completions", upstream.lastReq.URL.String())
			require.Equal(t, grok.GrokChatRawEndpoint, result.UpstreamEndpoint)
			require.Equal(t, tt.wantUpstream, result.UpstreamModel)
			require.False(t, gjson.GetBytes(upstream.lastBody, "tools").Exists())
			require.Equal(t, "raw ok", gjson.Get(recorder.Body.String(), "choices.0.message.content").String())
		})
	}
}

// TestForwardGrokChatViaResponses429UsesGrokRateLimitPolicy returns HTTP 429
// with a forty-five-second retry delay. It checks typed recovery, rate-limit
// storage and runtime block, retained delay and the actual Responses endpoint.
func TestForwardGrokChatViaResponses429UsesGrokRateLimitPolicy(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grok.GrokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 7501})

	provider := grokChatBridgeTestProvider(75)
	repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Retry-After":  []string{"45"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})
	before := time.Now()

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Equal(t, "45", http.Header(failoverErr.ResponseHeaders).Get("Retry-After"))
	require.Equal(t, grok.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, grok.GrokChatResponsesEndpoint, GetActualOpenAIUpstreamEndpoint(c))
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Zero(t, repo.tempUnschedCalls)
	require.WithinDuration(t, before.Add(45*time.Second), repo.lastRateLimitResetAt, time.Second)
	require.True(t, httpFixtureRuntimeBlocked(svc, provider))
}

// TestForwardGrokRawChat429PreservesRetryAfter forces native Chat with a stop
// control and returns HTTP 429. It checks the raw endpoint and retained retry
// delay in the typed failure.
func TestForwardGrokRawChat429PreservesRetryAfter(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}],"stream":false,"stop":"done"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grok.GrokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 7551})

	provider := grokChatBridgeTestProvider(755)
	provider.Record.Credentials["expires_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Retry-After":  []string{"45"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")

	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Equal(t, "45", http.Header(failoverErr.ResponseHeaders).Get("Retry-After"))
	require.Equal(t, grok.DefaultCLIBaseURL+"/chat/completions", upstream.lastReq.URL.String())
}

// TestForwardGrokRawChatErrorRecordsActualEndpoint forces native Chat with
// stop and returns a request rejection. It checks an error with absent result
// and matching captured and observed Chat endpoints.
func TestForwardGrokRawChatErrorRecordsActualEndpoint(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}],"stream":false,"stop":"done"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grok.GrokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 7601})

	provider := grokChatBridgeTestProvider(76)
	repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad request"}}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, grok.DefaultCLIBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, grok.GrokChatRawEndpoint, GetActualOpenAIUpstreamEndpoint(c))
}

// grokChatBridgeTestProvider creates an active Grok OAuth target for the
// supplied ID.
func grokChatBridgeTestProvider(id int64) *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id,
			Name:        "grok-cache-bridge",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Status:      billingcore.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
				"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
				"base_url":      grok.DefaultCLIBaseURL,
			},
		},
	}
}

// grokChatBridgeCompletedResponse constructs the fixture SSE terminal with
// supplied cache usage and response identifier. Callers provide an identifier
// safe for direct JSON interpolation and close the returned body after using
// this response.
func grokChatBridgeCompletedResponse(responseID string, cachedTokens int) *http.Response {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"cached ok"}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"` + responseID + `","object":"response","model":"grok-4.5","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"cached ok"}]}],"usage":{"input_tokens":9908,"output_tokens":12,"total_tokens":9920,"input_tokens_details":{"cached_tokens":` + strconv.Itoa(cachedTokens) + `}}}}`,
		"",
	}, "\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"Xai-Request-Id":                 []string{responseID + "-request"},
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"9"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

// TestForwardGrokChatViaResponsesDropsRedundantViewImage bridges inline image
// Chat input to Grok Responses. It checks the converted image part and
// outgoing tool policy through the shared inline-image assertions.
func TestForwardGrokChatViaResponsesDropsRedundantViewImage(t *testing.T) {
	body := grokInlineImageChatRequest()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 7991})

	provider := grokChatBridgeTestProvider(799)
	repo := &grokQuotaProviderRepo{grokFixtureProviders: &grokFixtureProviders{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}}
	upstream := &auxiliaryHTTPRecorder{resp: grokChatBridgeCompletedResponse("resp_chat_image", 0)}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, grok.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "input_image", gjson.GetBytes(upstream.lastBody, "input.0.content.1.type").String())
	assertGrokInlineImageTools(t, upstream.lastBody, "tools.#(name==\"%s\")")
}

// TestForwardGrokRawChatDropsRedundantViewImage forwards inline image input to
// a configured Grok API-key Chat endpoint. It checks native image
// representation and outgoing tool policy through the shared assertions.
func TestForwardGrokRawChatDropsRedundantViewImage(t *testing.T) {
	body := grokInlineImageChatRequest()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 800, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "test-key", "base_url": "https://grok.example.test/v1"},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl","object":"chat.completion","model":"grok-4.6","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://grok.example.test/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "image_url", gjson.GetBytes(upstream.lastBody, "messages.0.content.1.type").String())
	assertGrokInlineImageTools(t, upstream.lastBody, "tools.#(function.name==\"%s\")")
}

// grokInlineImageChatRequest creates Chat image input with view_image and
// shell tools.
func grokInlineImageChatRequest() []byte {
	return []byte(`{
		"model":"grok-4.6",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"What text is in this image?"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}
		]}],
		"stream":false,
		"tools":[
			{"type":"function","function":{"name":"view_image","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}},
			{"type":"function","function":{"name":"shell_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}}
		],
		"tool_choice":"auto"
	}`)
}

// TestForwardAsChatCompletionsForGrokStopFallsBackToXAIChatCompletions
// supplies stop and a raw cache seed to Grok OAuth. It checks raw dispatch,
// isolated conversation identity, stripped cache field, usage and quota
// snapshot.
func TestForwardAsChatCompletionsForGrokStopFallsBackToXAIChatCompletions(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}],"stream":false,"stop":"done","prompt_cache_key":"raw-client-cache-key"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Set("api_key", &apikey.APIKey{ID: 5101})

	provider := gatewaytestkit.HealthyGrokOAuthProvider(51, "access-token")
	repo := &grokQuotaProviderRepo{
		grokFixtureProviders: &grokFixtureProviders{
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{51: provider},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"application/json"},
			"Xai-Request-Id":                 []string{"xai-req"},
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"9"},
			"X-Ratelimit-Limit-Tokens":       []string{"1000"},
			"X-Ratelimit-Remaining-Tokens":   []string{"990"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl","object":"chat.completion","model":"grok-4.3","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":1}}}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), transport: upstream, providers: repo})

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.NoError(t, err)
	require.Equal(t, grok.DefaultCLIBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.NotEmpty(t, upstream.lastReq.Header.Get(GrokConversationIDHeader))
	require.NotEqual(t, "raw-client-cache-key", upstream.lastReq.Header.Get(GrokConversationIDHeader))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
	require.Equal(t, "grok-4.5", result.Model)
	require.Equal(t, "grok-4.5", result.UpstreamModel)
	require.Equal(t, 1, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
	require.NotNil(t, repo.updates[51]["grok_usage_snapshot"])
	require.Equal(t, http.StatusOK, recorder.Code)
}

// TestForwardAsChatCompletionsForGrokAPIKeyUsesConfiguredRawEndpointWithoutOAuthIdentity
// supplies a configured Grok API-key base. It checks the native Chat URL,
// bearer key and the fixture absence of OAuth CLI identity headers.
func TestForwardAsChatCompletionsForGrokAPIKeyUsesConfiguredRawEndpointWithoutOAuthIdentity(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 706,
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "third-party-key",
				"base_url": "https://grok.example.test/v1",
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl","object":"chat.completion","model":"grok-4.5","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	_, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.NoError(t, err)
	require.Equal(t, "https://grok.example.test/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer third-party-key", upstream.lastReq.Header.Get("Authorization"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.NotEqual(t, grok.DefaultGrokUpstreamUserAgent(), upstream.lastReq.Header.Get("User-Agent"))
}

// TestForwardAsChatCompletionsForGrokAPIKeyRejectsNonStreamingResponseWithoutUsage
// supplies completed native Chat content without usage. It checks
// missing-usage recovery, retained upstream request ID and no client
// commitment or body.
func TestForwardAsChatCompletionsForGrokAPIKeyRejectsNonStreamingResponseWithoutUsage(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 707,
			Name:        "grok-api-key",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "third-party-key",
				"base_url": "https://grok.example.test/v1",
			},
		},
	}
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "Xai-Request-Id": []string{"rid-grok-raw-missing-usage"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_missing_usage","object":"chat.completion","model":"grok-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		)),
	}}
	service := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := service.Text.Chat(context.Background(), c, provider, body, "", "")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, "grok_missing_usage", gjson.GetBytes(failoverErr.ResponseBody, "error.code").String())
	require.Equal(t, "rid-grok-raw-missing-usage", http.Header(failoverErr.ResponseHeaders).Get("x-request-id"))
	require.False(t, c.Writer.Written())
	require.Empty(t, recorder.Body.String())
}

// TestForwardAsChatCompletionsForGrokStreamingUsesRawXAIChatCompletions
// supplies an OAuth Grok stream without a cache identity. It checks raw
// dispatch, CLI headers, usage injection, returned usage, downstream DONE and
// quota snapshot.
func TestForwardAsChatCompletionsForGrokStreamingUsesRawXAIChatCompletions(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	provider := gatewaytestkit.HealthyGrokOAuthProvider(53, "access-token")
	repo := &grokQuotaProviderRepo{
		grokFixtureProviders: &grokFixtureProviders{
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{53: provider},
		},
	}
	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_grok","object":"chat.completion.chunk","model":"grok-4.3","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		"",
		`data: {"id":"chatcmpl_grok","object":"chat.completion.chunk","model":"grok-4.3","choices":[],"usage":{"prompt_tokens":6,"completion_tokens":4,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":1}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"X-Request-Id":                   []string{"chat-stream-req"},
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"7"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), options: protocolHTTPOptions(), transport: upstream, providers: repo})

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.NoError(t, err)
	require.Equal(t, grok.DefaultCLIBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, grok.CLIUserAgent(grok.CLIClientVersion), upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
	require.True(t, result.Stream)
	require.Equal(t, 6, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
	require.Contains(t, recorder.Body.String(), "data: [DONE]")
	require.NotNil(t, repo.updates[53]["grok_usage_snapshot"])
}

// TestForwardAsChatCompletionsForGrokStreamingStopFallsBackToRawXAIChatCompletions
// supplies stop and a native conversation header on a keyed stream. It checks
// raw CLI dispatch, rewritten conversation identity, usage/DONE output and
// quota snapshot.
func TestForwardAsChatCompletionsForGrokStreamingStopFallsBackToRawXAIChatCompletions(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}],"stream":true,"stop":"done"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set(GrokConversationIDHeader, "native-client-conversation")
	c.Set("api_key", &apikey.APIKey{ID: 5301})

	provider := gatewaytestkit.HealthyGrokOAuthProvider(53, "access-token")
	repo := &grokQuotaProviderRepo{
		grokFixtureProviders: &grokFixtureProviders{
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{53: provider},
		},
	}
	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_grok","object":"chat.completion.chunk","model":"grok-4.3","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		"",
		`data: {"id":"chatcmpl_grok","object":"chat.completion.chunk","model":"grok-4.3","choices":[],"usage":{"prompt_tokens":6,"completion_tokens":4,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":1}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"X-Request-Id":                   []string{"chat-stream-req"},
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"7"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), options: protocolHTTPOptions(), transport: upstream, providers: repo})

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.NoError(t, err)
	require.Equal(t, grok.DefaultCLIBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, grok.CLIUserAgent(grok.CLIClientVersion), upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, grok.CLIClientVersion, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.NotEmpty(t, upstream.lastReq.Header.Get(GrokConversationIDHeader))
	require.NotEqual(t, "native-client-conversation", upstream.lastReq.Header.Get(GrokConversationIDHeader))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
	require.True(t, result.Stream)
	require.Equal(t, 6, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
	require.Contains(t, recorder.Body.String(), "data: [DONE]")
	require.NotNil(t, repo.updates[53]["grok_usage_snapshot"])
}

// TestForwardAsChatCompletionsForGrokComposerBridgesImageInput describes one
// image before a Composer Chat request. It checks two endpoints, description
// replacement, exact large metadata number, separate conversation identity and
// summed usage.
func TestForwardAsChatCompletionsForGrokComposerBridgesImageInput(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-composer-2.5-fast","messages":[{"role":"system","content":"You are concise."},{"role":"user","content":[{"type":"text","text":"What is shown?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}],"metadata":{"large_id":9007199254740993},"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &apikey.APIKey{ID: 5501})

	provider := gatewaytestkit.HealthyGrokOAuthProvider(55, "access-token")
	repo := &grokQuotaProviderRepo{
		grokFixtureProviders: &grokFixtureProviders{
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{55: provider},
		},
	}
	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "xai-request-id": []string{"vision-req"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_vision","object":"response","model":"grok-build-0.1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"A small diagram with ABC letters."}]}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":                   []string{"application/json"},
				"X-Request-Id":                   []string{"composer-req"},
				"X-Ratelimit-Limit-Requests":     []string{"10"},
				"X-Ratelimit-Remaining-Requests": []string{"9"},
				"X-Ratelimit-Limit-Tokens":       []string{"1000"},
				"X-Ratelimit-Remaining-Tokens":   []string{"980"},
			},
			Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl_composer","object":"chat.completion","model":"grok-composer-2.5-fast","choices":[{"index":0,"message":{"role":"assistant","content":"It shows ABC."},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`)),
		},
	}}
	svc := newResponsesFixture(responsesFixtureInputs{grokTokens: newHTTPGrokTokenFixture(repo, nil), options: protocolHTTPOptions(), transport: upstream, providers: repo})

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, grok.DefaultCLIBaseURL+"/responses", upstream.requests[0].URL.String())
	require.Empty(t, upstream.requests[0].Header.Get(GrokConversationIDHeader))
	require.Equal(t, "grok-build-0.1", gjson.GetBytes(upstream.bodies[0], "model").String())
	require.Equal(t, "input_image", gjson.GetBytes(upstream.bodies[0], "input.0.content.1.type").String())
	require.Equal(t, grok.DefaultCLIBaseURL+"/chat/completions", upstream.requests[1].URL.String())
	require.NotEmpty(t, upstream.requests[1].Header.Get(GrokConversationIDHeader))
	require.Equal(t, "grok-composer-2.5-fast", gjson.GetBytes(upstream.bodies[1], "model").String())
	require.False(t, strings.Contains(string(upstream.bodies[1]), "image_url"))
	require.Equal(t, "9007199254740993", gjson.GetBytes(upstream.bodies[1], "metadata.large_id").Raw)
	require.Contains(t, gjson.GetBytes(upstream.bodies[1], "messages.1.content").String(), "Image 1 description")
	require.Contains(t, gjson.GetBytes(upstream.bodies[1], "messages.1.content").String(), "A small diagram with ABC letters.")
	require.Equal(t, 14, result.Usage.InputTokens)
	require.Equal(t, 12, result.Usage.OutputTokens)
	require.Equal(t, "It shows ABC.", gjson.Get(recorder.Body.String(), "choices.0.message.content").String())
	require.NotNil(t, repo.updates[55]["grok_usage_snapshot"])
}

// forcedResponsesChatTestProvider creates an API-key target forcing Chat
// through Responses conversion.
func forcedResponsesChatTestProvider() *gatewayprovider.ExecutionProvider {
	provider := rawChatCompletionsTestProvider()
	provider.Record.Extra = map[string]any{"openai_text_route_mode": "force_responses"}
	return provider
}

// TestForwardAsChatCompletions_ResponseFailed_PassthroughRule applies a
// configured override to buffered context-window failure. It checks HTTP 400,
// upstream_error type and the retained context-window message after rule
// matching.
func TestForwardAsChatCompletions_ResponseFailed_PassthroughRule(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	bindPassthroughRule(c, "openai", []string{"context_length_exceeded"}, 400)

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(buildContextLengthFailedSSE())),
	}}
	svc := newWSFixture(wsFixtureInputs{options: rawChatCompletionsTestConfig(), transport: upstream})

	provider := forcedResponsesChatTestProvider()
	_, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "passthrough")
	require.Equal(t, 400, rec.Code, "passthrough rule should override 502 to 400")

	respBody := rec.Body.String()
	errType := gjson.Get(respBody, "error.type").String()
	require.Equal(t, "upstream_error", errType)
	errMsg := gjson.Get(respBody, "error.message").String()
	require.NotEmpty(t, errMsg, "passthrough should preserve error message")
	require.Contains(t, errMsg, "context window")
}

// TestForwardAsChatCompletions_ResponseFailed_NoRule_Still502 supplies the
// same buffered context-window failure with no presentation rule. It checks a
// returned error and the default HTTP 502 response.
func TestForwardAsChatCompletions_ResponseFailed_NoRule_Still502(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(buildContextLengthFailedSSE())),
	}}
	svc := newWSFixture(wsFixtureInputs{options: rawChatCompletionsTestConfig(), transport: upstream})

	provider := forcedResponsesChatTestProvider()
	_, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")

	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code, "without passthrough rule should still be 502")
}

// TestForwardAsChatCompletions_ResponseFailedCustomErrorMissReturnsGeneric500
// excludes the event status from configured provider error codes. It checks
// generic HTTP 500, no failover wrapper and no persistent error-store call.
func TestForwardAsChatCompletions_ResponseFailedCustomErrorMissReturnsGeneric500(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	failed := `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"temporary failure"},"output":[]}}`
	repo := &openAIWSPolicyRepo{}
	svc := newWSFixture(wsFixtureInputs{options: rawChatCompletionsTestConfig(), transport: &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: " + failed + "\n\n")),
	}}})
	setWSFixtureHealth(svc, newUpstreamHealthForTest(repo, svc.options, nil, providercore.HealthOptions{}, nil))

	provider := forcedResponsesChatTestProvider()
	provider.Record.Credentials["custom_error_codes_enabled"] = true
	provider.Record.Credentials["custom_error_codes"] = []any{float64(http.StatusUnprocessableEntity)}

	_, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")

	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "Upstream gateway error", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	require.Zero(t, repo.setErrorCalls)
}

// TestForwardAsChatCompletions_ResponseFailedCustomNonDefaultStatusFailsOver
// admits semantic HTTP 422 through provider custom codes. It checks typed
// recovery, scheduling error recording, no same-provider retry and no client
// commitment.
func TestForwardAsChatCompletions_ResponseFailedCustomNonDefaultStatusFailsOver(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	failed := `{"type":"response.failed","response":{"status":"failed","error":{"status_code":422,"code":"configured","type":"upstream_error","message":"configured failure"},"output":[]}}`
	repo := &openAIWSPolicyRepo{}
	svc := newWSFixture(wsFixtureInputs{options: rawChatCompletionsTestConfig(), transport: &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: " + failed + "\n\n")),
	}}})
	setWSFixtureHealth(svc, newUpstreamHealthForTest(repo, svc.options, nil, providercore.HealthOptions{}, nil))

	provider := forcedResponsesChatTestProvider()
	provider.Record.Credentials["custom_error_codes_enabled"] = true
	provider.Record.Credentials["custom_error_codes"] = []any{float64(http.StatusUnprocessableEntity)}

	_, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusUnprocessableEntity, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameProvider)
	require.False(t, c.Writer.Written())
	require.Equal(t, 1, repo.setErrorCalls)
}

// TestForwardAsChatCompletions_ResponseFailed_ErrorCodeRuleMatchesViaSemanticStatus
// matches HTTP 400 inferred from a context-window event. It checks the
// configured final status, upstream_error type and context-window message in
// the envelope.
func TestForwardAsChatCompletions_ResponseFailed_ErrorCodeRuleMatchesViaSemanticStatus(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	bindStatusCodePassthroughRule(c, "openai", http.StatusBadRequest, "context_length_exceeded", http.StatusBadRequest)

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(buildContextLengthFailedSSE())),
	}}
	svc := newWSFixture(wsFixtureInputs{options: rawChatCompletionsTestConfig(), transport: upstream})

	provider := forcedResponsesChatTestProvider()
	_, err := svc.Text.Chat(context.Background(), c, provider, body, "", "")

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, "error-code-conditioned rule should match via semantic status inference")
	respBody := rec.Body.String()
	require.Equal(t, "upstream_error", gjson.Get(respBody, "error.type").String())
	require.Contains(t, gjson.Get(respBody, "error.message").String(), "context window")
}

// TestNormalizeResponsesRequestServiceTier passes the listed aliases,
// recognized tiers and unsupported value through request normalization. It
// checks priority canonicalization, retained accepted values and empty
// unsupported tier.
func TestNormalizeResponsesRequestServiceTier(t *testing.T) {
	t.Parallel()

	req := &openaicore.ResponsesRequest{ServiceTier: " fast "}
	normalizeResponsesRequestServiceTier(req)
	require.Equal(t, "priority", req.ServiceTier)

	req.ServiceTier = "flex"
	normalizeResponsesRequestServiceTier(req)
	require.Equal(t, "flex", req.ServiceTier)

	// Recognized tiers pass through unchanged.
	req.ServiceTier = "auto"
	normalizeResponsesRequestServiceTier(req)
	require.Equal(t, "auto", req.ServiceTier)

	req.ServiceTier = "default"
	normalizeResponsesRequestServiceTier(req)
	require.Equal(t, "default", req.ServiceTier)

	req.ServiceTier = "scale"
	normalizeResponsesRequestServiceTier(req)
	require.Equal(t, "scale", req.ServiceTier)

	// Unknown tier values are removed.
	req.ServiceTier = "turbo"
	normalizeResponsesRequestServiceTier(req)
	require.Empty(t, req.ServiceTier)
}

// TestNormalizeResponsesBodyServiceTier passes the listed service tiers
// through byte-level normalization. It checks returned tier and outgoing field
// together, including ultrafast retention and deletion of the unsupported
// value.
func TestNormalizeResponsesBodyServiceTier(t *testing.T) {
	t.Parallel()

	body, tier, err := normalizeResponsesBodyServiceTier([]byte(`{"model":"gpt-5.1","service_tier":"fast"}`))
	require.NoError(t, err)
	require.Equal(t, "priority", tier)
	require.Equal(t, "priority", gjson.GetBytes(body, "service_tier").String())

	body, tier, err = normalizeResponsesBodyServiceTier([]byte(`{"model":"gpt-5.1","service_tier":"flex"}`))
	require.NoError(t, err)
	require.Equal(t, "flex", tier)
	require.Equal(t, "flex", gjson.GetBytes(body, "service_tier").String())

	// Recognized tiers remain in the upstream body.
	body, tier, err = normalizeResponsesBodyServiceTier([]byte(`{"model":"gpt-5.1","service_tier":"auto"}`))
	require.NoError(t, err)
	require.Equal(t, "auto", tier)
	require.Equal(t, "auto", gjson.GetBytes(body, "service_tier").String())

	body, tier, err = normalizeResponsesBodyServiceTier([]byte(`{"model":"gpt-5.1","service_tier":"default"}`))
	require.NoError(t, err)
	require.Equal(t, "default", tier)
	require.Equal(t, "default", gjson.GetBytes(body, "service_tier").String())

	body, tier, err = normalizeResponsesBodyServiceTier([]byte(`{"model":"gpt-5.1","service_tier":"scale"}`))
	require.NoError(t, err)
	require.Equal(t, "scale", tier)
	require.Equal(t, "scale", gjson.GetBytes(body, "service_tier").String())

	body, tier, err = normalizeResponsesBodyServiceTier([]byte(`{"model":"gpt-5.6-sol","service_tier":"ultrafast"}`))
	require.NoError(t, err)
	require.Equal(t, "ultrafast", tier)
	require.Equal(t, "ultrafast", gjson.GetBytes(body, "service_tier").String())

	// Unknown tier values are removed.
	body, tier, err = normalizeResponsesBodyServiceTier([]byte(`{"model":"gpt-5.1","service_tier":"turbo"}`))
	require.NoError(t, err)
	require.Empty(t, tier)
	require.False(t, gjson.GetBytes(body, "service_tier").Exists())
}

// TestOpenAISetupTokenChatCompletionsUsesCodexTransform captures setup-token
// Chat before an HTTP 400 fixture rejection. It checks Codex URL, credential
// and identity headers, promoted instructions, user input and derived cache
// key.
func TestOpenAISetupTokenChatCompletionsUsesCodexTransform(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"setup instructions"},{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop after request capture"}}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}, transport: upstream})
	provider := openAISetupTokenCompatProvider(71)

	result, err := svc.Text.Chat(context.Background(), c, provider, body, "", "gpt-5.4")

	require.Error(t, err)
	require.Nil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, ChatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "Bearer setup-token-value", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "chatgpt-setup", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.NotEmpty(t, upstream.lastReq.Header.Get("originator"))
	require.Equal(t, "setup instructions", gjson.GetBytes(upstream.lastBody, "instructions").String())
	require.Equal(t, int64(1), gjson.GetBytes(upstream.lastBody, "input.#").Int())
	require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "input.0.role").String())
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
}
