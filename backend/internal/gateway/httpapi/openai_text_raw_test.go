package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	openaicore "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/ollama"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openAIRawStreamDisconnectedWriter simulates client disconnection by failing both write methods.
// Raw forwarding uses WriteString, so overriding Write alone would miss that path.
type openAIRawStreamDisconnectedWriter struct {
	gin.ResponseWriter
}

// newExecutionReadersFixture shares one settings store among test readers.
func newExecutionReadersFixture(repo settings.Repository, _ *wsFixtureOptions) *gatewayprovider.RuntimeReaders {
	if repo != nil {
		repo = settings.New(repo)
	}
	return gatewaytestkit.RuntimeReaders(repo)
}

func TestForwardAsRawChatCompletions_ForcesStreamUsageUpstreamAndPassesUsageDownstream(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":3}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_raw_usage"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()

	result, err := svc.Text.RawChat(context.Background(), c, provider, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 3, result.Usage.CacheReadInputTokens)
	require.NotNil(t, upstream.lastReq)
	require.NoError(t, upstream.lastReq.Context().Err())
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
	require.Contains(t, rec.Body.String(), `"usage"`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
}

func TestForwardAsRawChatCompletions_TransportErrorFailsOver(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"hello"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{
		err: errors.New(`Post "https://opencode.ai/zen/v1/chat/completions": EOF`),
	}
	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()
	provider.Record.Credentials["base_url"] = "https://opencode.ai/zen/v1"

	_, err := svc.Text.RawChat(context.Background(), c, provider, body, "")

	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "transport error must trigger provider failover")
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, 0, rec.Body.Len(), "service must not write a hard 502 before handler can fail over")
}

func TestForwardAsRawChatCompletions_PreservesMappedGPT56MaxEffort(t *testing.T) {
	body := []byte(`{"model":"sol","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"max","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_max","object":"chat.completion","model":"gpt-5.6-sol","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()
	provider.Record.Credentials["model_mapping"] = map[string]any{"sol": "gpt-5.6-sol"}

	result, err := svc.Text.RawChat(context.Background(), c, provider, body, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "max", *result.ReasoningEffort)
}

func TestForwardAsRawChatCompletions_RecordsMappedThirdPartyMaxEffort(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"max","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_max","object":"chat.completion","model":"deepseek/deepseek-v4-flash-0731","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()
	provider.Record.Credentials["model_mapping"] = map[string]any{"deepseek-v4-flash": "deepseek/deepseek-v4-flash-0731"}

	result, err := svc.Text.RawChat(context.Background(), c, provider, body, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "deepseek/deepseek-v4-flash-0731", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "max", *result.ReasoningEffort)
}

func TestForwardAsRawChatCompletions_NonStreamingCapturesCacheWriteUsage(t *testing.T) {
	tests := []struct {
		name      string
		usageJSON string
		wantWrite int
	}{
		{
			name:      "positive cache write",
			usageJSON: `{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":6}}`,
			wantWrite: 6,
		},
		{
			name:      "nested zero overrides legacy alias",
			usageJSON: `{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15,"cache_creation_input_tokens":19,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":0}}`,
			wantWrite: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}],"stream":false}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"chatcmpl_cache","object":"chat.completion","model":"gpt-5.6","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":` + tt.usageJSON + `}`,
				)),
			}}
			svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

			result, err := svc.Text.RawChat(context.Background(), c, rawChatCompletionsTestProvider(), body, "")

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 12, result.Usage.InputTokens)
			require.Equal(t, 4, result.Usage.CacheReadInputTokens)
			require.Equal(t, tt.wantWrite, result.Usage.CacheCreationInputTokens)
		})
	}
}

func TestForwardAsRawChatCompletions_PreservesDeepSeekReasoningContentNonStreaming(t *testing.T) {
	body := []byte(`{"model":"deepseek-reasoner","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamJSON := `{"id":"chatcmpl_reasoning","object":"chat.completion","model":"deepseek-reasoner","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"think first","content":"final answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_deepseek_reasoning_json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamJSON)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()

	result, err := svc.Text.RawChat(context.Background(), c, provider, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Equal(t, "think first", gjson.Get(rec.Body.String(), "choices.0.message.reasoning_content").String())
	require.Equal(t, "final answer", gjson.Get(rec.Body.String(), "choices.0.message.content").String())
	require.JSONEq(t, upstreamJSON, rec.Body.String())
}

func TestForwardAsRawChatCompletions_PreservesDeepSeekReasoningContentStreaming(t *testing.T) {
	body := []byte(`{"model":"deepseek-reasoner","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_reasoning","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_reasoning","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"reasoning_content":"think first"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_reasoning","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"content":"final answer"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_reasoning","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_deepseek_reasoning_stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()

	result, err := svc.Text.RawChat(context.Background(), c, provider, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `"reasoning_content":"think first"`)
	require.Contains(t, rec.Body.String(), `"content":"final answer"`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
}

func TestForwardAsRawChatCompletions_PreservesDeepSeekReasoningContentInRequest(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"weather"},{"role":"assistant","reasoning_content":"need tool","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"cloudy"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_deepseek_reasoning_request"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_request","object":"chat.completion","model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()

	result, err := svc.Text.RawChat(context.Background(), c, provider, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "need tool", gjson.GetBytes(upstream.lastBody, "messages.1.reasoning_content").String())
	require.Equal(t, "get_weather", gjson.GetBytes(upstream.lastBody, "messages.1.tool_calls.0.function.name").String())
}

func TestForwardAsRawChatCompletions_NormalizesGLMReasoningEffortForUpstream(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"xhigh","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_glm_effort"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_glm","object":"chat.completion","model":"glm-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()

	result, err := svc.Text.RawChat(context.Background(), c, provider, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "max", *result.ReasoningEffort)
}

func TestForwardAsRawChatCompletions_SilentRefusalTriggersFailover(t *testing.T) {
	body := largeRawChatCompletionsBody()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_silent","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		"",
		`data: {"id":"chatcmpl_silent","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_silent"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, rawChatCompletionsTestProvider(), body, "")
	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, forwardcore.IsOpenAISilentRefusalErrorBody(failoverErr.ResponseBody))
	require.False(t, c.Writer.Written(), "silent refusal must not commit a 200 response before failover")
	require.Empty(t, rec.Body.String())
}

func TestForwardAsRawChatCompletions_SilentRefusalToolCallsExempt(t *testing.T) {
	body := largeRawChatCompletionsBody()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		"",
		`data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]}}]}`,
		"",
		`data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_tool"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, rawChatCompletionsTestProvider(), body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"tool_calls"`)
	require.Contains(t, rec.Body.String(), `"finish_reason":"tool_calls"`)
}

func TestForwardAsRawChatCompletions_SilentRefusalNormalContentExempt(t *testing.T) {
	body := largeRawChatCompletionsBody()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_ok","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		"",
		`data: {"id":"chatcmpl_ok","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		"",
		`data: {"id":"chatcmpl_ok","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_ok"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, rawChatCompletionsTestProvider(), body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"content":"ok"`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
}

// TestForwardAsRawChatCompletions_StripsEmptyToolCallIdentity checks removal of empty id/name fields in later
// DashScope and DeepSeek deltas.
// Keep initial identities and argument fragments; empty fields otherwise overwrite identities in clients such
// as dsh and cause unknown-tool errors.
func TestForwardAsRawChatCompletions_StripsEmptyToolCallIdentity(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"weather"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_example","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`,
		"",
		`data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"{\"query\":"}}]}}]}`,
		"",
		`data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"\"example\"}"}}]}}]}`,
		"",
		`data: {"id":"chatcmpl_tool","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_tool_identity"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()

	result, err := svc.Text.RawChat(context.Background(), c, provider, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)

	downstream := rec.Body.String()
	require.Contains(t, downstream, `"id":"call_example"`)
	require.Contains(t, downstream, `"name":"web_search"`)
	require.Contains(t, downstream, `{\"query\":`)
	require.Contains(t, downstream, `\"example\"}`)
	require.Contains(t, downstream, "data: [DONE]")
	require.NotContains(t, downstream, `"id":""`)
	require.NotContains(t, downstream, `"name":""`)

	// Inspect each downstream data payload: later tool_calls.0.id and function.name fields
	// must be absent while the valid initial values remain.
	followUpSeen := false
	for line := range strings.SplitSeq(downstream, "\n") {
		payload, ok := openaicore.ExtractSSEDataLine(line)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(payload)
		if trimmed == "" || trimmed == "[DONE]" {
			continue
		}
		delta := gjson.Get(payload, "choices.0.delta")
		if !delta.Exists() || !delta.Get("tool_calls").Exists() {
			continue
		}
		id := delta.Get("tool_calls.0.id")
		if id.String() == "call_example" {
			require.Equal(t, "web_search", delta.Get("tool_calls.0.function.name").String())
			continue
		}
		require.False(t, id.Exists(), "empty id must be stripped: %s", payload)
		require.False(t, delta.Get("tool_calls.0.function.name").Exists(), "empty name must be stripped: %s", payload)
		require.NotEmpty(t, delta.Get("tool_calls.0.function.arguments").String())
		followUpSeen = true
	}
	require.True(t, followUpSeen)
}

// TestForwardAsRawChatCompletions_TruncatedStreamAfterOutputFailsRequest checks EOF without [DONE], usage, or
// finish_reason after output.
// The handler uses the error to append an SSE error and record SLA failure.
func TestForwardAsRawChatCompletions_TruncatedStreamAfterOutputFailsRequest(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := `data: {"id":"chatcmpl_cut","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"half an ans"},"finish_reason":null}]}` + "\n" + ""
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_truncated"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, rawChatCompletionsTestProvider(), body, "")
	require.Error(t, err)
	require.NotNil(t, result, "已收字节的用量仍需带回，供 ops 记录首 token 时延")
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "已写出语义字节后不得再 failover")

	code, message, ok := openai.OpenAIUpstreamStreamReadErrorDetails(err)
	require.True(t, ok)
	require.Equal(t, openai.OpenAIUpstreamStreamTruncatedCode, code)
	require.NotEmpty(t, message)
	// Already delivered content remains intact.
	require.Contains(t, rec.Body.String(), `"content":"half an ans"`)
	require.NotContains(t, rec.Body.String(), "data: [DONE]")
}

// TestForwardAsRawChatCompletions_EmptyStreamBeforeOutputTriggersFailover checks retry before header
// commitment for an empty HTTP 200 SSE stream.
func TestForwardAsRawChatCompletions_EmptyStreamBeforeOutputTriggersFailover(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_empty"}},
		Body:       io.NopCloser(strings.NewReader("")),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, rawChatCompletionsTestProvider(), body, "")
	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, openai.OpenAIUpstreamStreamTruncatedCode,
		gjson.GetBytes(failoverErr.ResponseBody, "error.code").String())
	require.True(t, failoverErr.ShouldRetryNextProvider())
	require.False(t, c.Writer.Written(), "换号重试前不得提交 200 响应头")
	require.Empty(t, rec.Body.String())
}

// TestForwardAsRawChatCompletions_StreamReadErrorAfterOutputFailsRequest checks that transport failures after
// output fail the request
// and remain distinguishable from clean EOF truncation in Ops.
func TestForwardAsRawChatCompletions_StreamReadErrorAfterOutputFailsRequest(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_reset"}},
		Body: &openAIChatStreamReadErrorCloser{
			payload: []byte(`data: {"id":"chatcmpl_reset","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\n"),
			err:     errors.New("read tcp 172.18.0.4->172.65.90.23:443: read: connection reset by peer"),
		},
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, rawChatCompletionsTestProvider(), body, "")
	require.Error(t, err)
	require.NotNil(t, result)

	code, _, ok := openai.OpenAIUpstreamStreamReadErrorDetails(err)
	require.True(t, ok)
	require.Equal(t, openai.OpenAIUpstreamStreamReadErrorCode, code)
	require.Contains(t, rec.Body.String(), `"content":"partial"`)
}

// TestForwardAsRawChatCompletions_MissingDoneWithUsageStillSucceeds checks settlement when usage arrives
// without [DONE].
// Compatible upstreams may end with EOF after generation while still reporting consumed tokens.
func TestForwardAsRawChatCompletions_MissingDoneWithUsageStillSucceeds(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_nodone","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		"",
		`data: {"id":"chatcmpl_nodone","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":6,"total_tokens":17}}`,
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_nodone_usage"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, rawChatCompletionsTestProvider(), body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 6, result.Usage.OutputTokens)
}

// TestForwardAsRawChatCompletions_MissingDoneWithFinishReasonStillSucceeds checks termination with
// finish_reason but no [DONE] or usage.
func TestForwardAsRawChatCompletions_MissingDoneWithFinishReasonStillSucceeds(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_finish","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_finish","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_nodone_finish"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, rawChatCompletionsTestProvider(), body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"finish_reason":"stop"`)
}

func (w *openAIRawStreamDisconnectedWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed: client disconnected")
}

func (w *openAIRawStreamDisconnectedWriter) WriteString(string) (int, error) {
	return 0, errors.New("write failed: client disconnected")
}

// TestForwardAsRawChatCompletions_ClientDisconnectTruncationStillBills checks observed usage settlement after
// client disconnection and upstream truncation.
// These failures cannot establish an upstream fault, so provider health stays unchanged.
func TestForwardAsRawChatCompletions_ClientDisconnectTruncationStillBills(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Writer = &openAIRawStreamDisconnectedWriter{ResponseWriter: c.Writer}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := `data: {"id":"chatcmpl_gone","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n" + ""
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_gone"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, rawChatCompletionsTestProvider(), body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
}

// TestForwardAsRawChatCompletions_ClientCancelTruncationStillBills checks that context.Canceled from
// propagated client cancellation is not classified as upstream truncation.
func TestForwardAsRawChatCompletions_ClientCancelTruncationStillBills(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_cancel"}},
		Body: &openAIChatStreamReadErrorCloser{
			payload: []byte(`data: {"id":"chatcmpl_cancel","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n"),
			err:     context.Canceled,
		},
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, rawChatCompletionsTestProvider(), body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
}

func TestForwardAsRawChatCompletions_ClientDisconnectDrainsUsage(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Writer = &openAIChatFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":17,"completion_tokens":8,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":6}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_raw_disconnect"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()

	result, err := svc.Text.RawChat(context.Background(), c, provider, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 17, result.Usage.InputTokens)
	require.Equal(t, 8, result.Usage.OutputTokens)
	require.Equal(t, 6, result.Usage.CacheReadInputTokens)
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
}

func TestForwardAsRawChatCompletions_UpstreamRequestIgnoresClientCancel(t *testing.T) {
	reqCtx, cancel := context.WithCancel(context.Background())
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(reqCtx)
	c.Request.Header.Set("Content-Type", "application/json")
	cancel()

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_raw_ctx"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})
	provider := rawChatCompletionsTestProvider()

	result, err := svc.Text.RawChat(reqCtx, c, provider, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.NoError(t, upstream.lastReq.Context().Err())
}

func TestForwardAsRawChatCompletions_UsesFilteredServiceTierForBilling(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"service_tier":"priority"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_raw_filter_tier"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)),
	}}
	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream, readers: newExecutionReadersFixture(nil, nil)})
	ctx := gatewayprovider.WithFastPolicyContext(context.Background(), &tierpolicy.OpenAIFastPolicySettings{
		Rules: []tierpolicy.OpenAIFastPolicyRule{{
			ServiceTier: tierpolicy.OpenAIFastTierPriority,
			Action:      anthropic.BetaPolicyActionFilter,
			Scope:       anthropic.BetaPolicyScopeAPIKey,
		}},
	})

	result, err := svc.Text.RawChat(ctx, c, rawChatCompletionsTestProvider(), body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Nil(t, result.ServiceTier)
	require.False(t, gjson.GetBytes(upstream.lastBody, "service_tier").Exists())
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func largeRawChatCompletionsBody() []byte {
	return []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"` +
		strings.Repeat("x", openai.SilentRefusalMinRequestBodyBytes) +
		`"}],"stream":true}`)
}

func ollamaCloudRawChatCompletionsTestProvider() *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 143,
			Name:     "DeepSeek Ollama",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "https://ollama.com",
			},
			Extra: map[string]any{
				providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions),
			},
		},
	}
}

func TestIsOllamaCloudRawChatCompletionsProvider(t *testing.T) {
	t.Parallel()

	t.Run("ollama.com + force_chat_completions", func(t *testing.T) {
		t.Parallel()
		require.True(t, gatewayprovider.IsOllamaCloudRawChatCompletionsProvider(ollamaCloudRawChatCompletionsTestProvider()))
	})

	t.Run("ollama.com + historical probe is ignored", func(t *testing.T) {
		t.Parallel()
		provider := ollamaCloudRawChatCompletionsTestProvider()
		provider.Record.Extra = map[string]any{
			providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModePreserveClientProtocol),
		}
		require.False(t, gatewayprovider.IsOllamaCloudRawChatCompletionsProvider(provider))
	})

	t.Run("extra usage signal without ollama host", func(t *testing.T) {
		t.Parallel()
		provider := rawChatCompletionsTestProvider()
		provider.Record.Credentials["base_url"] = "https://example.invalid/v1"
		provider.Record.Extra = map[string]any{
			providercore.ExtraKeyTextRouteMode:            string(providercore.TextRouteModeForceChatCompletions),
			providercore.OllamaCloudUsageSnapshotExtraKey: map[string]any{"status": "ok"},
		}
		require.True(t, gatewayprovider.IsOllamaCloudRawChatCompletionsProvider(provider))
	})

	t.Run("official DeepSeek", func(t *testing.T) {
		t.Parallel()
		provider := rawChatCompletionsTestProvider()
		provider.Record.Name = "DeepSeek"
		provider.Record.Credentials["base_url"] = "https://api.deepseek.com"
		provider.Record.Extra = map[string]any{
			providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions),
		}
		require.False(t, gatewayprovider.IsOllamaCloudRawChatCompletionsProvider(provider))
	})

	t.Run("OpenCode Go extra", func(t *testing.T) {
		t.Parallel()
		provider := rawChatCompletionsTestProvider()
		provider.Record.Credentials["base_url"] = "https://opencode.ai/zen/go/v1"
		provider.Record.Extra = map[string]any{
			providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions),
			"opencode_go_usage_auto_refresh":   true,
		}
		require.False(t, gatewayprovider.IsOllamaCloudRawChatCompletionsProvider(provider))
	})

	t.Run("ollama.com without force_chat_completions", func(t *testing.T) {
		t.Parallel()
		provider := ollamaCloudRawChatCompletionsTestProvider()
		provider.Record.Extra = nil
		require.False(t, gatewayprovider.IsOllamaCloudRawChatCompletionsProvider(provider))
	})

	t.Run("anthropic ollama.com", func(t *testing.T) {
		t.Parallel()
		provider := ollamaCloudRawChatCompletionsTestProvider()
		provider.Record.Platform = capability.PlatformAnthropic
		require.False(t, gatewayprovider.IsOllamaCloudRawChatCompletionsProvider(provider))
	})
}

func TestNormalizeOllamaCloudChatCompletionsResponseJSON(t *testing.T) {
	t.Parallel()

	t.Run("copies delta.reasoning to reasoning_content", func(t *testing.T) {
		t.Parallel()
		in := []byte(`{"choices":[{"delta":{"reasoning":"abc"}}]}`)
		out := ollama.NormalizeOllamaCloudChatCompletionsResponseJSON(in)
		require.Equal(t, "abc", gjson.GetBytes(out, "choices.0.delta.reasoning").String())
		require.Equal(t, "abc", gjson.GetBytes(out, "choices.0.delta.reasoning_content").String())
	})

	t.Run("copies message.thinking to reasoning_content", func(t *testing.T) {
		t.Parallel()
		in := []byte(`{"choices":[{"message":{"thinking":"abc"}}]}`)
		out := ollama.NormalizeOllamaCloudChatCompletionsResponseJSON(in)
		require.Equal(t, "abc", gjson.GetBytes(out, "choices.0.message.thinking").String())
		require.Equal(t, "abc", gjson.GetBytes(out, "choices.0.message.reasoning_content").String())
	})

	t.Run("does not overwrite existing reasoning_content", func(t *testing.T) {
		t.Parallel()
		in := []byte(`{"choices":[{"delta":{"reasoning":"new","reasoning_content":"old"}}]}`)
		out := ollama.NormalizeOllamaCloudChatCompletionsResponseJSON(in)
		require.Equal(t, string(in), string(out))
		require.Equal(t, "old", gjson.GetBytes(out, "choices.0.delta.reasoning_content").String())
	})

	t.Run("empty reasoning does not open reasoning_content", func(t *testing.T) {
		t.Parallel()
		in := []byte(`{"choices":[{"delta":{"reasoning":""}}]}`)
		out := ollama.NormalizeOllamaCloudChatCompletionsResponseJSON(in)
		require.Equal(t, string(in), string(out))
		require.False(t, gjson.GetBytes(out, "choices.0.delta.reasoning_content").Exists())
	})

	t.Run("empty thinking does not open reasoning_content", func(t *testing.T) {
		t.Parallel()
		in := []byte(`{"choices":[{"message":{"thinking":""}}]}`)
		out := ollama.NormalizeOllamaCloudChatCompletionsResponseJSON(in)
		require.Equal(t, string(in), string(out))
		require.False(t, gjson.GetBytes(out, "choices.0.message.reasoning_content").Exists())
	})

	t.Run("tool call chunk is unchanged", func(t *testing.T) {
		t.Parallel()
		in := []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`)
		out := ollama.NormalizeOllamaCloudChatCompletionsResponseJSON(in)
		require.Equal(t, string(in), string(out))
	})
}

func TestNormalizeOllamaCloudChatCompletionsRequest(t *testing.T) {
	t.Parallel()

	in := []byte(`{"messages":[{"role":"user","content":"weather"},{"role":"assistant","reasoning_content":"prev","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]}]}`)
	out := ollama.NormalizeOllamaCloudChatCompletionsRequest(in)
	require.Equal(t, "prev", gjson.GetBytes(out, "messages.1.reasoning").String())
	require.Equal(t, "prev", gjson.GetBytes(out, "messages.1.reasoning_content").String())
	require.Equal(t, "", gjson.GetBytes(out, "messages.1.content").String())
	require.Equal(t, "get_weather", gjson.GetBytes(out, "messages.1.tool_calls.0.function.name").String())
	require.False(t, gjson.GetBytes(out, "messages.0.reasoning").Exists())
}

func TestApplyOllamaCloudRawChatCompletionsLeavesForeignProvidersUnchanged(t *testing.T) {
	t.Parallel()

	reqBody := []byte(`{"messages":[{"role":"assistant","reasoning_content":"prev","content":""}]}`)
	respBody := []byte(`{"choices":[{"delta":{"reasoning":"abc"}}]}`)
	sseLine := `data: {"choices":[{"delta":{"reasoning":"abc"}}]}`

	official := rawChatCompletionsTestProvider()
	official.Record.Name = "DeepSeek"
	official.Record.Credentials["base_url"] = "https://api.deepseek.com"
	official.Record.Extra = map[string]any{
		providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions),
	}

	opencode := rawChatCompletionsTestProvider()
	opencode.Record.Credentials["base_url"] = "https://opencode.ai/zen/go/v1"
	opencode.Record.Extra = map[string]any{
		providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions),
		"opencode_go_usage_auto_refresh":   true,
	}

	for _, provider := range []*gatewayprovider.ExecutionProvider{official, opencode} {
		require.Equal(t, reqBody, gatewayprovider.ApplyOllamaCloudRawChatCompletionsRequest(provider, reqBody))
		require.Equal(t, respBody, gatewayprovider.ApplyOllamaCloudRawChatCompletionsResponse(provider, respBody))
		require.Equal(t, sseLine, gatewayprovider.ApplyOllamaCloudRawChatCompletionsSSELine(provider, sseLine))
	}
}

func TestNormalizeOllamaCloudChatCompletionsSSELine(t *testing.T) {
	t.Parallel()

	before := `data: {"choices":[{"delta":{"reasoning":"abc"}}]}`
	after := ollama.NormalizeOllamaCloudChatCompletionsSSELine(before)
	require.True(t, strings.HasPrefix(after, "data: "))
	payload := strings.TrimPrefix(after, "data: ")
	require.Equal(t, "abc", gjson.Get(payload, "choices.0.delta.reasoning").String())
	require.Equal(t, "abc", gjson.Get(payload, "choices.0.delta.reasoning_content").String())
	require.Equal(t, "data: [DONE]", ollama.NormalizeOllamaCloudChatCompletionsSSELine("data: [DONE]"))
}

func TestForwardAsRawChatCompletions_OllamaCloudReasoningAliasStreaming(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_ollama","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_ollama","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"reasoning":"abc"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_ollama","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"final answer"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_ollama","object":"chat.completion.chunk","model":"deepseek-v4-pro","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8,"completion_tokens_details":{"reasoning_tokens":4}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_ollama_reasoning_stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, ollamaCloudRawChatCompletionsTestProvider(), body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `"reasoning":"abc"`)
	require.Contains(t, rec.Body.String(), `"reasoning_content":"abc"`)
	require.Contains(t, rec.Body.String(), `"content":"final answer"`)
	require.Contains(t, rec.Body.String(), `"reasoning_tokens":4`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
}

func TestForwardAsRawChatCompletions_OllamaCloudThinkingAliasNonStreaming(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"},{"role":"assistant","reasoning_content":"prev","content":""}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamJSON := `{"id":"chatcmpl_ollama","object":"chat.completion","model":"deepseek-v4-pro","choices":[{"index":0,"message":{"role":"assistant","thinking":"abc","content":"final answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8,"completion_tokens_details":{"reasoning_tokens":4}}}`
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_ollama_thinking_json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamJSON)),
	}}

	svc := newResponsesFixture(responsesFixtureInputs{options: protocolHTTPOptions(), transport: upstream})

	result, err := svc.Text.RawChat(context.Background(), c, ollamaCloudRawChatCompletionsTestProvider(), body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "prev", gjson.GetBytes(upstream.lastBody, "messages.1.reasoning").String())
	require.Equal(t, "prev", gjson.GetBytes(upstream.lastBody, "messages.1.reasoning_content").String())
	require.Equal(t, "abc", gjson.Get(rec.Body.String(), "choices.0.message.thinking").String())
	require.Equal(t, "abc", gjson.Get(rec.Body.String(), "choices.0.message.reasoning_content").String())
	require.Equal(t, "final answer", gjson.Get(rec.Body.String(), "choices.0.message.content").String())
	require.Equal(t, int64(4), gjson.Get(rec.Body.String(), "usage.completion_tokens_details.reasoning_tokens").Int())
}
