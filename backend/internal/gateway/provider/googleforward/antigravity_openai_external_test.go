package googleforward_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// TestAntigravityCompatOAuthUsesNativeTokenAndRoute compares Chat and Responses
// with a fixed cached OAuth token. Captured authorization uses that token and
// v1internal streaming, while client output and completion usage retain the
// expected protocol values.
func TestAntigravityCompatOAuthUsesNativeTokenAndRoute(t *testing.T) {
	tests := []struct {
		name string // table scenario name
		path string // HTTP fixture request path
		body []byte // client request fixture bytes
		// selected compatibility fixture invocation
		call func(*googleforward.Antigravity, context.Context, *gin.Context, *gatewayprovider.ExecutionProvider, []byte) (*forwardcore.MessagesResult, error)
	}{
		{
			name: "chat completions",

			path: "/v1/chat/completions",

			body: []byte(`{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":"Reply exactly: ok"}]}`),

			call: func(svc *googleforward.Antigravity, ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) (*forwardcore.MessagesResult, error) {
				return svc.ForwardAsChatCompletions(ctx, gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, body, nil)
			},
		},

		{
			name: "responses",

			path: "/v1/responses",

			body: []byte(`{"model":"gemini-3.1-pro-high","input":"Reply exactly: ok"}`),

			call: func(svc *googleforward.Antigravity, ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) (*forwardcore.MessagesResult, error) {
				return svc.ForwardAsResponses(ctx, gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, body, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var authorization string
			var upstreamPath string
			var upstreamAlt string
			upstream := &queuedHTTPUpstreamStub{
				responses: []*http.Response{antigravityCompatSuccessResponse()},

				onCall: func(req *http.Request, _ *queuedHTTPUpstreamStub) {
					authorization = req.Header.Get("Authorization")
					upstreamPath = req.URL.Path
					upstreamAlt = req.URL.Query().Get("alt")
				},
			}
			svc := newAntigravityCompatibilityFixture(
				googleforward.Options{MaxLineSize: 500 * 1024 * 1024},
				upstream,
			)
			c, recorder := newAntigravityCompatContext(http.MethodPost, tt.path, tt.body)

			result, err := tt.call(svc, context.Background(), c, newAntigravityCompatProvider(capability.ProviderTypeOAuth), tt.body)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "Bearer fresh-oauth-token", authorization)
			require.Equal(t, "/v1internal:streamGenerateContent", upstreamPath)
			require.Equal(t, "sse", upstreamAlt)
			require.Equal(t, "request-3757", result.RequestID)
			require.Equal(t, 8, result.Usage.InputTokens)
			require.Equal(t, 3, result.Usage.OutputTokens)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), "ok")
			if tt.name == "chat completions" {
				require.Equal(t, "stop", gjson.Get(recorder.Body.String(), "choices.0.finish_reason").String())
				require.Equal(t, int64(8), gjson.Get(recorder.Body.String(), "usage.prompt_tokens").Int())
				require.Equal(t, int64(3), gjson.Get(recorder.Body.String(), "usage.completion_tokens").Int())
			}
		})
	}
}

// TestAntigravityCompatResponsesRestoresNamespaceTools compares buffered and
// streaming Responses namespace-tool requests. The captured upstream flattened
// name is restored to client name and namespace, and the flattened spelling is
// absent from output.
func TestAntigravityCompatResponsesRestoresNamespaceTools(t *testing.T) {
	tests := []struct {
		name   string // table scenario name
		stream bool   // client streaming fixture flag
		body   []byte // client request fixture bytes
	}{
		{
			name: "non-streaming",

			body: []byte(`{"model":"gemini-3.1-pro-high","input":"Use the tool","tools":[{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"read_thread","description":"Read a task","parameters":{"type":"object","properties":{"thread_id":{"type":"string"}}}}]}]}`),
		},

		{
			name: "streaming",

			stream: true,

			body: []byte(`{"model":"gemini-3.1-pro-high","input":"Use the tool","stream":true,"tools":[{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"read_thread","description":"Read a task","parameters":{"type":"object","properties":{"thread_id":{"type":"string"}}}}]}]}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstreamBody := `data: {"response":{"responseId":"resp_namespace","candidates":[{"content":{"parts":[{"functionCall":{"id":"call_namespace","name":"codex_app__read_thread","args":{"thread_id":"123"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}` + "\n\n"
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
				StatusCode: http.StatusOK,

				Header: http.Header{"Content-Type": []string{"text/event-stream"}},

				Body: io.NopCloser(strings.NewReader(upstreamBody)),
			}}}
			svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, upstream)
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/responses", tt.body)

			result, err := svc.ForwardAsResponses(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), newAntigravityCompatProvider(capability.ProviderTypeOAuth), tt.body, nil)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tt.stream, result.Stream)
			require.Len(t, upstream.requestBodies, 1)
			require.Contains(t, string(upstream.requestBodies[0]), `"codex_app__read_thread"`)
			require.Contains(t, recorder.Body.String(), `"name":"read_thread"`)
			require.Contains(t, recorder.Body.String(), `"namespace":"codex_app"`)
			require.NotContains(t, recorder.Body.String(), `"codex_app__read_thread"`)
		})
	}
}

// TestAntigravityCompatChatRestoresForkToolNames compares buffered and
// streaming Chat requests with a rewritten session tool. Captured upstream
// bytes use the selected rewrite while client output restores the requested
// tool name.
func TestAntigravityCompatChatRestoresForkToolNames(t *testing.T) {
	tests := []struct {
		name   string // table scenario name
		stream bool   // client streaming fixture flag
		body   []byte // client request fixture bytes
	}{
		{
			name: "non-streaming",

			body: []byte(`{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":"Use the tool"}],"tools":[{"type":"function","function":{"name":"sessions_lookup","description":"Look up a session","parameters":{"type":"object","properties":{}}}}]}`),
		},

		{
			name: "streaming",

			stream: true,

			body: []byte(`{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":"Use the tool"}],"stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"sessions_lookup","description":"Look up a session","parameters":{"type":"object","properties":{}}}}]}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstreamBody := `data: {"response":{"responseId":"resp_tool_rewrite","candidates":[{"content":{"parts":[{"functionCall":{"id":"call_tool_rewrite","name":"cc_sess_lookup","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}` + "\n\n"
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
				StatusCode: http.StatusOK,

				Header: http.Header{"Content-Type": []string{"text/event-stream"}},

				Body: io.NopCloser(strings.NewReader(upstreamBody)),
			}}}
			svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, upstream)
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", tt.body)

			result, err := svc.ForwardAsChatCompletions(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), newAntigravityCompatProvider(capability.ProviderTypeOAuth), tt.body, nil)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tt.stream, result.Stream)
			require.Len(t, upstream.requestBodies, 1)
			require.Contains(t, string(upstream.requestBodies[0]), `"cc_sess_lookup"`)
			require.NotContains(t, string(upstream.requestBodies[0]), `"sessions_lookup"`)
			require.Contains(t, recorder.Body.String(), `"sessions_lookup"`)
			require.NotContains(t, recorder.Body.String(), `"cc_sess_lookup"`)
		})
	}
}

// TestAntigravityCompatRejectsUnsupportedProviderType supplies upstream and
// Setup Token targets to Chat or Responses compatibility. Preparation returns
// no result and local status 400 identifying the required Antigravity OAuth
// provider kind.
func TestAntigravityCompatRejectsUnsupportedProviderType(t *testing.T) {
	tests := []struct {
		name         string // table scenario name
		path         string // HTTP fixture request path
		providerType string // selected provider credential kind
		// selected compatibility fixture invocation
		call func(*googleforward.Antigravity, context.Context, *gin.Context, *gatewayprovider.ExecutionProvider, []byte) (*forwardcore.MessagesResult, error)
	}{
		{
			name: "chat completions upstream",

			path: "/v1/chat/completions",

			providerType: capability.ProviderTypeUpstream,

			call: func(svc *googleforward.Antigravity, ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) (*forwardcore.MessagesResult, error) {
				return svc.ForwardAsChatCompletions(ctx, gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, body, nil)
			},
		},

		{
			name: "responses setup token",

			path: "/v1/responses",

			providerType: capability.ProviderTypeSetupToken,

			call: func(svc *googleforward.Antigravity, ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) (*forwardcore.MessagesResult, error) {
				return svc.ForwardAsResponses(ctx, gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, body, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"gemini-3.1-pro-high"}`)
			c, recorder := newAntigravityCompatContext(http.MethodPost, tt.path, body)

			result, err := tt.call(newAntigravityFixture(antigravityDependencies{}), context.Background(), c, newAntigravityCompatProvider(tt.providerType), body)

			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "native OAuth provider required for antigravity compatibility mode")
		})
	}
}

// TestBuildAntigravityCompatGeminiBody_ConfiguresMixedToolInvocations compares
// mixed server/client tools against either category alone. The wrapped native
// request adds camelCase server-side invocation configuration for the mixed
// case and omits it otherwise.
func TestBuildAntigravityCompatGeminiBody_ConfiguresMixedToolInvocations(t *testing.T) {
	svc := newAntigravityFixture(antigravityDependencies{})
	tests := []struct {
		name      string // table scenario name
		tools     string // input tool fixture JSON
		wantField bool   // expected invocation-option presence flag
	}{
		{
			name: "mixed server and client tools",

			tools: `[{"name":"get_weather","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}]`,

			wantField: true,
		},

		{
			name:  "client tools only",
			tools: `[{"name":"get_weather","input_schema":{"type":"object"}}]`,
		},

		{
			name:  "server tools only",
			tools: `[{"type":"web_search_20250305","name":"web_search"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claudeBody := []byte(`{"messages":[{"role":"user","content":"hello"}],"tools":` + tt.tools + `}`)
			claudeBody = bytes.ReplaceAll(claudeBody, []byte{92}, nil)
			body, err := googleforward.AntigravityBodyForTest(svc, context.Background(), claudeBody, nil, "project-1", "gemini-2.5-flash")
			require.NoError(t, err)

			var wrapped map[string]any
			require.NoError(t, json.Unmarshal(body, &wrapped))
			request, ok := wrapped["request"].(map[string]any)
			require.True(t, ok)
			toolConfig, exists := request["toolConfig"].(map[string]any)
			if !tt.wantField {
				require.False(t, exists)
				return
			}
			require.True(t, exists)
			require.Equal(t, true, toolConfig["includeServerSideToolInvocations"])
			require.NotContains(t, toolConfig, "include_server_side_tool_invocations")
		})
	}
}

// TestAntigravityCompatChatMixedBuiltInToolsEnableServerSideInvocations
// converts Chat function, search, and code-execution tools together through a
// Claude-family route. Captured upstream bytes retain both function
// declarations and built-in tools with server-side invocation configuration.
func TestAntigravityCompatChatMixedBuiltInToolsEnableServerSideInvocations(t *testing.T) {
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}}
	svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, upstream)
	body := []byte(`{
		"model":"claude-opus-4-6-thinking",
		"messages":[{"role":"user","content":"hello"}],
		"stream":true,
		"tools":[
			{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}},
			{"type":"function","function":{"name":"terminal","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}},
			{"type":"web_search"},
			{"type":"code_execution"}
		]
	}`)
	c, _ := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)

	result, err := svc.ForwardAsChatCompletions(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), newAntigravityCompatProvider(capability.ProviderTypeOAuth), body, nil)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requestBodies, 1)
	requestBody := upstream.requestBodies[0]
	require.True(t, gjson.GetBytes(requestBody, "request.toolConfig.includeServerSideToolInvocations").Bool())
	require.Len(t, gjson.GetBytes(requestBody, "request.tools.0.functionDeclarations").Array(), 2)
	require.True(t, gjson.GetBytes(requestBody, "request.tools.1.googleSearch").Exists())
	require.True(t, gjson.GetBytes(requestBody, "request.tools.2.codeExecution").Exists())
}

// TestAntigravityCompatPreservesChatTokenLimit compares positive Chat limits,
// completion-limit precedence, and values around the platform ceiling. Captured
// Gemini maxOutputTokens reflects the requested positive limit capped at 64000
// after precedence selection.
func TestAntigravityCompatPreservesChatTokenLimit(t *testing.T) {
	tests := []struct {
		name string // table scenario name
		body string // client request fixture bytes
		want int64  // expected max-output token value
	}{
		{
			name: "legacy max_tokens below bridge floor",

			body: `{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":"ok"}],"max_tokens":8}`,

			want: 8,
		},

		{
			name: "max_completion_tokens takes precedence",

			body: `{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":"ok"}],"max_tokens":8,"max_completion_tokens":13}`,

			want: 13,
		},

		{
			name: "max_tokens at safe ceiling is preserved",

			body: `{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":"ok"}],"max_tokens":64000}`,

			want: 64000,
		},

		{
			name: "max_tokens above safe ceiling is clamped",

			body: `{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":"ok"}],"max_tokens":64001}`,

			want: 64000,
		},

		{
			name: "precedence applies before clamping",

			body: `{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":"ok"}],"max_tokens":8,"max_completion_tokens":64001}`,

			want: 64000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}}
			svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, upstream)
			body := []byte(tt.body)
			c, _ := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)

			result, err := svc.ForwardAsChatCompletions(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), newAntigravityCompatProvider(capability.ProviderTypeOAuth), body, nil)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requestBodies, 1)
			require.Equal(t, tt.want, gjson.GetBytes(upstream.requestBodies[0], "request.generationConfig.maxOutputTokens").Int())
		})
	}
}

// TestPreserveChatCompletionTokenLimitIgnoresAbsentAndNonPositiveValues
// supplies absent and nonpositive limits to the token-limit helper, including
// completion-limit precedence. The existing Anthropic max_tokens value remains
// unchanged for each exercised input.
func TestPreserveChatCompletionTokenLimitIgnoresAbsentAndNonPositiveValues(t *testing.T) {
	tests := []struct {
		name string // table scenario name
		// Chat token-limit fixture value
		request protocolopenai.ChatCompletionsRequest
	}{
		{name: "absent"},

		{name: "zero max_tokens", request: protocolopenai.ChatCompletionsRequest{MaxTokens: antigravityCompatIntPtr(0)}},

		{
			name:    "negative max_completion_tokens takes precedence",
			request: protocolopenai.ChatCompletionsRequest{MaxTokens: antigravityCompatIntPtr(12), MaxCompletionTokens: antigravityCompatIntPtr(-1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claudeRequest := &protocolanthropic.AnthropicRequest{MaxTokens: 99}
			antigravity.PreserveChatCompletionTokenLimit(&tt.request, claudeRequest)
			require.Equal(t, 99, claudeRequest.MaxTokens)
		})
	}
}

// TestAntigravityCompatRoutesByMappedModelFamily compares Gemini and Claude-
// family selected models during Chat compatibility. Captured v1internal model
// matches the input and sessionId presence follows the exercised model-family
// transform branch.
func TestAntigravityCompatRoutesByMappedModelFamily(t *testing.T) {
	tests := []struct {
		model         string // selected route model identifier
		wantSessionID bool   // expected native session presence
	}{
		{model: "gemini-3.1-pro-high", wantSessionID: false},

		{model: "claude-sonnet-4-5", wantSessionID: true},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}}
			svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, upstream)
			body := []byte(`{"model":"` + tt.model + `","messages":[{"role":"user","content":"ok"}],"max_tokens":8}`)
			c, _ := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)

			result, err := svc.ForwardAsChatCompletions(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), newAntigravityCompatProvider(capability.ProviderTypeOAuth), body, nil)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requestBodies, 1)
			require.Equal(t, tt.model, gjson.GetBytes(upstream.requestBodies[0], "model").String())
			require.Equal(t, tt.wantSessionID, gjson.GetBytes(upstream.requestBodies[0], "request.sessionId").Exists())
		})
	}
}

// TestAntigravityCompatUnauthorizedIsCredentialFailure returns upstream status
// 401 with correlation identity before output. Typed failover carries provider-
// auth scope/reason, next-provider action, safe status/message, and the
// correlation header while the client body remains empty.
func TestAntigravityCompatUnauthorizedIsCredentialFailure(t *testing.T) {
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
		StatusCode: http.StatusUnauthorized,

		Header: http.Header{"X-Request-Id": []string{"auth-3757"}},

		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Invalid bearer token"}}`)),
	}}}
	svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, upstream)
	body := []byte(`{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":"ok"}]}`)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)

	result, err := svc.ForwardAsChatCompletions(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), newAntigravityCompatProvider(capability.ProviderTypeOAuth), body, nil)

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, forwardcore.GatewayFailureStageProviderAuth, failoverErr.Stage)
	require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
	require.Equal(t, forwardcore.AntigravityCredentialRejectedReason, failoverErr.Reason)
	require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
	require.Equal(t, http.StatusBadGateway, failoverErr.ClientStatusCode)
	require.Equal(t, forwardcore.AntigravityCredentialRejectedClientMessage, failoverErr.ClientMessage)
	require.Equal(t, "auth-3757", http.Header(failoverErr.ResponseHeaders).Get("X-Request-Id"))
	require.Empty(t, recorder.Body.String())
}

// TestAntigravityCompatUsageOnlyNonStreamingTriggersFailover supplies usage-
// only upstream events to buffered Chat and Responses requests. Both return
// retryable failover with nil result and leave response body and content-type
// header unstarted.
func TestAntigravityCompatUsageOnlyNonStreamingTriggersFailover(t *testing.T) {
	tests := []struct {
		name string // table scenario name
		path string // HTTP fixture request path
		body []byte // client request fixture bytes
		// selected compatibility fixture invocation
		call func(*googleforward.Antigravity, context.Context, *gin.Context, *gatewayprovider.ExecutionProvider, []byte) (*forwardcore.MessagesResult, error)
	}{
		{
			name: "chat completions",

			path: "/v1/chat/completions",

			body: []byte(`{"model":"gemini-3.1-pro-high","messages":[{"role":"user","content":"ok"}]}`),

			call: func(svc *googleforward.Antigravity, ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) (*forwardcore.MessagesResult, error) {
				return svc.ForwardAsChatCompletions(ctx, gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, body, nil)
			},
		},

		{
			name: "responses",

			path: "/v1/responses",

			body: []byte(`{"model":"gemini-3.1-pro-high","input":"ok"}`),

			call: func(svc *googleforward.Antigravity, ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte) (*forwardcore.MessagesResult, error) {
				return svc.ForwardAsResponses(ctx, gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, body, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
				StatusCode: http.StatusOK,

				Header: http.Header{"Content-Type": []string{"text/event-stream"}},

				Body: io.NopCloser(strings.NewReader(
					`data: {"response":{"responseId":"resp_3757","usageMetadata":{"promptTokenCount":8}}}` + "\n\n",
				)),
			}}}
			svc := newAntigravityCompatibilityFixture(googleforward.Options{MaxLineSize: 500 * 1024 * 1024}, upstream)
			c, recorder := newAntigravityCompatContext(http.MethodPost, tt.path, tt.body)

			result, err := tt.call(
				svc,
				context.Background(),
				c,
				newAntigravityCompatProvider(capability.ProviderTypeOAuth),
				tt.body,
			)

			require.Nil(t, result)
			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.RetryableOnSameProvider)
			require.Empty(t, recorder.Body.String())
			require.Empty(t, recorder.Header().Get("Content-Type"))
		})
	}
}

// newAntigravityCompatProvider creates a test target with the supplied provider
// kind. It carries a stale record token, project, and selected identity
// mappings so compatibility fixtures can observe cached-token replacement.
func newAntigravityCompatProvider(providerType string) *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           3757,

			Name: "antigravity-compat",

			Platform: capability.PlatformAntigravity,

			Type: providerType,

			Status: billing.StatusActive,

			Concurrency: 1,

			Credentials: map[string]any{
				"access_token": "stale-provider-token",

				"project_id": "project-3757",

				"model_mapping": map[string]any{
					"gemini-3.1-pro-high": "gemini-3.1-pro-high",

					"claude-sonnet-4-5": "claude-sonnet-4-5",

					"claude-opus-4-6-thinking": "claude-opus-4-6-thinking",
				},
			},
		},
	}
}

// antigravityCompatSuccessResponse creates a fresh stream response containing
// text, terminal reason, and usage. Its reader is handed to platform execution
// for response consumption and closure during each fixture call.
func antigravityCompatSuccessResponse() *http.Response {
	body := `data: {"response":{"responseId":"resp_3757","candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}` + "\n\n"
	return &http.Response{
		StatusCode: http.StatusOK,

		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"request-3757"},
		},

		Body: io.NopCloser(strings.NewReader(body)),
	}
}

// antigravityCompatIntPtr returns a pointer to the supplied fixture token-limit
// value.
func antigravityCompatIntPtr(v int) *int { return &v }
