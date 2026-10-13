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

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/compact"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestOpenAIPassthroughCompactFallbackSecondStreamFailureUsesStandardErrorPath
// returns context-window failures from both initial and Compact retry streams.
// It checks two requests, final HTTP 400, no remaining Compact signal and
// ordered retry/error Ops events.
func TestOpenAIPassthroughCompactFallbackSecondStreamFailureUsesStandardErrorPath(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":true,"input":[{"type":"compaction_trigger"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	MarkOpenAINativeCompactionV2(c)

	failed := "event: response.failed\n" +
		`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"context window exceeded"}}}` + "\n\n"
	upstream := &auxiliaryHTTPRecorder{responses: []*http.Response{
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(failed))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(failed))},
	}}
	svc := newResponsesFixture(responsesFixtureInputs{compactModel: "gpt-5.4", transport: upstream})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1, Name: "openai-oauth", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Concurrency: 1,
			Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-provider"},
			Status:      billing.StatusActive, Schedulable: true,
		},
	}

	result, err := svc.Text.Passthrough(
		context.Background(), c, provider, body, body, "gpt-5.5", false, nil, true, time.Now(),
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Len(t, upstream.bodies, 2)
	var compactSignal *compact.Failure
	require.False(t, errors.As(err, &compactSignal))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "context window exceeded")
	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*ops.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 2)
	require.Equal(t, "retry", events[0].Kind)
	require.Equal(t, "compact_model_fallback", events[0].Reason)
	require.Equal(t, "http_error", events[1].Kind)
	require.True(t, events[1].Passthrough)
}

// TestOpenAIPassthroughAPIKeyRestoresClientToolsNonStreaming lowers client
// custom tools for a native API-key response. It checks outgoing declarations
// and restored custom call types and inputs in buffered JSON.
func TestOpenAIPassthroughAPIKeyRestoresClientToolsNonStreaming(t *testing.T) {
	body := openAIClientToolsRequest(false)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_tools","status":"completed","output":[
			{"type":"function_call","id":"i1","call_id":"c1","name":"exec","arguments":"{\"input\":\"pwd\"}"},
			{"type":"function_call","id":"i2","call_id":"c2","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"}],"usage":{}}`)),
	}}
	svc := openAIClientToolsTestService(upstream)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 5659, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}}

	result, err := svc.Text.Passthrough(context.Background(), c, provider, body, body, "gpt-5.4", false, nil, false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Empty(t, result.UpstreamResponseModel)
	assertOpenAIClientToolsLowered(t, upstream.lastBody)
	require.Equal(t, "custom_tool_call", gjson.Get(recorder.Body.String(), "output.0.type").String())
	require.Equal(t, "pwd", gjson.Get(recorder.Body.String(), "output.0.input").String())
	require.Equal(t, "custom_tool_call", gjson.Get(recorder.Body.String(), "output.1.type").String())
	require.Equal(t, "*** Begin Patch", gjson.Get(recorder.Body.String(), "output.1.input").String())
}

// TestOpenAIPassthroughAPIKeyPreservesCustomToolOutputContentParts supplies
// text and file parts in a custom tool result. It checks conversion to
// function_call_output with the upstream output array and its field values
// retained.
func TestOpenAIPassthroughAPIKeyPreservesCustomToolOutputContentParts(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","stream":false,"tools":[{"type":"custom","name":"exec"}],"input":[{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"result"},{"type":"input_file","file_id":"file_123"}]}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_tools","status":"completed","output":[],"usage":{}}`)),
	}}
	svc := openAIClientToolsTestService(upstream)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 6240, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}}

	result, err := svc.Text.Passthrough(context.Background(), c, provider, body, body, "gpt-5.4", false, nil, false, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "function_call_output", gjson.GetBytes(upstream.lastBody, "input.0.type").String())
	output := gjson.GetBytes(upstream.lastBody, "input.0.output")
	require.True(t, output.IsArray(), "native Responses content parts must reach the upstream as an array")
	require.Equal(t, "input_text", output.Get("0.type").String())
	require.Equal(t, "result", output.Get("0.text").String())
	require.Equal(t, "input_file", output.Get("1.type").String())
	require.Equal(t, "file_123", output.Get("1.file_id").String())
}

// TestOpenAIPassthroughAPIKeyRestoresClientToolsStreaming returns
// function-call stream events for a lowered custom tool. It checks restored
// custom-call events and string input, plus the final observed runtime model.
func TestOpenAIPassthroughAPIKeyRestoresClientToolsStreaming(t *testing.T) {
	body := openAIClientToolsRequest(true)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	sse := strings.Join([]string{
		`data: {"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"apply_patch","status":"in_progress"}}`,
		`data: {"type":"response.function_call_arguments.done","sequence_number":1,"item_id":"i1","call_id":"c1","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"}`,
		`data: {"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}","status":"completed"}}`,
		`data: {"type":"response.completed","sequence_number":3,"response":{"id":"resp_stream_tools","model":"runtime-version","status":"completed","output":[{"type":"function_call","id":"i1","call_id":"c1","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
	}, "\n\n") + "\n\n"
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}}
	svc := openAIClientToolsTestService(upstream)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 5660, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}}

	result, err := svc.Text.Passthrough(context.Background(), c, provider, body, body, "gpt-5.4", false, nil, true, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "runtime-version", result.UpstreamResponseModel)
	assertOpenAIClientToolsLowered(t, upstream.lastBody)
	output := recorder.Body.String()
	require.Contains(t, output, `"type":"custom_tool_call"`)
	require.Contains(t, output, `"type":"response.custom_tool_call_input.done"`)
	require.Contains(t, output, `"input":"*** Begin Patch"`)
	require.NotContains(t, output, `"input":{`)
}

// TestOpenAIUpstreamAccessStateClassification compares structured access-state
// codes with the listed free-form messages. It checks accepted cases'
// credential failure scope, next-provider action and client presentation
// alongside rejected classifications.
func TestOpenAIUpstreamAccessStateClassification(t *testing.T) {
	tests := []struct {
		name string // fixture case name
		body string // upstream error payload
		want bool   // expected access-state classification
	}{
		{"workspace_code", `{"detail":{"code":"deactivated_workspace"}}`, true},
		{"disabled_provider_message", `{"error":{"message":"Your provider is disabled"}}`, false},
		{"suspended_workspace_message", `{"response":{"error":{"message":"This workspace has been suspended"}}}`, false},
		{"deactivated_organization_message", `{"detail":{"message":"The organization is deactivated"}}`, false},
		{"scalar_detail", `{"detail":"This workspace has been disabled"}`, false},
		{"suspended_org_code", `{"error":{"code":"org_suspended"}}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			require.Equal(t, tt.want, gatewayprovider.IsOpenAIUpstreamAccessStateError("", body))
			if !tt.want {
				return
			}
			require.True(t, gatewayprovider.ShouldFailoverOpenAIResponse(http.StatusForbidden, "", body))
			require.True(t, shouldFailoverOpenAIPassthroughResponse(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Type: capability.ProviderTypeOAuth}}, http.StatusForbidden, body))

			err := gatewayprovider.NewOpenAIUpstreamFailure(http.StatusForbidden, nil, body, "", true)
			require.True(t, err.IsCredentialFailure())
			require.Equal(t, forwardcore.GatewayFailureScopeProvider, err.Scope)
			require.Equal(t, forwardcore.OpenAIUpstreamAccessStateReason, err.Reason)
			require.Equal(t, forwardcore.NextProviderRetry, err.NextProviderAction)
			require.False(t, err.RetryableOnSameProvider)
			require.False(t, err.RequestScopedTransient)
			require.Equal(t, http.StatusBadGateway, err.ClientStatusCode)
			require.Equal(t, "Upstream access is temporarily unavailable, please retry later", err.ClientMessage)
		})
	}
}

// TestOpenAIUpstreamAccessStateDoesNotScanEchoedJSON puts disabled-provider
// wording in an echo beside an ordinary invalid-input error. It checks that
// access-state and passthrough failover classification reject that unrelated
// text.
func TestOpenAIUpstreamAccessStateDoesNotScanEchoedJSON(t *testing.T) {
	body := []byte(`{"error":{"code":"invalid_request_error","message":"Invalid input"},"echo":{"prompt":"my provider is disabled"}}`)
	require.False(t, gatewayprovider.IsOpenAIUpstreamAccessStateError("", body))
	require.False(t, shouldFailoverOpenAIPassthroughResponse(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Type: capability.ProviderTypeOAuth}}, http.StatusBadRequest, body))
}

// TestOpenAIHTTPAccessStateDoesNotTrustBadRequestMessage mentions account
// disabling inside an unknown-parameter HTTP 400. It checks absence of
// access-state, failover and credential-failure classification for this
// request rejection.
func TestOpenAIHTTPAccessStateDoesNotTrustBadRequestMessage(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error","code":"unknown_parameter","message":"Unknown parameter: account disabled"}}`)

	require.False(t, gatewayprovider.IsOpenAIUpstreamAccessStateError("", body), "free-form stream messages are not durable provider evidence")
	require.False(t, gatewayprovider.IsOpenAIHTTPUpstreamAccessStateError(http.StatusBadRequest, "", body))
	require.False(t, gatewayprovider.ShouldFailoverOpenAIResponse(http.StatusBadRequest, "", body))
	require.False(t, shouldFailoverOpenAIPassthroughResponse(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Type: capability.ProviderTypeOAuth}}, http.StatusBadRequest, body))

	err := gatewayprovider.NewOpenAIUpstreamFailure(http.StatusBadRequest, nil, body, "", false)
	require.False(t, err.IsCredentialFailure())
}

// TestOpenAICyberPolicyWrapped5xxNeverFailsOver wraps a cyber-policy code
// inside an HTTP 502 failure. It checks that both general Responses and OAuth
// passthrough classifiers decline provider failover.
func TestOpenAICyberPolicyWrapped5xxNeverFailsOver(t *testing.T) {
	body := []byte(`{"error":{"code":"cyber_policy","message":"blocked"}}`)

	require.False(t, gatewayprovider.ShouldFailoverOpenAIResponse(http.StatusBadGateway, "wrapped upstream failure", body))
	require.False(t, shouldFailoverOpenAIPassthroughResponse(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Type: capability.ProviderTypeOAuth}}, http.StatusBadGateway, body))
}
