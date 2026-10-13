package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Native Responses preflight uses local counting when a selected target cannot
// provide the official count endpoint. The fixtures compare a custom relay,
// Grok, and an official API-key target returning 404, checking positive
// estimates and whether a network request was attempted.

// TestForwardResponsesInputTokensCustomRelayUsesLocalEstimate supplies a custom
// OpenAI relay with text and tools. It checks positive response.input_tokens
// output and confirms the recording transport receives no upstream request.
func TestForwardResponsesInputTokensCustomRelayUsesLocalEstimate(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)

	upstream := &auxiliaryHTTPRecorder{}
	svc := newAuxiliaryFixture(auxiliaryFixtureInputs{
		transport: upstream,
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 159, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "relay-key", "base_url": "https://relay.example/v1"},
		},
	}
	body := []byte(`{"model":"gpt-5.4","instructions":"Be concise.","input":"hello world","tools":[{"type":"function","name":"lookup","description":"Look up a value","parameters":{"type":"object"}}]}`)

	err := svc.ForwardResponsesInputTokens(context.Background(), c, provider, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "response.input_tokens", gjson.Get(recorder.Body.String(), "object").String())
	require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
	require.Nil(t, upstream.lastReq)
}

// TestForwardResponsesInputTokensGrokUsesLocalEstimate supplies Grok OAuth
// input to native preflight. It checks a successful response.input_tokens
// object and positive local count despite no configured upstream transport for
// this fixture.
func TestForwardResponsesInputTokensGrokUsesLocalEstimate(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)
	svc := newAuxiliaryFixture(auxiliaryFixtureInputs{})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 160, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}

	err := svc.ForwardResponsesInputTokens(context.Background(), c, provider, []byte(`{"model":"grok-4.1","input":"hello world"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "response.input_tokens", gjson.Get(recorder.Body.String(), "object").String())
	require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
}

// TestForwardResponsesInputTokensUpstream404FallsBackLocally returns a generic
// 404 from the official counting target. It checks an upstream request occurred
// and the client receives a successful native object with a positive local
// estimate.
func TestForwardResponsesInputTokensUpstream404FallsBackLocally(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)
	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"Invalid URL"}}`)),
	}}
	svc := newAuxiliaryFixture(auxiliaryFixtureInputs{
		transport: upstream,
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 171, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "official-key", "base_url": "https://api.openai.com/v1"},
		},
	}
	body := []byte(`{"model":"gpt-5.4","instructions":"Be concise.","input":"hello world"}`)

	err := svc.ForwardResponsesInputTokens(context.Background(), c, provider, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "response.input_tokens", gjson.Get(recorder.Body.String(), "object").String())
	require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
	require.NotNil(t, upstream.lastReq)
}
