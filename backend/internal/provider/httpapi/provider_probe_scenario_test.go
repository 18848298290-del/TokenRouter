package httpapi

// These scenarios cover test_events.go output and the provider test service invoked by test_handler.go.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

const compactionTestV2SSESuccessBody = "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"id\":\"cmp_probe\",\"encrypted_content\":\"blob\"}}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_probe\",\"output\":[]}}\n\n"

type cnProviderTestRepo struct {
	provider *providercore.Record
}

type cnProviderTestHTTP struct {
	request *http.Request
	body    string
}

type grokProviderTestRateLimitRepo struct {
	*grokTestStoreFixture
	rateLimitedCalls int
	resetAt          time.Time
}

// These fixtures record store writes and platform requests while using the production test service.
type grokTestStoreFixture struct {
	providersByID map[int64]*providercore.Record
}

type grokTestTransportFixture struct {
	resp     *http.Response
	lastReq  *http.Request
	lastBody []byte
}

type grokTestTargetLoader struct{ target providercore.TestTarget }

// grokTestFailureStoreFixture records health writes and call counts.
type grokTestFailureStoreFixture struct {
	tempUnschedCalls, rateLimitedCalls int
	lastRateLimitedID                  int64
	lastRateLimitResetAt               time.Time
}

type probeAgentStore struct {
	provideradapter.OpenAIProviderTestStore
	provider      *providercore.Record
	setErrorCalls int
}

type probeAgentInvalidations struct{ providerIDs []int64 }

type queuedHTTPUpstream struct {
	responses []*http.Response
	requests  []*http.Request
	tlsFlags  []bool
}

type openAIProbeStore struct {
	openAIProbeRecords
	updateExtraCalls   chan map[string]any
	updatedExtra       map[string]any
	bulkUpdatedIDs     []int64
	bulkUpdatedPayload providercore.ProviderBulkUpdate
	rateLimitedID      int64
	rateLimitedAt      *time.Time
	clearedErrorID     int64
	setErrorID         int64
	setErrorMsg        string
}

// openAIProbeOutput holds the HTTP request and response recorder.
type openAIProbeOutput struct {
	Request  *http.Request
	recorder *httptest.ResponseRecorder
}

type openAIProbeRecords struct {
	providersByID map[int64]*providercore.Record
}

type openAIProbeTransport struct {
	requests       []*http.Request
	bodies         [][]byte
	responses      []*http.Response
	resp           *http.Response
	err            error
	lastReq        *http.Request
	lastBody       []byte
	lastTLSProfile *tlsfingerprint.Profile
}

type automaticProbeProfileStore struct {
	egress.TLSFingerprintProfileRepository
	values []*egress.TLSFingerprintProfile
}

type automaticProbeRouterStore struct {
	egress.TLSFingerprintRouterRepository
	values []*egress.TLSFingerprintRouter
}

type qoderProviderTestSessionProviderStub struct {
	session     *qoder.SessionContext
	err         error
	invalidated []int64
}

type qoderProviderTestClientStub struct {
	request  *http.Request
	requests []*http.Request
	body     string
	bodies   [][]byte
	err      error
	headers  map[string]string
}

type qoderProviderTestOAuthClientStub struct {
	token string
	err   error
}

type qoderHTTPUpstreamRecorder struct {
	body                string
	userInfoBody        string
	userInfoStatusCode  int
	proxyURL            string
	providerID          int64
	providerConcurrency int
	profileSet          bool
	requests            []*http.Request
}

// These fixtures connect test targets and HTTP output to the production execution paths.
type qoderTestOutputFixture struct {
	context.Context
	recorder *httptest.ResponseRecorder
}

type qoderTargetFixture struct{ target providercore.TestTarget }

func TestProviderTestService_AdaptiveChatOnlyProvidersTestChatAndAnthropicEndpoints(t *testing.T) {
	provider := adaptiveCNProviderTestProvider(301, capability.PlatformZhipu)
	svc, upstream := adaptiveCNProviderTestService(
		provider,
		adaptiveCNChatTestResponse(),
		adaptiveCNAnthropicTestResponse(),
	)
	c, recorder := newTestContext()

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "glm-4.7", "hello", providercore.ProviderTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "http://chat.example/v1/chat/completions", upstream.requests[0].URL.String())
	require.Equal(t, "http://anthropic.example/v1/messages", upstream.requests[1].URL.String())
	require.Equal(t, "Bearer sk-adaptive-test", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "sk-adaptive-test", anthropic.GetHeaderRaw(upstream.requests[1].Header, "x-api-key"))
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_start"`))
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_complete"`))
	require.Contains(t, recorder.Body.String(), "已通过原生 /v1/messages 验证")
}

func TestProviderTestService_AdaptiveDeepSeekAlsoTestsResponsesEndpoint(t *testing.T) {
	provider := adaptiveCNProviderTestProvider(302, capability.PlatformDeepseek)
	svc, upstream := adaptiveCNProviderTestService(
		provider,
		adaptiveCNChatTestResponse(),
		adaptiveCNAnthropicTestResponse(),
		adaptiveCNResponsesTestResponse(),
	)
	c, recorder := newTestContext()

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "deepseek-chat", "", providercore.ProviderTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 3)
	require.Equal(t, "http://responses.example/responses", upstream.requests[2].URL.String())
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(upstream.requests[2].Context()))
	require.Equal(t, "Bearer sk-adaptive-test", upstream.requests[2].Header.Get("Authorization"))
	require.True(t, gjson.GetBytes(upstream.bodies[2], "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.bodies[2], "store").Bool())
	require.False(t, gjson.GetBytes(upstream.bodies[2], "instructions").Exists())
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_complete"`))
	require.Contains(t, recorder.Body.String(), "已通过原生 /responses 验证")
}

func TestProviderTestService_AdaptiveKimiAlsoTestsResponsesEndpoint(t *testing.T) {
	provider := adaptiveCNProviderTestProvider(306, capability.PlatformKimi)
	svc, upstream := adaptiveCNProviderTestService(
		provider,
		adaptiveCNChatTestResponse(),
		adaptiveCNAnthropicTestResponse(),
		adaptiveCNResponsesTestResponse(),
	)
	c, recorder := newTestContext()

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "k3-256k", "", providercore.ProviderTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 3)
	require.Equal(t, "http://responses.example/v1/responses", upstream.requests[2].URL.String())
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(upstream.requests[2].Context()))
	require.Equal(t, "Bearer sk-adaptive-test", upstream.requests[2].Header.Get("Authorization"))
	require.True(t, gjson.GetBytes(upstream.bodies[2], "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.bodies[2], "store").Bool())
	require.False(t, gjson.GetBytes(upstream.bodies[2], "instructions").Exists())
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_complete"`))
	require.Contains(t, recorder.Body.String(), "已通过原生 /responses 验证")
}

func TestProviderTestService_AdaptiveStopsAndNamesFailingEndpoint(t *testing.T) {
	provider := adaptiveCNProviderTestProvider(303, capability.PlatformDeepseek)
	svc, upstream := adaptiveCNProviderTestService(
		provider,
		adaptiveCNChatTestResponse(),
		newJSONResponse(http.StatusNotFound, `{"error":{"message":"missing messages route"}}`),
	)
	c, recorder := newTestContext()

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "deepseek-chat", "", providercore.ProviderTestModeDefault)

	require.Error(t, err)
	require.Contains(t, err.Error(), "Adaptive Anthropic endpoint returned 404")
	require.Len(t, upstream.requests, 2)
	require.Contains(t, recorder.Body.String(), `"type":"error"`)
	require.NotContains(t, recorder.Body.String(), `"type":"test_complete"`)
}

func TestProviderTestService_AdaptiveRejectsInvalidAnthropicSuccessBody(t *testing.T) {
	provider := adaptiveCNProviderTestProvider(305, capability.PlatformKimi)
	svc, upstream := adaptiveCNProviderTestService(
		provider,
		adaptiveCNChatTestResponse(),
		newJSONResponse(http.StatusOK, `<html>not an Anthropic stream</html>`),
	)
	c, recorder := newTestContext()

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "kimi-k2.5", "", providercore.ProviderTestModeDefault)

	require.Error(t, err)
	require.Contains(t, err.Error(), "Adaptive Anthropic stream ended before message_stop")
	require.Len(t, upstream.requests, 2)
	require.Contains(t, recorder.Body.String(), `"type":"error"`)
	require.NotContains(t, recorder.Body.String(), `"type":"test_complete"`)
}

func TestProviderTestService_FixedCNChatProtocolStillTestsOnlyChatEndpoint(t *testing.T) {
	provider := adaptiveCNProviderTestProvider(304, capability.PlatformZhipu)
	provider.Credentials["api_protocol"] = providercore.APIProtocolChatCompletions
	provider.Credentials["base_url"] = "http://fixed-chat.example/v1"
	svc, upstream := adaptiveCNProviderTestService(provider, adaptiveCNChatTestResponse())
	c, recorder := newTestContext()

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "glm-4.7", "", providercore.ProviderTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "http://fixed-chat.example/v1/chat/completions", upstream.requests[0].URL.String())
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_complete"`))
}

func TestProviderTestService_FixedCNAnthropicUsesNativeEndpointWithoutBetaQuery(t *testing.T) {
	provider := adaptiveCNProviderTestProvider(306, capability.PlatformZhipu)
	provider.Credentials["api_protocol"] = providercore.APIProtocolAnthropic
	provider.Credentials["base_url"] = "https://open.bigmodel.cn/api/anthropic"
	svc, upstream := adaptiveCNProviderTestService(provider, adaptiveCNAnthropicTestResponse())
	c, recorder := newTestContext()

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "glm-4.7", "", providercore.ProviderTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://open.bigmodel.cn/api/anthropic/v1/messages", upstream.requests[0].URL.String())
	require.Empty(t, upstream.requests[0].URL.RawQuery)
	require.Equal(t, "sk-adaptive-test", anthropic.GetHeaderRaw(upstream.requests[0].Header, "x-api-key"))
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
}

func TestProviderTestService_FixedCNAnthropicUsesProviderDefault(t *testing.T) {
	provider := adaptiveCNProviderTestProvider(307, capability.PlatformZhipu)
	provider.Credentials["api_protocol"] = providercore.APIProtocolAnthropic
	delete(provider.Credentials, "api_base_urls")
	svc, upstream := adaptiveCNProviderTestService(provider, adaptiveCNAnthropicTestResponse())
	c, _ := newTestContext()

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "glm-4.7", "", providercore.ProviderTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://open.bigmodel.cn/api/anthropic/v1/messages", upstream.requests[0].URL.String())
}

func TestProviderTestService_FixedCNAnthropicRejectsOpenAIBaseURLAndMarksAuthErrors(t *testing.T) {
	t.Run("misconfigured base URL", func(t *testing.T) {
		provider := adaptiveCNProviderTestProvider(308, capability.PlatformZhipu)
		provider.Credentials["api_protocol"] = providercore.APIProtocolAnthropic
		provider.Credentials["base_url"] = "https://open.bigmodel.cn/api/paas/v4"
		svc, upstream := adaptiveCNProviderTestService(provider)
		c, recorder := newTestContext()

		err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "glm-4.7", "", providercore.ProviderTestModeDefault)

		require.Error(t, err)
		require.Empty(t, upstream.requests)
		require.Contains(t, recorder.Body.String(), "looks like an OpenAI-compatible endpoint")
	})

	t.Run("forbidden marks provider error", func(t *testing.T) {
		provider := adaptiveCNProviderTestProvider(309, capability.PlatformZhipu)
		provider.Credentials["api_protocol"] = providercore.APIProtocolAnthropic
		provider.Credentials["base_url"] = "https://open.bigmodel.cn/api/anthropic"
		svc, _ := adaptiveCNProviderTestService(provider, newJSONResponse(http.StatusForbidden, `{"error":{"message":"invalid key"}}`))
		c, _ := newTestContext()

		err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "glm-4.7", "", providercore.ProviderTestModeDefault)

		require.Error(t, err)
		repo := testassert.MustType[*openAIProbeStore](svc.Store)
		require.Equal(t, provider.ID, repo.setErrorID)
	})
}

func TestProviderTestService_AdaptiveSelectedProtocolTestsOnlyThatEndpoint(t *testing.T) {
	provider := adaptiveCNProviderTestProvider(310, capability.PlatformDeepseek)
	svc, upstream := adaptiveCNProviderTestService(provider, adaptiveCNAnthropicTestResponse())
	c, recorder := newTestContext()

	err := executeOpenAIProbeRequestType(t, svc, c, provider.ID, "deepseek-chat", "", providercore.ProviderTestTypeText, providercore.ProviderTestModeDefault, providercore.APIProtocolAnthropic)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "http://anthropic.example/v1/messages", upstream.requests[0].URL.String())
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_start"`))
	require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"test_complete"`))
}

func TestProviderTestService_SelectedProtocolMustBeEnabled(t *testing.T) {
	provider := adaptiveCNProviderTestProvider(311, capability.PlatformZhipu)
	provider.Credentials[providercore.UpstreamProtocolsKey] = []any{string(protocol.ProtocolOpenAIChatCompletions)}
	svc, upstream := adaptiveCNProviderTestService(provider)
	c, recorder := newTestContext()

	err := executeOpenAIProbeRequestType(t, svc, c, provider.ID, "glm-4.7", "", providercore.ProviderTestTypeText, providercore.ProviderTestModeDefault, providercore.APIProtocolAnthropic)

	require.Error(t, err)
	require.Empty(t, upstream.requests)
	require.Contains(t, recorder.Body.String(), "Test protocol anthropic is not supported for this provider")
}

func TestProviderTestServiceCNProviderUsesConfiguredProtocol(t *testing.T) {
	tests := []struct {
		name       string
		provider   *providercore.Record
		wantPath   string
		wantModel  string
		wantHeader string
	}{
		{
			name: "Kimi Chat Completions",
			provider: &providercore.Record{
				ID: 1, Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "kimi-key", "base_url": "https://relay.example/v1"},
			},
			wantPath:   "/v1/chat/completions",
			wantModel:  "kimi-k2.5",
			wantHeader: "Authorization",
		},
		{
			name: "智谱 Anthropic Messages",
			provider: &providercore.Record{
				ID: 2, Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "zhipu-key", "api_protocol": providercore.APIProtocolAnthropic, "base_url": "https://relay.example/anthropic"},
			},
			wantPath:   "/anthropic/v1/messages",
			wantModel:  "glm-4.7",
			wantHeader: "x-api-key",
		},
		{
			name: "DeepSeek Responses",
			provider: &providercore.Record{
				ID: 3, Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "deepseek-key", "api_protocol": providercore.APIProtocolResponses, "base_url": "https://relay.example"},
			},
			wantPath: "/v1/responses",
			// Responses providers use the OpenAI probe and its default model when none is supplied.
			wantModel:  openai.DefaultTestModel,
			wantHeader: "Authorization",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &cnProviderTestRepo{provider: test.provider}
			upstream := &cnProviderTestHTTP{}
			service := newCNProtocolTestCore(repo.GetByID, upstream)
			recorder := httptest.NewRecorder()
			require.Error(t, service.Test(t.Context(), providercore.TestRequest{ProviderID: test.provider.ID, Prompt: "hi", Mode: providercore.ProviderTestModeDefault}, NewTestEventSink(recorder)))

			require.NotNil(t, upstream.request)
			require.Equal(t, "relay.example", upstream.request.URL.Hostname())
			require.Equal(t, test.wantPath, upstream.request.URL.Path)
			require.NotEmpty(t, anthropic.GetHeaderRaw(upstream.request.Header, test.wantHeader))
			require.Equal(t, test.wantModel, gjson.Get(upstream.body, "model").String())
		})
	}
}

func TestProviderTestService_TestProviderConnection_GrokUsesXAIResponses(t *testing.T) {
	provider := &providercore.Record{
		ID:          13,
		Name:        "grok-oauth",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "grok-access-token",
			"refresh_token": "grok-refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			"model_mapping": map[string]any{
				"grok": "grok-latest",
			},
		},
	}
	repo := &grokTestStoreFixture{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}
	upstream := &grokTestTransportFixture{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\"}\n\n",
		)),
	}}
	svc := &provideradapter.GrokProviderTest{
		Store:     repo,
		Tokens:    &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()},
		Transport: upstream,
	}

	rec := httptest.NewRecorder()

	err := executeGrokProviderTest(t, svc, provider, rec, "grok", "", providercore.ProviderTestModeDefault)
	require.NoError(t, err)

	require.Equal(t, "https://cli-chat-proxy.grok.com/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer grok-access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, xai.CLIClientVersion, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.Equal(t, "application/json, text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "grok-latest", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "hi", gjson.GetBytes(upstream.lastBody, "input").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "max_output_tokens").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Exists())
	require.NotContains(t, rec.Body.String(), "claude")
	require.Contains(t, rec.Body.String(), `"model":"grok-latest"`)
	require.Contains(t, rec.Body.String(), `"type":"test_complete"`)
}

func TestProviderTestService_TestProviderConnection_GrokUsesCustomTextPrompt(t *testing.T) {
	provider := &providercore.Record{
		ID:          17,
		Name:        "grok-custom-prompt",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "grok-access-token",
			"refresh_token": "grok-refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	repo := &grokTestStoreFixture{providersByID: map[int64]*providercore.Record{provider.ID: provider}}
	upstream := &grokTestTransportFixture{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n")),
	}}
	svc := &provideradapter.GrokProviderTest{
		Store:     repo,
		Tokens:    &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()},
		Transport: upstream,
	}
	recorder := httptest.NewRecorder()

	err := executeGrokProviderTest(t, svc, provider, recorder, "grok-4.5", "describe this provider in one sentence", providercore.ProviderTestModeDefault, providercore.ProviderTestTypeText)
	require.NoError(t, err)
	require.Equal(t, "describe this provider in one sentence", gjson.GetBytes(upstream.lastBody, "input").String())
}

func TestProviderTestService_TestProviderConnection_GrokExplicitImageUsesMediaEndpoint(t *testing.T) {
	provider := &providercore.Record{
		ID:          18,
		Name:        "grok-image-api-key",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "grok-api-key"},
	}
	repo := &grokTestStoreFixture{providersByID: map[int64]*providercore.Record{provider.ID: provider}}
	upstream := &grokTestTransportFixture{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aGVsbG8=","mime_type":"image/png"}]}`)),
	}}
	svc := &provideradapter.GrokProviderTest{Store: repo, Transport: upstream, OperatorValidator: (egress.OperatorURLPolicy{}).Validate}
	recorder := httptest.NewRecorder()

	err := executeGrokProviderTest(t, svc, provider, recorder, "custom-image-alias", "draw a lighthouse", providercore.ProviderTestModeDefault, providercore.ProviderTestTypeImage)
	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer grok-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "custom-image-alias", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "draw a lighthouse", gjson.GetBytes(upstream.lastBody, "prompt").String())
	require.Contains(t, recorder.Body.String(), "data:image/png;base64,aGVsbG8=")
}

func TestProviderTestService_TestProviderConnection_GrokDefaultsEmptyModelTo45(t *testing.T) {
	provider := &providercore.Record{
		ID:          16,
		Name:        "grok-oauth-default-model",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "grok-access-token",
			"refresh_token": "grok-refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			// The default model is selected after provider mapping, so this mapping key is skipped for empty input.
			"model_mapping": map[string]any{"grok-4.5": "grok-4.3"},
		},
	}
	repo := &grokTestStoreFixture{providersByID: map[int64]*providercore.Record{provider.ID: provider}}
	upstream := &grokTestTransportFixture{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\"}\n\n",
		)),
	}}
	svc := &provideradapter.GrokProviderTest{
		Store:     repo,
		Tokens:    &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()},
		Transport: upstream,
	}
	recorder := httptest.NewRecorder()

	err := executeGrokProviderTest(t, svc, provider, recorder, "", "", providercore.ProviderTestModeDefault)

	require.NoError(t, err)
	require.Equal(t, xai.DefaultResponsesModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, recorder.Body.String(), `"model":"grok-4.5"`)
}

func TestProviderTestService_Grok429PersistsRateLimitReset(t *testing.T) {
	provider := &providercore.Record{
		ID:          14,
		Name:        "grok-oauth-limited",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "grok-access-token",
			"refresh_token": "grok-refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	baseRepo := &grokTestStoreFixture{providersByID: map[int64]*providercore.Record{provider.ID: provider}}
	repo := &grokProviderTestRateLimitRepo{grokTestStoreFixture: baseRepo}
	upstream := &grokTestTransportFixture{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"45"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	svc := &provideradapter.GrokProviderTest{
		Store:     repo,
		Tokens:    &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()},
		Transport: upstream,
	}
	recorder := httptest.NewRecorder()

	err := executeGrokProviderTest(t, svc, provider, recorder, "grok", "", providercore.ProviderTestModeDefault)

	require.Error(t, err)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, time.Now().Add(45*time.Second), repo.resetAt, time.Second)
}

func TestProviderTestService_Grok429WithoutQuotaHeadersUsesFallback(t *testing.T) {
	provider := &providercore.Record{
		ID: 15, Name: "grok-oauth-limited-no-headers", Platform: capability.PlatformGrok,
		Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "grok-access-token",
			"refresh_token": "grok-refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	baseRepo := &grokTestStoreFixture{providersByID: map[int64]*providercore.Record{provider.ID: provider}}
	repo := &grokProviderTestRateLimitRepo{grokTestStoreFixture: baseRepo}
	upstream := &grokTestTransportFixture{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"quota exhausted"}}`)),
	}}
	svc := &provideradapter.GrokProviderTest{
		Store: repo, Tokens: &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, Transport: upstream,
	}
	recorder := httptest.NewRecorder()
	before := time.Now()

	err := executeGrokProviderTest(t, svc, provider, recorder, "grok", "", providercore.ProviderTestModeDefault)

	require.Error(t, err)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, before.Add(2*time.Minute), repo.resetAt, time.Second)
}

func TestProviderTestServiceGrokAPIKeyUsesXAIResponses(t *testing.T) {
	provider := &providercore.Record{
		ID:          54,
		Name:        "grok-api-key",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeAPIKey,
		Concurrency: 2,
		Credentials: map[string]any{
			"api_key":  "xai-test-key",
			"base_url": "https://api.x.ai/v1",
		},
	}
	upstream := &grokTestTransportFixture{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\"}\n\n",
		)),
	}}
	svc := &provideradapter.GrokProviderTest{Transport: upstream}
	recorder := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: recorder}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/54/test", nil)

	err := executeGrokProbe(t, svc, c, provider, "grok")
	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer xai-test-key", upstream.lastReq.Header.Get("Authorization"))
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
}

func TestProviderTestServiceGrokAPIKeyAllowsConfiguredHTTPWhenGlobalPolicyDoes(t *testing.T) {
	provider := &providercore.Record{
		ID:          55,
		Name:        "grok-api-key-http",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "third-party-key",
			"base_url": "http://grok.example.test/v1",
		},
	}
	upstream := &grokTestTransportFixture{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\"}\n\n",
		)),
	}}
	svc := &provideradapter.GrokProviderTest{OperatorValidator: (egress.OperatorURLPolicy{AllowInsecureHTTP: true}).Validate, Transport: upstream}
	recorder := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: recorder}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/55/test", nil)

	err := executeGrokProbe(t, svc, c, provider, "grok")
	require.NoError(t, err)
	require.Equal(t, "http://grok.example.test/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer third-party-key", upstream.lastReq.Header.Get("Authorization"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
}

func TestProviderTestServiceGrokOAuthPaymentRequiredTemporarilyUnschedulesProvider(t *testing.T) {
	provider := healthyGrokOAuthGatewayTestProvider(56, "access-token")
	repo := &grokTestFailureStoreFixture{}
	upstream := &grokTestTransportFixture{resp: &http.Response{
		StatusCode: http.StatusPaymentRequired,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"code":"personal-team-blocked:spending-limit"}`)),
	}}
	svc := &provideradapter.GrokProviderTest{
		Store:     repo,
		Tokens:    &providercore.GrokTokenSource{},
		Transport: upstream,
	}
	recorder := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: recorder}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/56/test", nil)
	before := time.Now()

	err := executeGrokProbe(t, svc, c, provider, "grok")

	require.Error(t, err)
	require.Zero(t, repo.tempUnschedCalls)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Equal(t, provider.ID, repo.lastRateLimitedID)
	require.WithinDuration(t, before.Add(10*time.Minute), repo.lastRateLimitResetAt, time.Second)
	require.Contains(t, recorder.Body.String(), `"type":"error"`)
	require.Contains(t, recorder.Body.String(), "Grok Responses API returned 402")
}

func TestProviderTestServiceOpenAICompactAgentIdentityUsesFreshAssertion(t *testing.T) {
	key, privateKey := newProbeAgentKey(t)
	provider := providercore.Record{
		ID:          21,
		Name:        "agent-identity",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"auth_mode":                  providercore.OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":           key.RuntimeID,
			"agent_private_key":          privateKey,
			"task_id":                    key.TaskID,
			"chatgpt_account_id":         "provider-agent-test",
			"chatgpt_account_is_fedramp": true,
		},
	}
	repo := &probeAgentStore{provider: &provider}
	upstream := &openAIProbeTransport{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(compactionTestV2SSESuccessBody)),
	}}
	svc := &provideradapter.OpenAIProviderTest{Store: repo, Transport: upstream, EnsureTask: probeAgentTasks(repo, "", nil).Ensure}

	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/21/test", bytes.NewReader(nil))

	require.NoError(t, executeOpenAIProbeRequest(t, svc, c, provider.ID, "gpt-5.4", "", providercore.ProviderTestModeCompact))
	require.Equal(t, "AgentAssertion", strings.SplitN(upstream.lastReq.Header.Get("Authorization"), " ", 2)[0])
	require.Equal(t, "provider-agent-test", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "true", upstream.lastReq.Header.Get("x-openai-fedramp"))
	require.NotContains(t, upstream.lastReq.Header.Get("Authorization"), privateKey)
}

func TestProviderTestServiceOpenAICompactAgentIdentityRecoversInvalidTaskOnce(t *testing.T) {
	key, privateKey := newProbeAgentKey(t)
	provider := &providercore.Record{
		ID:          22,
		Name:        "agent-identity-recovery",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"auth_mode":          providercore.OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":   key.RuntimeID,
			"agent_private_key":  privateKey,
			"task_id":            "task-compact-old",
			"chatgpt_account_id": "provider-agent-compact-recovery",
		},
	}
	repo := &probeAgentStore{provider: provider}
	registerCalls := 0
	registerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registerCalls++
		_, _ = io.WriteString(w, `{"task_id":"task-compact-new"}`)
	}))
	defer registerServer.Close()

	upstream := &openAIProbeTransport{responses: []*http.Response{
		{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(compactionTestV2SSESuccessBody))},
	}}
	invalidator := &probeAgentInvalidations{}
	svc := &provideradapter.OpenAIProviderTest{Store: repo, Transport: upstream, EnsureTask: probeAgentTasks(repo, registerServer.URL, invalidator).Ensure}
	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/22/test", bytes.NewReader(nil))

	require.NoError(t, executeOpenAIProbeRequest(t, svc, c, provider.ID, "gpt-5.4", "", providercore.ProviderTestModeCompact))
	require.Equal(t, 1, registerCalls)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "task-compact-new", provider.GetCredential("task_id"))
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, []int64{provider.ID}, invalidator.providerIDs)
}

func TestProviderTestService_TestProviderConnection_OpenAICompactOAuthUsesNativeV2AndDoesNotPersistSupport(t *testing.T) {
	updateCalls := make(chan map[string]any, 1)
	provider := providercore.Record{
		ID:          1,
		Name:        "openai-oauth",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}
	repo := &openAIProbeStore{openAIProbeRecords: openAIProbeRecords{providersByID: map[int64]*providercore.Record{provider.ID: &provider}}, updateExtraCalls: updateCalls}
	upstream := &openAIProbeTransport{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid-probe"}},
		Body:       io.NopCloser(strings.NewReader(compactionTestV2SSESuccessBody)),
	}}
	svc := &provideradapter.OpenAIProviderTest{
		Store:     repo,
		Transport: upstream,
	}

	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/1/test", bytes.NewReader(nil))

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "gpt-5.4", "", providercore.ProviderTestModeCompact)
	require.NoError(t, err)

	require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", upstream.lastReq.URL.String())
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, openai.CodexCLIVersion, upstream.lastReq.Header.Get("Version"))
	require.NotEmpty(t, upstream.lastReq.Header.Get("Session_Id"))
	require.Contains(t, upstream.lastReq.Header.Get("x-codex-beta-features"), "remote_compaction_v2")
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, openai.CodexCLIUserAgent, upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, openai.CodexDefaultOriginator, upstream.lastReq.Header.Get("Originator"))
	require.Equal(t, "chatgpt-acc", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	input := gjson.GetBytes(upstream.lastBody, "input").Array()
	require.NotEmpty(t, input)
	require.Equal(t, "compaction_trigger", input[len(input)-1].Get("type").String())

	require.Empty(t, updateCalls, "手动压缩测试不应写入能力状态")
	require.Contains(t, rec.Body.String(), `"type":"test_complete"`)
}

func TestProviderTestService_TestProviderConnection_OpenAICompactOAuth404DoesNotChangeCapability(t *testing.T) {
	updateCalls := make(chan map[string]any, 1)
	provider := providercore.Record{
		ID:          2,
		Name:        "openai-oauth",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"openai_compact_mode": "force_on",
		},
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}
	repo := &openAIProbeStore{openAIProbeRecords: openAIProbeRecords{providersByID: map[int64]*providercore.Record{provider.ID: &provider}}, updateExtraCalls: updateCalls}
	upstream := &openAIProbeTransport{resp: &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`404 page not found`)),
	}}
	svc := &provideradapter.OpenAIProviderTest{
		Store:     repo,
		Transport: upstream,
	}

	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/2/test", bytes.NewReader(nil))

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "gpt-5.4", "", providercore.ProviderTestModeCompact)
	require.Error(t, err)

	require.Empty(t, updateCalls, "手动压缩测试不应写入能力状态")
	require.Contains(t, rec.Body.String(), `"type":"error"`)
}

func TestProviderTestService_TestProviderConnection_OpenAICompactAPIKeyUsesNativeResponsesPath(t *testing.T) {
	updateCalls := make(chan map[string]any, 1)
	provider := providercore.Record{
		ID:          3,
		Name:        "openai-apikey",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":               "sk-test",
			"base_url":              "https://example.com/v1",
			"compact_model_mapping": map[string]any{"gpt-5.4": "gpt-5.4-openai-compact"},
		},
	}
	repo := &openAIProbeStore{openAIProbeRecords: openAIProbeRecords{providersByID: map[int64]*providercore.Record{provider.ID: &provider}}, updateExtraCalls: updateCalls}
	upstream := &openAIProbeTransport{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(compactionTestV2SSESuccessBody)),
	}}
	svc := &provideradapter.OpenAIProviderTest{
		Store:       repo,
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{}).Validate,
	}

	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/3/test", bytes.NewReader(nil))

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "gpt-5.4", "", providercore.ProviderTestModeCompact)
	require.NoError(t, err)

	require.Equal(t, "https://example.com/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, upstream.lastReq.Header.Get("x-codex-beta-features"), "remote_compaction_v2")
	require.Empty(t, updateCalls, "手动压缩测试不应写入能力状态")
}

func TestProviderTestService_TestProviderConnection_OpenAICompactAPIKeyDefaultBaseURLUsesResponsesPath(t *testing.T) {
	updateCalls := make(chan map[string]any, 1)
	provider := providercore.Record{
		ID:          4,
		Name:        "openai-apikey-default",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
	}
	repo := &openAIProbeStore{openAIProbeRecords: openAIProbeRecords{providersByID: map[int64]*providercore.Record{provider.ID: &provider}}, updateExtraCalls: updateCalls}
	upstream := &openAIProbeTransport{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(compactionTestV2SSESuccessBody)),
	}}
	svc := &provideradapter.OpenAIProviderTest{
		Store:       repo,
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{}).Validate,
	}

	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/4/test", bytes.NewReader(nil))

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "gpt-5.4", "", providercore.ProviderTestModeCompact)
	require.NoError(t, err)
	require.Equal(t, "https://api.openai.com/v1/responses", upstream.lastReq.URL.String())
	require.Empty(t, updateCalls)
}

func TestProviderTestService_TestProviderConnection_OpenAILegacyCompactUsesDedicatedPathWithoutState(t *testing.T) {
	updateCalls := make(chan map[string]any, 1)
	provider := providercore.Record{
		ID:          5,
		Name:        "openai-legacy-compact",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":               "sk-test",
			"base_url":              "https://example.com/v1",
			"compact_model_mapping": map[string]any{"gpt-5.4": "gpt-5.4-openai-compact"},
		},
	}
	repo := &openAIProbeStore{openAIProbeRecords: openAIProbeRecords{providersByID: map[int64]*providercore.Record{provider.ID: &provider}}, updateExtraCalls: updateCalls}
	upstream := &openAIProbeTransport{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"legacy_compact_probe","status":"completed"}`)),
	}}
	svc := &provideradapter.OpenAIProviderTest{
		Store:       repo,
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{}).Validate,
	}

	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/5/test", bytes.NewReader(nil))

	err := executeOpenAIProbeRequest(t, svc, c, provider.ID, "gpt-5.4", "", providercore.ProviderTestModeLegacyCompact)
	require.NoError(t, err)
	require.Equal(t, "https://example.com/v1/responses/compact", upstream.lastReq.URL.String())
	require.Equal(t, "gpt-5.4-openai-compact", gjson.GetBytes(upstream.lastBody, "model").String())

	require.Empty(t, updateCalls, "手动压缩测试不应写入能力状态")
}

// TestManualCompactionTestsPreserveConfigurationAcrossOutcomes checks authentication failures, rate limits, and administrator capability settings.
func TestManualCompactionTestsPreserveConfigurationAcrossOutcomes(t *testing.T) {
	for _, mode := range []string{providercore.ProviderTestModeCompact, providercore.ProviderTestModeLegacyCompact} {
		for _, status := range []int{200, 401, 404, 429} {
			t.Run(fmt.Sprintf("%s/%d", mode, status), func(t *testing.T) {
				extra := map[string]any{"openai_compact_mode": "force_off", providercore.OpenAINativeCompactionV2ModeExtraKey: "force_on"}
				provider := &providercore.Record{
					ID: 13, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive,
					Credentials: map[string]any{"access_token": "test"}, Extra: extra,
				}
				repo := &openAIProbeStore{}
				body := compactionTestV2SSESuccessBody
				if mode == providercore.ProviderTestModeLegacyCompact {
					body = `{"id":"legacy_test","status":"completed"}`
				}
				if status != 200 {
					body = fmt.Sprintf(`{"error":{"type":"usage_limit_reached","message":"test failure","resets_at":%d}}`, time.Now().Add(time.Hour).Unix())
				}
				resp := newJSONResponse(status, body)
				upstream := &openAIProbeTransport{resp: resp}
				svc := &provideradapter.OpenAIProviderTest{Store: repo, Transport: upstream}
				c, _ := newTestContext()
				err := executeOpenAIProbe(t, svc, c, provider, "gpt-5.4", "", mode)
				if status == 200 {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				require.Equal(t, "force_off", provider.Extra["openai_compact_mode"])
				require.Equal(t, "force_on", provider.Extra[providercore.OpenAINativeCompactionV2ModeExtraKey])
				for _, key := range providercore.DeprecatedOpenAIProviderExtraKeys {
					require.NotContains(t, repo.updatedExtra, key)
				}
				require.NotContains(t, repo.updatedExtra, "openai_compact_mode")
				require.NotContains(t, repo.updatedExtra, providercore.OpenAINativeCompactionV2ModeExtraKey)
				if status == 401 {
					require.Equal(t, provider.ID, repo.setErrorID)
				}
				if status == 429 {
					require.Equal(t, provider.ID, repo.rateLimitedID)
				}
			})
		}
	}
}

func TestProviderTestService_OpenAIImageOAuthHandlesOutputItemDoneFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/1/test", nil)

	upstream := &openAIProbeTransport{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_123\",\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"revised_prompt\":\"draw a cat\",\"output_format\":\"png\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000006,\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc := &provideradapter.OpenAIProviderTest{Transport: upstream}
	provider := &providercore.Record{
		ID:       53,
		Name:     "openai-oauth",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	err := executeOpenAIImageOAuthProbe(t, svc, c, context.Background(), provider, "gpt-image-2", "draw a cat")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Contains(t, rec.Body.String(), "Calling Codex /responses image tool")
	require.Contains(t, rec.Body.String(), "data:image/png;base64,aGVsbG8=")
	require.Contains(t, rec.Body.String(), "\"success\":true")
}

func TestProviderTestService_OpenAIImageOAuthForwardsTLSProfile(t *testing.T) {
	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/1/test", nil)

	upstream := &openAIProbeTransport{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_456\",\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"output_format\":\"png\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc := &provideradapter.OpenAIProviderTest{
		Transport:  upstream,
		ResolveTLS: (&provideradapter.OpenAIProbePolicy{ManualProfiles: &egressadapter.TLSProfiles{}}).ResolveTestTLS,
	}
	provider := &providercore.Record{
		ID:       55,
		Name:     "openai-oauth-tls",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
		Extra: map[string]any{
			"enable_tls_fingerprint": true,
		},
	}

	err := executeOpenAIImageOAuthProbe(t, svc, c, context.Background(), provider, "gpt-image-2", "draw a cat")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastTLSProfile)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
}

func TestProviderTestService_OpenAIImageAPIKeyUsesConfiguredV1BaseURL(t *testing.T) {
	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/1/test", nil)

	upstream := &openAIProbeTransport{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aGVsbG8=","revised_prompt":"draw a cat"}]}`)),
		},
	}
	svc := &provideradapter.OpenAIProviderTest{
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{}).Validate,
	}
	provider := &providercore.Record{
		ID:       54,
		Name:     "openai-apikey",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://image-upstream.example/v1",
		},
	}

	err := executeOpenAIImageAPIKeyProbe(t, svc, c, context.Background(), provider, "gpt-image-2", "draw a cat")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, "https://image-upstream.example/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer test-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Contains(t, rec.Body.String(), "data:image/png;base64,aGVsbG8=")
	require.Contains(t, rec.Body.String(), "\"success\":true")
}

func TestProviderTestService_OpenAIExplicitImageTypeDoesNotInspectModelName(t *testing.T) {
	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/1/test", nil)

	upstream := &openAIProbeTransport{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aGVsbG8="}]}`)),
		},
	}
	svc := &provideradapter.OpenAIProviderTest{
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{}).Validate,
	}
	provider := &providercore.Record{
		ID:       56,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://image-upstream.example/v1",
		},
	}

	err := executeOpenAIProbe(t, svc, c, provider, "custom-model-alias", "draw a lighthouse", providercore.ProviderTestModeDefault, providercore.ProviderTestTypeImage)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://image-upstream.example/v1/images/generations", upstream.lastReq.URL.String())
	body, err := io.ReadAll(upstream.lastReq.Body)
	require.NoError(t, err)
	require.Equal(t, "custom-model-alias", gjson.GetBytes(body, "model").String())
	require.Equal(t, "draw a lighthouse", gjson.GetBytes(body, "prompt").String())
}

func TestProviderTestService_OpenAIExplicitTextTypeDoesNotInspectModelName(t *testing.T) {
	rec := httptest.NewRecorder()
	c := &openAIProbeOutput{recorder: rec}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/1/test", nil)

	upstream := &openAIProbeTransport{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n")),
		},
	}
	svc := &provideradapter.OpenAIProviderTest{
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{}).Validate,
	}
	provider := &providercore.Record{
		ID:       57,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://text-upstream.example/v1",
		},
	}

	err := executeOpenAIProbe(t, svc, c, provider, "gpt-image-2", "reply briefly", providercore.ProviderTestModeDefault, providercore.ProviderTestTypeText)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://text-upstream.example/v1/responses", upstream.lastReq.URL.String())
	body, err := io.ReadAll(upstream.lastReq.Body)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", gjson.GetBytes(body, "model").String())
	require.Equal(t, "reply briefly", gjson.GetBytes(body, "input.0.content.0.text").String())
}

func TestProviderTestService_OpenAIImageAPIKeyHandlesURLAndEmptyImages(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
		want    string
	}{
		{"url 返回", `{"data":[{"url":"https://cdn.example/cat.png"}]}`, false, `"image_url":"https://cdn.example/cat.png"`},
		{"没有图片数据", `{"data":[{"revised_prompt":"draw a cat"}]}`, true, "Upstream returned no image data"},
		{"拒绝非 https 链接", `{"data":[{"url":"javascript:alert(1)"}]}`, true, "Upstream returned no image data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := &openAIProbeOutput{recorder: rec}
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/1/test", nil)
			upstream := &openAIProbeTransport{
				resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(tc.body)),
				},
			}
			svc := &provideradapter.OpenAIProviderTest{Transport: upstream, ValidateURL: (egress.OperatorURLPolicy{}).Validate}
			provider := &providercore.Record{
				ID:          55,
				Name:        "openai-apikey",
				Platform:    capability.PlatformOpenAI,
				Type:        capability.ProviderTypeAPIKey,
				Credentials: map[string]any{"api_key": "test-api-key", "base_url": "https://image-upstream.example/v1"},
			}

			err := executeOpenAIImageAPIKeyProbe(t, svc, c, context.Background(), provider, "gpt-image-2", "draw a cat")
			require.Equal(t, tc.wantErr, err != nil)
			require.Contains(t, rec.Body.String(), tc.want)
			require.Equal(t, !tc.wantErr, strings.Contains(rec.Body.String(), `"success":true`))
		})
	}
}

// TestProviderTestServiceSkipsShadow checks that shadow probes resolve credentials through the parent.
func TestProviderTestServiceSkipsShadow(t *testing.T) {
	pid := int64(100)
	shadow := &providercore.Record{
		ID:               200,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &pid,
	}
	repo := &openAIProbeStore{openAIProbeRecords: openAIProbeRecords{providersByID: map[int64]*providercore.Record{shadow.ID: shadow}}}
	svc := &provideradapter.OpenAIProviderTest{Store: repo}
	c, _ := newTestContext()

	err := executeOpenAIProbeRequest(t, svc, c, 200, "", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "resolve spark shadow parent")
}

func TestProviderTestService_OpenAISuccessPersistsSnapshotFromHeaders(t *testing.T) {
	ctx, recorder := newTestContext()

	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader(`data: {"type":"response.completed"}

`))
	resp.Header.Set("x-codex-primary-used-percent", "88")
	resp.Header.Set("x-codex-primary-reset-after-seconds", "604800")
	resp.Header.Set("x-codex-primary-window-minutes", "10080")
	resp.Header.Set("x-codex-secondary-used-percent", "42")
	resp.Header.Set("x-codex-secondary-reset-after-seconds", "18000")
	resp.Header.Set("x-codex-secondary-window-minutes", "300")

	repo := &openAIProbeStore{}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{Store: repo, Transport: upstream}
	provider := &providercore.Record{
		ID:          89,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	req := upstream.requests[0]
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(req.Context()))
	require.Equal(t, "responses=experimental", req.Header.Get("OpenAI-Beta"))
	require.Equal(t, openai.CodexDefaultOriginator, req.Header.Get("Originator"))
	require.Equal(t, openai.CodexCLIUserAgent, req.Header.Get("User-Agent"))
	require.NotEmpty(t, repo.updatedExtra)
	require.Equal(t, 42.0, repo.updatedExtra["codex_5h_used_percent"])
	require.Equal(t, 88.0, repo.updatedExtra["codex_7d_used_percent"])
	require.Contains(t, recorder.Body.String(), "test_complete")
}

// TestProviderTestService_OpenAIOAuthTestDoesNotRedirectBareGPT56 checks that an unknown model name passes through unchanged.
func TestProviderTestService_OpenAIOAuthTestDoesNotRedirectBareGPT56(t *testing.T) {
	ctx, _ := newTestContext()

	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader(`data: {"type":"response.completed"}

`))

	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{Transport: upstream}
	provider := &providercore.Record{
		ID:          90,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.6", "connection probe", "")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)

	body, err := io.ReadAll(upstream.requests[0].Body)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6", gjson.GetBytes(body, "model").String())
	require.Equal(t, "connection probe", gjson.GetBytes(body, "input.0.content.0.text").String())
}

func TestProviderTestService_OpenAIOAuthUsesConfiguredUserAgent(t *testing.T) {
	ctx, _ := newTestContext()

	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader(`data: {"type":"response.completed"}

`))
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{Transport: upstream}
	provider := &providercore.Record{
		ID:          890,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "test-token",
			"user_agent":   "codex-tui/9.9.0 test-terminal",
		},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "codex-tui/9.9.0 test-terminal", upstream.requests[0].Header.Get("User-Agent"))
	require.Equal(t, "codex-tui", upstream.requests[0].Header.Get("Originator"))
}

func TestProviderTestService_OpenAIShadowUsesParentCredentialsAndShadowModel(t *testing.T) {
	ctx, recorder := newTestContext()

	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader(`data: {"type":"response.completed"}

`))

	parentID := int64(100)
	parent := &providercore.Record{
		ID:       parentID,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"access_token":       "parent-token",
			"chatgpt_account_id": "org-parent",
		},
	}
	shadow := &providercore.Record{
		ID:               200,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		Status:           billing.StatusActive,
		ParentProviderID: &parentID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
		Concurrency:      2,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5.3-codex-spark": "gpt-5.3-codex-spark",
			},
		},
	}

	repo := &openAIProbeStore{
		openAIProbeRecords: openAIProbeRecords{
			providersByID: map[int64]*providercore.Record{
				parentID: parent,
				200:      shadow,
			},
		},
	}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{Store: repo, Transport: upstream}

	err := executeOpenAIProbeRequest(t, svc, ctx, shadow.ID, "gpt-5.3-codex-spark", "", "")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	req := upstream.requests[0]
	require.Equal(t, "Bearer parent-token", req.Header.Get("Authorization"))
	require.Equal(t, "org-parent", req.Header.Get("chatgpt-account-id"))
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.3-codex-spark", gjson.GetBytes(body, "model").String())
	require.Contains(t, recorder.Body.String(), `"success":true`)
}

func TestProviderTestService_OpenAIStreamEOFBeforeCompletedFails(t *testing.T) {
	ctx, recorder := newTestContext()

	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader(`data: {"type":"response.output_text.delta","delta":"hi"}

`))

	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{Transport: upstream}
	provider := &providercore.Record{
		ID:          90,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.Error(t, err)
	require.Contains(t, recorder.Body.String(), "response.completed")
	require.NotContains(t, recorder.Body.String(), `"success":true`)
}

func TestProviderTestService_RunTestBackgroundWithPromptAndUserAgentOverridesHeader(t *testing.T) {
	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader(`data: {"type":"response.completed"}

`))
	provider := providercore.Record{
		ID:          901,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "test-token",
		},
	}
	repo := &openAIProbeStore{openAIProbeRecords: openAIProbeRecords{providersByID: map[int64]*providercore.Record{provider.ID: &provider}}}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{
		Store:     repo,
		Transport: upstream,
	}

	result, err := openAIProbeCore(svc).RunTestBackgroundWithPromptAndUserAgent(context.Background(), provider.ID, "gpt-5.4", "hi", "codex-tui/9.9.1 test-terminal")

	require.NoError(t, err)
	require.Equal(t, "success", result.Status)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "codex-tui/9.9.1 test-terminal", upstream.requests[0].Header.Get("User-Agent"))
	require.Equal(t, "codex-tui", upstream.requests[0].Header.Get("Originator"))
}

func TestProviderTestService_OpenAI429PersistsSnapshotAndRateLimitState(t *testing.T) {
	ctx, _ := newTestContext()

	resp := newJSONResponse(http.StatusTooManyRequests, `{"error":{"type":"usage_limit_reached","message":"limit reached","resets_at":1777283883}}`)
	resp.Header.Set("x-codex-primary-used-percent", "100")
	resp.Header.Set("x-codex-primary-reset-after-seconds", "604800")
	resp.Header.Set("x-codex-primary-window-minutes", "10080")
	resp.Header.Set("x-codex-secondary-used-percent", "100")
	resp.Header.Set("x-codex-secondary-reset-after-seconds", "18000")
	resp.Header.Set("x-codex-secondary-window-minutes", "300")

	repo := &openAIProbeStore{}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{Store: repo, Transport: upstream}
	provider := &providercore.Record{
		ID:          88,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      providercore.StatusError,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.Error(t, err)
	require.NotEmpty(t, repo.updatedExtra)
	require.Equal(t, 100.0, repo.updatedExtra["codex_5h_used_percent"])
	require.Equal(t, provider.ID, repo.rateLimitedID)
	require.NotNil(t, repo.rateLimitedAt)
	require.Equal(t, provider.ID, repo.clearedErrorID)
	require.Equal(t, billing.StatusActive, provider.Status)
	require.Empty(t, provider.ErrorMessage)
	require.NotNil(t, provider.RateLimitResetAt)
}

func TestProviderTestService_OpenAI429BodyOnlyPersistsRateLimitAndClearsStaleError(t *testing.T) {
	ctx, _ := newTestContext()

	resp := newJSONResponse(http.StatusTooManyRequests, `{"error":{"type":"usage_limit_reached","message":"limit reached","resets_at":"1777283883"}}`)

	repo := &openAIProbeStore{}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{Store: repo, Transport: upstream}
	provider := &providercore.Record{
		ID:           77,
		Platform:     capability.PlatformOpenAI,
		Type:         capability.ProviderTypeOAuth,
		Status:       providercore.StatusError,
		ErrorMessage: "Access forbidden (403): provider may be suspended or lack permissions",
		Concurrency:  1,
		Credentials:  map[string]any{"access_token": "test-token"},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.Error(t, err)
	require.Equal(t, provider.ID, repo.rateLimitedID)
	require.NotNil(t, repo.rateLimitedAt)
	require.Equal(t, provider.ID, repo.clearedErrorID)
	require.Equal(t, billing.StatusActive, provider.Status)
	require.Empty(t, provider.ErrorMessage)
	require.NotNil(t, provider.RateLimitResetAt)
	require.Empty(t, repo.updatedExtra)
}

func TestProviderTestService_OpenAI429SyncsObservedPlanType(t *testing.T) {
	ctx, _ := newTestContext()

	resp := newJSONResponse(http.StatusTooManyRequests, `{"error":{"type":"usage_limit_reached","message":"limit reached","plan_type":"free","resets_at":1777283883}}`)

	repo := &openAIProbeStore{}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{Store: repo, Transport: upstream}
	provider := &providercore.Record{
		ID:          81,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token", "plan_type": "plus"},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.Error(t, err)
	require.Equal(t, []int64{provider.ID}, repo.bulkUpdatedIDs)
	require.Equal(t, "free", repo.bulkUpdatedPayload.Credentials["plan_type"])
	require.Equal(t, "free", provider.Credentials["plan_type"])
	require.Equal(t, provider.ID, repo.rateLimitedID)
	require.NotNil(t, provider.RateLimitResetAt)
}

func TestProviderTestService_OpenAI429ActiveProviderDoesNotClearError(t *testing.T) {
	ctx, _ := newTestContext()

	resp := newJSONResponse(http.StatusTooManyRequests, `{"error":{"type":"usage_limit_reached","message":"limit reached","resets_in_seconds":3600}}`)

	repo := &openAIProbeStore{}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{Store: repo, Transport: upstream}
	provider := &providercore.Record{
		ID:          78,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.Error(t, err)
	require.Equal(t, provider.ID, repo.rateLimitedID)
	require.NotNil(t, repo.rateLimitedAt)
	require.Zero(t, repo.clearedErrorID)
	require.Equal(t, billing.StatusActive, provider.Status)
	require.NotNil(t, provider.RateLimitResetAt)
}

func TestProviderTestService_OpenAI429WithoutResetSignalDoesNotMutateRuntimeState(t *testing.T) {
	ctx, _ := newTestContext()

	resp := newJSONResponse(http.StatusTooManyRequests, `{"error":{"type":"usage_limit_reached","message":"limit reached"}}`)

	repo := &openAIProbeStore{}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{Store: repo, Transport: upstream}
	provider := &providercore.Record{
		ID:           79,
		Platform:     capability.PlatformOpenAI,
		Type:         capability.ProviderTypeOAuth,
		Status:       providercore.StatusError,
		ErrorMessage: "stale 403",
		Concurrency:  1,
		Credentials:  map[string]any{"access_token": "test-token"},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.Error(t, err)
	require.Zero(t, repo.rateLimitedID)
	require.Nil(t, repo.rateLimitedAt)
	require.Zero(t, repo.clearedErrorID)
	require.Equal(t, providercore.StatusError, provider.Status)
	require.Equal(t, "stale 403", provider.ErrorMessage)
	require.Nil(t, provider.RateLimitResetAt)
}

func TestProviderTestService_OpenAI401SetsPermanentErrorOnly(t *testing.T) {
	ctx, _ := newTestContext()

	resp := newJSONResponse(http.StatusUnauthorized, `{"error":"bad token"}`)

	repo := &openAIProbeStore{}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{Store: repo, Transport: upstream}
	provider := &providercore.Record{
		ID:          80,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.Error(t, err)
	require.Equal(t, provider.ID, repo.setErrorID)
	require.Contains(t, repo.setErrorMsg, "Authentication failed (401)")
	require.Zero(t, repo.rateLimitedID)
	require.Zero(t, repo.clearedErrorID)
	require.Nil(t, provider.RateLimitResetAt)
}

// TestProviderTestService_DeepSeekResponsesRoutesToOpenAIProbe checks that CN Responses providers
// use the OpenAI probe through the shared test entry point.
func TestProviderTestService_DeepSeekResponsesRoutesToOpenAIProbe(t *testing.T) {
	ctx, _ := newTestContext()

	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &provideradapter.OpenAIProviderTest{
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{}).Validate,
	}
	provider := &providercore.Record{
		ID:          93,
		Platform:    capability.PlatformDeepseek,
		Type:        capability.ProviderTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":      "sk-test",
			"base_url":     "https://relay.example.com/v1",
			"api_protocol": providercore.APIProtocolResponses,
		},
		Extra: map[string]any{},
	}
	repo := &openAIProbeStore{
		openAIProbeRecords: openAIProbeRecords{
			providersByID: map[int64]*providercore.Record{93: provider},
		},
	}
	svc.Store = repo

	err := executeOpenAIProbeRequest(t, svc, ctx, provider.ID, "gpt-5.4", "", "")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://relay.example.com/v1/responses", upstream.requests[0].URL.String())
}

func TestProviderTestService_OpenAIAPIKeySelectedChatUsesChatCompletionsPath(t *testing.T) {
	ctx, recorder := newTestContext()

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &openAIProbeTransport{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &provideradapter.OpenAIProviderTest{
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{}).Validate,
	}
	provider := &providercore.Record{
		ID:          91,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://compat-upstream.example/v1",
		},
		Extra: map[string]any{providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions)},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "hello", "")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, upstreamcore.HTTPUpstreamProfileOpenAI, upstreamcore.HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, "https://compat-upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "messages.0.content").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	body := recorder.Body.String()
	require.Contains(t, body, "pong")
	require.Contains(t, body, "已通过 /v1/chat/completions 验证")
	require.Contains(t, body, `"success":true`)
	require.NotContains(t, body, "当前测试接口仅支持 Responses API 路径")
}

func TestProviderTestService_OpenAIChatCompletionsPathReturns4xx(t *testing.T) {
	ctx, recorder := newTestContext()

	upstream := &openAIProbeTransport{resp: newJSONResponse(http.StatusBadRequest, `{"error":{"message":"bad request"}}`)}
	svc := &provideradapter.OpenAIProviderTest{
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{}).Validate,
	}
	provider := &providercore.Record{
		ID:          92,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://compat-upstream.example",
		},
		Extra: map[string]any{providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions)},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.Error(t, err)
	require.Equal(t, "https://compat-upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.Contains(t, err.Error(), "Chat Completions API (/v1/chat/completions) returned 400")
	require.Contains(t, recorder.Body.String(), "/v1/chat/completions")
	require.NotContains(t, recorder.Body.String(), `"success":true`)
}

func TestProviderTestService_OpenAIChatCompletionsPathTimeout(t *testing.T) {
	ctx, recorder := newTestContext()

	upstream := &openAIProbeTransport{err: context.DeadlineExceeded}
	svc := &provideradapter.OpenAIProviderTest{
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{}).Validate,
	}
	provider := &providercore.Record{
		ID:          93,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://compat-upstream.example",
		},
		Extra: map[string]any{providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions)},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.Error(t, err)
	require.Equal(t, "https://compat-upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.Contains(t, err.Error(), "Chat Completions API (/v1/chat/completions) request failed")
	require.Contains(t, err.Error(), context.DeadlineExceeded.Error())
	require.Contains(t, recorder.Body.String(), "/v1/chat/completions")
	require.NotContains(t, recorder.Body.String(), `"success":true`)
}

func TestProviderTestService_OpenAIChatCompletionsPathRejectsNonJSONStream(t *testing.T) {
	ctx, recorder := newTestContext()

	upstream := &openAIProbeTransport{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: not-json\n\n")),
	}}
	svc := &provideradapter.OpenAIProviderTest{
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{}).Validate,
	}
	provider := &providercore.Record{
		ID:          94,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://compat-upstream.example",
		},
		Extra: map[string]any{providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions)},
	}

	err := executeOpenAIProbe(t, svc, ctx, provider, "gpt-5.4", "", "")
	require.Error(t, err)
	require.Equal(t, "https://compat-upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.Contains(t, err.Error(), "Invalid Chat Completions response from /v1/chat/completions")
	require.Contains(t, recorder.Body.String(), "/v1/chat/completions")
	require.NotContains(t, recorder.Body.String(), `"success":true`)
}

// TestProviderTestServiceExplicitProtocolDoesNotMutateProvider checks that the selected protocol applies to this test while persisted routing stays intact.
func TestProviderTestServiceExplicitProtocolDoesNotMutateProvider(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions"} {
		t.Run(protocol, func(t *testing.T) {
			provider := providercore.Record{ID: 901, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://example.com/v1"}, Extra: map[string]any{"openai_text_route_mode": "force_chat_completions"}}
			if protocol == "chat_completions" {
				provider.Extra["openai_text_route_mode"] = "force_responses"
			}
			original := provider.Extra["openai_text_route_mode"]
			upstream := &openAIProbeTransport{resp: &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"capture"}`))}}
			svc := &provideradapter.OpenAIProviderTest{Store: &openAIProbeStore{openAIProbeRecords: openAIProbeRecords{providersByID: map[int64]*providercore.Record{provider.ID: &provider}}}, Transport: upstream, ValidateURL: (egress.OperatorURLPolicy{}).Validate}
			c, _ := newTestContext()
			c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
			require.Error(t, executeOpenAIProbeRequestType(t, svc, c, provider.ID, "gpt-5.4", "hi", "text", "default", protocol))
			path := "/v1/responses"
			if protocol == "chat_completions" {
				path = "/v1/chat/completions"
			}
			require.Equal(t, path, upstream.lastReq.URL.Path)
			require.Equal(t, original, provider.Extra["openai_text_route_mode"])
		})
	}
}

func TestProviderTestService_AutomaticOpenAIProbeUsesMatchedRoute(t *testing.T) {
	provider := providercore.Record{
		ID:          1001,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": int64(10),
			"tls_fingerprint_router_id":  int64(9),
			"openai_oauth_client_policy": providercore.OpenAIOAuthClientPolicyTLSRouterMatchedOnly,
		},
	}
	router := &egress.TLSFingerprintRouter{
		ID:      9,
		Name:    "probe-router",
		Enabled: true,
		Rules: []egress.TLSFingerprintRouterRule{{
			Name:                    "probe-client",
			Enabled:                 true,
			MatchType:               egress.TLSRouterMatchExact,
			Pattern:                 "probe-client/1.0",
			TLSFingerprintProfileID: 20,
			UpstreamUserAgent:       "codex_vscode/0.144.1 probe-terminal",
			UpstreamOriginator:      "codex_vscode",
		}},
	}
	upstream := &openAIProbeTransport{resp: successfulOpenAIAutomaticProbeResponse()}
	svc := newOpenAIAutomaticProbeTestService(t,
		[]providercore.Record{provider},
		upstream,
		router,
		map[int64]*egress.TLSFingerprintProfile{
			10: {ID: 10, Name: "fixed"},
			20: {ID: 20, Name: "routed"},
		},
		nil,
	)

	result, err := svc.RunTestBackgroundWithPromptAndUserAgent(context.Background(), provider.ID, "gpt-5.4", "hi", "probe-client/1.0")

	require.NoError(t, err)
	require.Equal(t, "success", result.Status)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "routed", upstream.lastTLSProfile.Name)
	require.Equal(t, "codex_vscode/0.144.1 probe-terminal", upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "codex_vscode", upstream.lastReq.Header.Get("Originator"))
}

func TestProviderTestService_AutomaticOpenAIProbeRejectsClientPolicyLocally(t *testing.T) {
	tests := []struct {
		name       string
		policy     string
		userAgent  string
		runDefault bool
		reason     string
		router     *egress.TLSFingerprintRouter
		extra      map[string]any
	}{
		{
			name:      "TLS 路由未命中",
			policy:    providercore.OpenAIOAuthClientPolicyTLSRouterMatchedOnly,
			userAgent: "curl/8.0",
			reason:    providercore.CodexClientRestrictionReasonNotMatchedTLSRouter,
			router: &egress.TLSFingerprintRouter{
				ID:      9,
				Name:    "probe-router",
				Enabled: true,
				Rules: []egress.TLSFingerprintRouterRule{{
					Enabled:   true,
					MatchType: egress.TLSRouterMatchPrefix,
					Pattern:   "allowed-client/",
				}},
			},
			extra: map[string]any{"tls_fingerprint_router_id": int64(9)},
		},
		{
			name:      "Codex 官方身份未命中",
			policy:    providercore.OpenAIOAuthClientPolicyCodexOnly,
			userAgent: "custom-client/1.0",
			reason:    providercore.CodexClientRestrictionReasonNotMatchedUA,
		},
		{
			name:       "定时测试执行相同策略",
			policy:     providercore.OpenAIOAuthClientPolicyTLSRouterMatchedOnly,
			runDefault: true,
			reason:     providercore.CodexClientRestrictionReasonNotMatchedTLSRouter,
			router: &egress.TLSFingerprintRouter{
				ID:      9,
				Name:    "probe-router",
				Enabled: true,
				Rules: []egress.TLSFingerprintRouterRule{{
					Enabled:   true,
					MatchType: egress.TLSRouterMatchPrefix,
					Pattern:   "different-client/",
				}},
			},
			extra: map[string]any{"tls_fingerprint_router_id": int64(9)},
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extra := map[string]any{"openai_oauth_client_policy": test.policy}
			maps.Copy(extra, test.extra)
			provider := providercore.Record{
				ID:       int64(1100 + i),
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Extra:    extra,
			}
			upstream := &openAIProbeTransport{resp: successfulOpenAIAutomaticProbeResponse()}
			svc := newOpenAIAutomaticProbeTestService(t, []providercore.Record{provider}, upstream, test.router, nil, nil)

			var result *providercore.ScheduledTestResult
			var err error
			if test.runDefault {
				result, err = svc.RunTestBackground(context.Background(), provider.ID, "gpt-5.4")
			} else {
				result, err = svc.RunTestBackgroundWithPromptAndUserAgent(context.Background(), provider.ID, "gpt-5.4", "hi", test.userAgent)
			}

			require.NoError(t, err)
			require.Equal(t, "failed", result.Status)
			require.Contains(t, result.ErrorMessage, "policy="+test.policy)
			require.Contains(t, result.ErrorMessage, "reason="+test.reason)
			require.Empty(t, upstream.requests)
		})
	}
}

func TestProviderTestService_AutomaticOpenAIProbeFallsBackToProviderTLSProfile(t *testing.T) {
	provider := providercore.Record{
		ID:          1201,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": int64(10),
			"tls_fingerprint_router_id":  int64(9),
			"openai_oauth_client_policy": providercore.OpenAIOAuthClientPolicyAny,
		},
	}
	router := &egress.TLSFingerprintRouter{
		ID:      9,
		Name:    "probe-router",
		Enabled: true,
		Rules: []egress.TLSFingerprintRouterRule{{
			Enabled:                 true,
			MatchType:               egress.TLSRouterMatchExact,
			Pattern:                 "other-client/1.0",
			TLSFingerprintProfileID: 20,
		}},
	}
	upstream := &openAIProbeTransport{resp: successfulOpenAIAutomaticProbeResponse()}
	svc := newOpenAIAutomaticProbeTestService(t,
		[]providercore.Record{provider},
		upstream,
		router,
		map[int64]*egress.TLSFingerprintProfile{10: {ID: 10, Name: "fixed"}, 20: {ID: 20, Name: "unused-route"}},
		nil,
	)

	result, err := svc.RunTestBackgroundWithPromptAndUserAgent(context.Background(), provider.ID, "gpt-5.4", "hi", "unmatched-client/1.0")

	require.NoError(t, err)
	require.Equal(t, "success", result.Status)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "fixed", upstream.lastTLSProfile.Name)
}

func TestProviderTestService_AutomaticOpenAIProbeEmptyUserAgentParticipatesInRouting(t *testing.T) {
	parentID := int64(1301)
	parent := providercore.Record{
		ID:       parentID,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "parent-token",
			"user_agent":   "codex_vscode/0.144.1 parent-terminal",
		},
	}
	shadow := providercore.Record{
		ID:               1302,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &parentID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
		Concurrency:      1,
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": int64(10),
			"tls_fingerprint_router_id":  int64(9),
		},
	}
	defaultProvider := providercore.Record{
		ID:          1303,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "default-token"},
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": int64(10),
			"tls_fingerprint_router_id":  int64(9),
		},
	}

	tests := []struct {
		name            string
		providers       []providercore.Record
		providerID      int64
		pattern         string
		routeProfileID  int64
		expectedUA      string
		expectedProfile string
	}{
		{
			name:            "凭据提供商自定义 UA",
			providers:       []providercore.Record{parent, shadow},
			providerID:      shadow.ID,
			pattern:         "codex_vscode/0.144.1 parent-terminal",
			routeProfileID:  20,
			expectedUA:      "codex_vscode/0.144.1 parent-terminal",
			expectedProfile: "routed",
		},
		{
			name:            "内置 Codex UA",
			providers:       []providercore.Record{defaultProvider},
			providerID:      defaultProvider.ID,
			pattern:         openai.CodexCLIUserAgent,
			routeProfileID:  0,
			expectedUA:      openai.CodexCLIUserAgent,
			expectedProfile: "Built-in Default (Node.js 24.x)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := &egress.TLSFingerprintRouter{
				ID:      9,
				Name:    "probe-router",
				Enabled: true,
				Rules: []egress.TLSFingerprintRouterRule{{
					Enabled:                 true,
					MatchType:               egress.TLSRouterMatchExact,
					Pattern:                 test.pattern,
					TLSFingerprintProfileID: test.routeProfileID,
				}},
			}
			upstream := &openAIProbeTransport{resp: successfulOpenAIAutomaticProbeResponse()}
			svc := newOpenAIAutomaticProbeTestService(t,
				test.providers,
				upstream,
				router,
				map[int64]*egress.TLSFingerprintProfile{10: {ID: 10, Name: "fixed"}, 20: {ID: 20, Name: "routed"}},
				nil,
			)

			result, err := svc.RunTestBackground(context.Background(), test.providerID, "gpt-5.4")

			require.NoError(t, err)
			require.Equal(t, "success", result.Status)
			require.Equal(t, test.expectedUA, upstream.lastReq.Header.Get("User-Agent"))
			require.Equal(t, test.expectedProfile, upstream.lastTLSProfile.Name)
		})
	}
}

func TestProviderTestService_AutomaticOpenAIProbeMissingRouteProfileFallsBack(t *testing.T) {
	provider := providercore.Record{
		ID:          1401,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": int64(10),
			"tls_fingerprint_router_id":  int64(9),
		},
	}
	router := &egress.TLSFingerprintRouter{
		ID:      9,
		Name:    "probe-router",
		Enabled: true,
		Rules: []egress.TLSFingerprintRouterRule{{
			Enabled:                 true,
			MatchType:               egress.TLSRouterMatchExact,
			Pattern:                 "route-client/1.0",
			TLSFingerprintProfileID: 404,
		}},
	}
	upstream := &openAIProbeTransport{resp: successfulOpenAIAutomaticProbeResponse()}
	svc := newOpenAIAutomaticProbeTestService(t,
		[]providercore.Record{provider},
		upstream,
		router,
		map[int64]*egress.TLSFingerprintProfile{10: {ID: 10, Name: "fixed"}},
		nil,
	)

	result, err := svc.RunTestBackgroundWithPromptAndUserAgent(context.Background(), provider.ID, "gpt-5.4", "hi", "route-client/1.0")

	require.NoError(t, err)
	require.Equal(t, "success", result.Status)
	require.Equal(t, "fixed", upstream.lastTLSProfile.Name)
}

func TestProviderTestService_AutomaticOpenAIProbeRoutesChatCompletionsAndImages(t *testing.T) {
	t.Run("Chat Completions", func(t *testing.T) {
		provider := providercore.Record{
			ID:          1501,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "https://compat-upstream.example/v1",
			},
			Extra: map[string]any{
				providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions),
				"tls_fingerprint_router_id":        int64(9),
			},
		}
		router := &egress.TLSFingerprintRouter{
			ID:      9,
			Name:    "probe-router",
			Enabled: true,
			Rules: []egress.TLSFingerprintRouterRule{{
				Enabled:           true,
				MatchType:         egress.TLSRouterMatchExact,
				Pattern:           "chat-client/1.0",
				UpstreamUserAgent: "chat-upstream/2.0",
			}},
		}
		upstream := &openAIProbeTransport{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
		}}
		cfg := &egress.OperatorURLPolicy{}
		svc := newOpenAIAutomaticProbeTestService(t, []providercore.Record{provider}, upstream, router, nil, cfg)

		result, err := svc.RunTestBackgroundWithPromptAndUserAgent(context.Background(), provider.ID, "gpt-5.4", "hi", "chat-client/1.0")

		require.NoError(t, err)
		require.Equal(t, "success", result.Status)
		require.Equal(t, "chat-upstream/2.0", upstream.lastReq.Header.Get("User-Agent"))
		require.Contains(t, upstream.lastReq.URL.Path, "/chat/completions")
	})

	t.Run("OAuth 图片", func(t *testing.T) {
		provider := providercore.Record{
			ID:          1502,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{"access_token": "test-token"},
			Extra: map[string]any{
				"enable_tls_fingerprint":    true,
				"tls_fingerprint_router_id": int64(9),
			},
		}
		router := &egress.TLSFingerprintRouter{
			ID:      9,
			Name:    "probe-router",
			Enabled: true,
			Rules: []egress.TLSFingerprintRouterRule{{
				Enabled:                 true,
				MatchType:               egress.TLSRouterMatchExact,
				Pattern:                 "image-client/1.0",
				TLSFingerprintProfileID: 20,
				UpstreamUserAgent:       "codex-tui/0.144.1 image-terminal",
				UpstreamOriginator:      "codex-tui",
			}},
		}
		upstream := &openAIProbeTransport{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"output_format\":\"png\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n" +
					"data: [DONE]\n\n",
			)),
		}}
		svc := newOpenAIAutomaticProbeTestService(t,
			[]providercore.Record{provider},
			upstream,
			router,
			map[int64]*egress.TLSFingerprintProfile{20: {ID: 20, Name: "image-route"}},
			nil,
		)

		result, err := svc.RunTestBackgroundWithPromptAndUserAgent(context.Background(), provider.ID, "gpt-image-2", "draw", "image-client/1.0")

		require.NoError(t, err)
		require.Equal(t, "success", result.Status)
		require.Equal(t, "image-route", upstream.lastTLSProfile.Name)
		require.Equal(t, "codex-tui/0.144.1 image-terminal", upstream.lastReq.Header.Get("User-Agent"))
		require.Equal(t, "codex-tui", upstream.lastReq.Header.Get("Originator"))
	})
}

func TestProviderTestService_ManualOpenAITestDoesNotEnforceAutomaticPolicy(t *testing.T) {
	provider := providercore.Record{
		ID:          1601,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
		Extra: map[string]any{
			"openai_oauth_client_policy": providercore.OpenAIOAuthClientPolicyTLSRouterMatchedOnly,
			"tls_fingerprint_router_id":  int64(9),
		},
	}
	router := &egress.TLSFingerprintRouter{
		ID:      9,
		Name:    "probe-router",
		Enabled: true,
		Rules: []egress.TLSFingerprintRouterRule{{
			Enabled:   true,
			MatchType: egress.TLSRouterMatchExact,
			Pattern:   "never-matched-by-manual-test",
		}},
	}
	upstream := &openAIProbeTransport{resp: successfulOpenAIAutomaticProbeResponse()}
	svc := newOpenAIAutomaticProbeTestService(t, []providercore.Record{provider}, upstream, router, nil, nil)
	c, _ := newTestContext()

	err := svc.Test(c.Request.Context(), providercore.TestRequest{ProviderID: provider.ID, Model: "gpt-5.4", Prompt: "hi", Mode: providercore.ProviderTestModeDefault}, NewTestEventSink(c.recorder))

	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
}

func TestProviderTestService_QoderCosyUsesNativeTestPath(t *testing.T) {
	ctx, recorder := newQoderProviderTestContext()
	provider := &providercore.Record{
		ID:          7,
		Name:        "qoder",
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Concurrency: 1,
		Credentials: map[string]any{
			"security_oauth_token": "token",
			"machine_id":           "machine",
		},
	}
	client := &qoderProviderTestClientStub{
		body: "data: {\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"reasoning_content\\\":\\\"hidden thought\\\"}}]}\"}\n\n" +
			"data: {\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"OK\\\"}}]}\"}\n\n" +
			"data: {\"body\":\"[DONE]\"}\n\n",
	}
	svc := &provideradapter.QoderProviderTest{
		Sessions: &qoderProviderTestSessionProviderStub{
			session: &qoder.SessionContext{Identity: &qoder.AuthIdentity{SecurityOauthToken: "token"}},
		},
		Client:   client,
		UserInfo: &qoderProviderTestOAuthClientStub{},
	}

	err := executeQoderTest(t, ctx, svc, provider, "auto", "hi", "")

	require.NoError(t, err)
	body := recorder.Body.String()
	require.Contains(t, body, `"type":"content"`)
	require.Contains(t, body, `"text":"OK"`)
	require.NotContains(t, body, "hidden thought")
	require.Contains(t, body, `"type":"test_complete"`)
	require.NotContains(t, body, "Unsupported provider type: cosy")
	require.NotNil(t, client.request)
}

func TestProviderTestService_QoderPATRebuildsSessionForConnectionTest(t *testing.T) {
	ctx, _ := newQoderProviderTestContext()
	provider := &providercore.Record{
		ID:       12,
		Name:     "qoder-pat",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
		Credentials: map[string]any{
			"pat": "pat-123",
		},
	}
	tokenSource := &qoderProviderTestSessionProviderStub{
		session: &qoder.SessionContext{Identity: &qoder.AuthIdentity{SecurityOauthToken: "token"}},
	}
	svc := &provideradapter.QoderProviderTest{
		Sessions: tokenSource,
		Client: &qoderProviderTestClientStub{
			body: "data: {\"body\":\"[DONE]\"}\n\n",
		},
		UserInfo: &qoderProviderTestOAuthClientStub{},
	}

	err := executeQoderTest(t, ctx, svc, provider, "", "", "")

	require.NoError(t, err)
	require.Equal(t, []int64{provider.ID}, tokenSource.invalidated)
}

func TestProviderTestService_QoderProbesUserInfoBeforeStream(t *testing.T) {
	ctx, recorder := newQoderProviderTestContext()
	provider := &providercore.Record{
		ID:       11,
		Name:     "qoder",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
	}
	client := &qoderProviderTestClientStub{
		body: "data: {\"body\":\"[DONE]\"}\n\n",
	}
	oauthClient := &qoderProviderTestOAuthClientStub{}
	svc := &provideradapter.QoderProviderTest{
		Sessions: &qoderProviderTestSessionProviderStub{
			session: &qoder.SessionContext{Identity: &qoder.AuthIdentity{SecurityOauthToken: "session-token"}},
		},
		Client:   client,
		UserInfo: oauthClient,
	}

	err := executeQoderTest(t, ctx, svc, provider, "", "", "")

	require.NoError(t, err)
	require.Equal(t, "session-token", oauthClient.token)
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
	require.Equal(t, "auto", client.headers["x-model-key"])
}

func TestProviderTestService_QoderWrappedErrorIsVisible(t *testing.T) {
	ctx, recorder := newQoderProviderTestContext()
	provider := &providercore.Record{
		ID:       8,
		Name:     "qoder",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
	}
	client := &qoderProviderTestClientStub{
		body: "data: {\"body\":\"{\\\"code\\\":\\\"101\\\",\\\"message\\\":\\\"Signature invalid\\\"}\",\"statusCodeValue\":403,\"statusCode\":\"FORBIDDEN\"}\n\n",
	}
	svc := &provideradapter.QoderProviderTest{
		Sessions: &qoderProviderTestSessionProviderStub{
			session: &qoder.SessionContext{Identity: &qoder.AuthIdentity{SecurityOauthToken: "token"}},
		},
		Client:   client,
		UserInfo: &qoderProviderTestOAuthClientStub{},
	}

	err := executeQoderTest(t, ctx, svc, provider, "", "", "")

	require.Error(t, err)
	body := recorder.Body.String()
	require.Contains(t, body, `"type":"error"`)
	require.Contains(t, body, "Qoder upstream error 101: Signature invalid")
	require.NotContains(t, body, "Unsupported provider type: cosy")
}

func TestProviderTestService_QoderReasoningOnlyDoesNotEmitContent(t *testing.T) {
	ctx, recorder := newQoderProviderTestContext()
	provider := &providercore.Record{
		ID:       9,
		Name:     "qoder",
		Platform: capability.PlatformQoder,
		Type:     capability.ProviderTypeCosy,
	}
	client := &qoderProviderTestClientStub{
		body: "data: {\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"reasoning_content\\\":\\\"hidden thought\\\"}}]}\"}\n\n" +
			"data: {\"body\":\"[DONE]\"}\n\n",
	}
	svc := &provideradapter.QoderProviderTest{
		Sessions: &qoderProviderTestSessionProviderStub{
			session: &qoder.SessionContext{Identity: &qoder.AuthIdentity{SecurityOauthToken: "token"}},
		},
		Client:   client,
		UserInfo: &qoderProviderTestOAuthClientStub{},
	}

	err := executeQoderTest(t, ctx, svc, provider, "", "", "")

	require.NoError(t, err)
	body := recorder.Body.String()
	require.NotContains(t, body, `"type":"content"`)
	require.NotContains(t, body, "hidden thought")
	require.Contains(t, body, `"type":"test_complete"`)
}

func TestProviderTestService_QoderUsesHTTPUpstreamForProxyAndTLS(t *testing.T) {
	ctx, recorder := newQoderProviderTestContext()
	proxyID := int64(12)
	provider := &providercore.Record{
		ID:          10,
		Name:        "qoder",
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Concurrency: 2,
		ProxyID:     &proxyID,
		Proxy: &egress.Proxy{
			ID:       proxyID,
			Protocol: "http",
			Host:     "proxy.example.com",
			Port:     8080,
		},
		Extra: map[string]any{
			"enable_tls_fingerprint": true,
		},
	}
	client := &qoderProviderTestClientStub{}
	upstream := &qoderHTTPUpstreamRecorder{
		body: "data: {\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"OK\\\"}}]}\"}\n\n" +
			"data: {\"body\":\"[DONE]\"}\n\n",
	}
	svc := &provideradapter.QoderProviderTest{
		Sessions: &qoderProviderTestSessionProviderStub{
			session: &qoder.SessionContext{Identity: &qoder.AuthIdentity{SecurityOauthToken: "token"}},
		},
		Client:    client,
		UserInfo:  &qoderProviderTestOAuthClientStub{},
		Transport: upstream,
		Profiles:  &egressadapter.TLSProfiles{},
	}

	err := executeQoderTest(t, ctx, svc, provider, "", "", "")

	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), `"text":"OK"`)
	require.Equal(t, "http://proxy.example.com:8080", upstream.proxyURL)
	require.Equal(t, int64(10), upstream.providerID)
	require.True(t, upstream.profileSet)
	require.NotNil(t, client.request)
}

func TestProviderTestService_QoderDefaultUserInfoProbeUsesHTTPUpstream(t *testing.T) {
	ctx, recorder := newQoderProviderTestContext()
	proxyID := int64(12)
	provider := &providercore.Record{
		ID:          12,
		Name:        "qoder",
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Concurrency: 3,
		ProxyID:     &proxyID,
		Proxy: &egress.Proxy{
			ID:       proxyID,
			Protocol: "http",
			Host:     "proxy.example.com",
			Port:     8080,
		},
		Extra: map[string]any{
			"enable_tls_fingerprint": true,
		},
	}
	client := &qoderProviderTestClientStub{}
	upstream := &qoderHTTPUpstreamRecorder{
		userInfoBody: `{"id":"user-12","name":"Qoder User"}`,
		body: "data: {\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"OK\\\"}}]}\"}\n\n" +
			"data: {\"body\":\"[DONE]\"}\n\n",
	}
	svc := &provideradapter.QoderProviderTest{
		Sessions: &qoderProviderTestSessionProviderStub{
			session: &qoder.SessionContext{Identity: &qoder.AuthIdentity{SecurityOauthToken: "token"}},
		},
		Client:    client,
		Transport: upstream,
		Profiles:  &egressadapter.TLSProfiles{},
	}

	err := executeQoderTest(t, ctx, svc, provider, "", "", "")

	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), `"text":"OK"`)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, http.MethodGet, upstream.requests[0].Method)
	require.Contains(t, upstream.requests[0].URL.Path, qoder.UserInfoPath)
	require.Equal(t, "http://proxy.example.com:8080", upstream.proxyURL)
	require.Equal(t, int64(12), upstream.providerID)
	require.Equal(t, 3, upstream.providerConcurrency)
	require.True(t, upstream.profileSet)
	// Check the Qoder session supplied to the probe through its session provider.
	sessionProvider, ok := svc.Sessions.(*qoderProviderTestSessionProviderStub)
	require.True(t, ok)
	require.Equal(t, "user-12", sessionProvider.session.Identity.UID)
}

func TestProviderTestService_QoderUserInfoProbeRedactsSensitiveErrorBody(t *testing.T) {
	ctx, recorder := newQoderProviderTestContext()
	provider := &providercore.Record{
		ID:          13,
		Name:        "qoder",
		Platform:    capability.PlatformQoder,
		Type:        capability.ProviderTypeCosy,
		Concurrency: 1,
		Credentials: map[string]any{
			"security_oauth_token": "token",
		},
	}
	upstream := &qoderHTTPUpstreamRecorder{
		userInfoStatusCode: http.StatusInternalServerError,
		userInfoBody:       `{"message":"failed","securityOauthToken":"sec-secret","refresh_token":"rt-secret","uid":"uid-secret","cookie":"sid=secret"}`,
	}
	svc := &provideradapter.QoderProviderTest{
		Sessions: &qoderProviderTestSessionProviderStub{
			session: &qoder.SessionContext{Identity: &qoder.AuthIdentity{SecurityOauthToken: "token"}},
		},
		Client:    &qoderProviderTestClientStub{},
		Transport: upstream,
		UserInfo:  nil,
	}

	err := executeQoderTest(t, ctx, svc, provider, "", "", "")

	require.Error(t, err)
	body := recorder.Body.String()
	require.Contains(t, body, "qoder userinfo probe failed")
	require.NotContains(t, body, "sec-secret")
	require.NotContains(t, body, "rt-secret")
	require.NotContains(t, body, "uid-secret")
	require.NotContains(t, body, "sid=secret")
	require.Contains(t, body, "***")
}

func adaptiveCNProviderTestProvider(id int64, platform string) *providercore.Record {
	return &providercore.Record{
		ID:          id,
		Name:        "adaptive-cn-test",
		Platform:    platform,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":      "sk-adaptive-test",
			"api_protocol": providercore.APIProtocolAdaptive,
			"api_base_urls": map[string]any{
				providercore.APIProtocolChatCompletions: "http://chat.example/v1",
				providercore.APIProtocolAnthropic:       "http://anthropic.example",
				providercore.APIProtocolResponses:       "http://responses.example",
			},
		},
	}
}

func adaptiveCNProviderTestService(provider *providercore.Record, responses ...*http.Response) (*provideradapter.OpenAIProviderTest, *openAIProbeTransport) {
	repo := &openAIProbeStore{
		openAIProbeRecords: openAIProbeRecords{
			providersByID: map[int64]*providercore.Record{provider.ID: provider},
		},
	}
	upstream := &openAIProbeTransport{responses: responses}
	return &provideradapter.OpenAIProviderTest{
		Store:       repo,
		Transport:   upstream,
		ValidateURL: (egress.OperatorURLPolicy{AllowInsecureHTTP: true}).Validate,
	}, upstream
}

func adaptiveCNChatTestResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(`data: {"choices":[{"delta":{"content":"chat ok"},"finish_reason":"stop"}]}

data: [DONE]

`)),
	}
}

func adaptiveCNAnthropicTestResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(`data: {"type":"content_block_delta","delta":{"text":"anthropic ok"}}

data: {"type":"message_stop"}

`)),
	}
}

func adaptiveCNResponsesTestResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(`data: {"type":"response.output_text.delta","delta":"responses ok"}

data: {"type":"response.completed"}

`)),
	}
}

func (r *cnProviderTestRepo) GetByID(context.Context, int64) (*providercore.Record, error) {
	copy := *r.provider
	return &copy, nil
}

func (h *cnProviderTestHTTP) Do(req *http.Request, proxyURL string, providerID int64, concurrency int) (*http.Response, error) {
	return h.DoWithTLS(req, proxyURL, providerID, concurrency, nil)
}

func (h *cnProviderTestHTTP) DoWithTLS(
	req *http.Request,
	_ string,
	_ int64,
	_ int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	payload, _ := io.ReadAll(req.Body)
	h.body = string(payload)
	h.request = req.Clone(req.Context())
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"error":"stop after request capture"}`)),
	}, nil
}

func (r *grokProviderTestRateLimitRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.rateLimitedCalls++
	r.resetAt = resetAt
	return nil
}

func (s *grokTestStoreFixture) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	value, ok := s.providersByID[id]
	if !ok {
		return nil, fmt.Errorf("provider %d missing", id)
	}
	return providercore.CloneRecord(value), nil
}

func (*grokTestStoreFixture) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

func (*grokTestStoreFixture) SetRateLimited(context.Context, int64, time.Time) error { return nil }

func (*grokTestStoreFixture) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}

func (u *grokTestTransportFixture) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.lastReq = req
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.lastBody = body
	return u.resp, nil
}

func (l grokTestTargetLoader) LoadTestTarget(context.Context, providercore.TestRequest) (providercore.TestTarget, error) {
	return l.target, nil
}

func executeGrokProviderTest(t *testing.T, executor *provideradapter.GrokProviderTest, value *providercore.Record, recorder *httptest.ResponseRecorder, model, prompt, mode string, types ...string) error {
	t.Helper()
	core := providercore.NewTestService(grokTestTargetLoader{target: executor.Target(value)}, providercore.TestOptions{Now: time.Now, Error: func(string) {}, WriteError: func(error) {}})
	request := providercore.TestRequest{ProviderID: value.ID, Model: model, Prompt: prompt, Mode: mode}
	if len(types) > 0 {
		request.Type = &types[0]
	}
	return core.Test(t.Context(), request, NewTestEventSink(recorder))
}

func healthyGrokOAuthGatewayTestProvider(id int64, token string) *providercore.Record {
	return &providercore.Record{
		ID:          id,
		Name:        "grok",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  token,
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(2 * providercore.GrokTokenRefreshSkew).UTC().Format(time.RFC3339),
			"base_url":      xai.DefaultCLIBaseURL,
		},
	}
}

func (f *grokTestFailureStoreFixture) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}

func (f *grokTestFailureStoreFixture) SetRateLimited(_ context.Context, id int64, reset time.Time) error {
	f.rateLimitedCalls++
	f.lastRateLimitedID = id
	f.lastRateLimitResetAt = reset
	return nil
}

func (f *grokTestFailureStoreFixture) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	f.tempUnschedCalls++
	return nil
}

func executeGrokProbe(t *testing.T, executor *provideradapter.GrokProviderTest, output *openAIProbeOutput, value *providercore.Record, model string) error {
	t.Helper()
	run := provideradapter.NewTestRun(output.Request.Context(), output.Request.Header, NewTestEventSink(output.recorder))
	defer run.Cancel()
	return run.Result(executor.Execute(run, value, model))
}

// newProbeAgentKey generates a local signing key for tests.
func newProbeAgentKey(t *testing.T) (openai.AgentIdentityKey, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	return openai.AgentIdentityKey{RuntimeID: "runtime-test", TaskID: "task-test", PrivateKey: private}, base64.StdEncoding.EncodeToString(der)
}

func (r *probeAgentStore) GetByID(context.Context, int64) (*providercore.Record, error) {
	return providercore.CloneRecord(r.provider), nil
}

func (r *probeAgentStore) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.provider.Credentials = credentials
	return nil
}

func (*probeAgentStore) Update(context.Context, *providercore.Record) error {
	return fmt.Errorf("unexpected full provider update")
}

func (*probeAgentStore) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

func (r *probeAgentStore) SetError(context.Context, int64, string) error {
	r.setErrorCalls++
	return nil
}

func (r *probeAgentInvalidations) Invalidate(id int64) { r.providerIDs = append(r.providerIDs, id) }

func probeAgentTasks(store *probeAgentStore, endpoint string, invalidator *probeAgentInvalidations) *provideradapter.ProbeTasks {
	options := providercore.OpenAITaskOptions{
		Read: store.GetByID,
		Register: func(ctx context.Context, value *providercore.Record) (string, error) {
			return provideradapter.RegisterAgentIdentityTask(ctx, value, endpoint)
		},
		Persist: func(ctx context.Context, value *providercore.Record, credentials map[string]any) error {
			_, err := providercore.PersistCredentials(ctx, store, value, credentials, nil)
			return err
		},
	}
	if invalidator != nil {
		options.Invalidate = invalidator.Invalidate
	}
	return &provideradapter.ProbeTasks{Coordinator: &providercore.OpenAITaskCoordinator{}, Options: options}
}

func executeOpenAIProbeRequestType(t *testing.T, executor *provideradapter.OpenAIProviderTest, output *openAIProbeOutput, id int64, model, prompt, kind, mode, protocol string) error {
	t.Helper()
	return openAIProbeCore(executor).Test(output.Request.Context(), providercore.TestRequest{ProviderID: id, Model: model, Prompt: prompt, Mode: mode, Type: &kind, Protocol: protocol}, NewTestEventSink(output.recorder))
}

func (u *queuedHTTPUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected Do call")
}

func (u *queuedHTTPUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.requests = append(u.requests, req)
	u.tlsFlags = append(u.tlsFlags, profile != nil)
	if len(u.responses) == 0 {
		return nil, fmt.Errorf("no mocked response")
	}
	resp := u.responses[0]
	u.responses = u.responses[1:]
	return resp, nil
}

func newJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newTestContext() (*openAIProbeOutput, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	return &openAIProbeOutput{Request: httptest.NewRequest(http.MethodPost, "/test", nil), recorder: recorder}, recorder
}

func (r *openAIProbeStore) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.updatedExtra = updates
	if r.updateExtraCalls != nil {
		copy := make(map[string]any, len(updates))
		maps.Copy(copy, updates)
		r.updateExtraCalls <- copy
	}
	return nil
}

func (r *openAIProbeStore) BulkUpdate(_ context.Context, ids []int64, updates providercore.ProviderBulkUpdate) (int64, error) {
	r.bulkUpdatedIDs = append([]int64(nil), ids...)
	r.bulkUpdatedPayload = updates
	return int64(len(ids)), nil
}

func (r *openAIProbeStore) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitedID = id
	r.rateLimitedAt = &resetAt
	return nil
}

func (r *openAIProbeStore) ClearError(_ context.Context, id int64) error {
	r.clearedErrorID = id
	return nil
}

func (r *openAIProbeStore) SetError(_ context.Context, id int64, errorMsg string) error {
	r.setErrorID = id
	r.setErrorMsg = errorMsg
	return nil
}

func (r openAIProbeRecords) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	value, ok := r.providersByID[id]
	if !ok {
		return nil, fmt.Errorf("provider %d missing", id)
	}
	return providercore.CloneRecord(value), nil
}

// configureOpenAIProbe connects policy, platform execution, and event handling.
func configureOpenAIProbe(executor *provideradapter.OpenAIProviderTest) {
	policy := &provideradapter.OpenAIProbePolicy{Available: true, DefaultBrowserUserAgent: gateway.DefaultOpenAICodexUserAgent}
	if executor.Store != nil {
		policy.Read = executor.Store.GetByID
	}
	if executor.Prepare == nil {
		executor.Prepare = policy.Prepare
	}
	if executor.ApplyRouting == nil {
		executor.ApplyRouting = policy.ApplyTestRouting
	}
	if executor.ResolveTLS == nil {
		executor.ResolveTLS = policy.ResolveTestTLS
	}
}

func executeOpenAIProbe(t *testing.T, executor *provideradapter.OpenAIProviderTest, output *openAIProbeOutput, value *providercore.Record, model, prompt, mode string, types ...string) error {
	t.Helper()
	configureOpenAIProbe(executor)
	run := provideradapter.NewTestRun(output.Request.Context(), output.Request.Header, NewTestEventSink(output.recorder))
	defer run.Cancel()
	return run.Result(executor.Execute(run, value, model, prompt, mode, types...))
}

func executeOpenAIImageAPIKeyProbe(t *testing.T, executor *provideradapter.OpenAIProviderTest, output *openAIProbeOutput, ctx context.Context, value *providercore.Record, model, prompt string) error {
	t.Helper()
	configureOpenAIProbe(executor)
	run := provideradapter.NewTestRun(output.Request.Context(), output.Request.Header, NewTestEventSink(output.recorder))
	defer run.Cancel()
	return run.Result(executor.ExecuteImageAPIKey(run, ctx, value, model, prompt))
}

func executeOpenAIImageOAuthProbe(t *testing.T, executor *provideradapter.OpenAIProviderTest, output *openAIProbeOutput, ctx context.Context, value *providercore.Record, model, prompt string) error {
	t.Helper()
	configureOpenAIProbe(executor)
	run := provideradapter.NewTestRun(output.Request.Context(), output.Request.Header, NewTestEventSink(output.recorder))
	defer run.Cancel()
	return run.Result(executor.ExecuteImageOAuth(run, ctx, value, model, prompt))
}

func openAIProbeCore(executor *provideradapter.OpenAIProviderTest) *providercore.TestService {
	configureOpenAIProbe(executor)
	targets := &provideradapter.TestTargets{Read: executor.Store.GetByID, OpenAI: executor, CN: &provideradapter.CNProviderTest{Transport: executor.Transport, Store: executor.Store, ValidateURL: executor.ValidateURL, Responses: executor}}
	return providercore.NewTestService(targets, providercore.TestOptions{Now: time.Now, Error: func(string) {}, WriteError: func(error) {}})
}

func executeOpenAIProbeRequest(t *testing.T, executor *provideradapter.OpenAIProviderTest, output *openAIProbeOutput, id int64, model, prompt, mode string) error {
	t.Helper()
	return openAIProbeCore(executor).Test(output.Request.Context(), providercore.TestRequest{ProviderID: id, Model: model, Prompt: prompt, Mode: mode}, NewTestEventSink(output.recorder))
}

func (f *openAIProbeTransport) DoWithTLS(req *http.Request, _ string, _ int64, _ int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	f.lastReq = req
	f.requests = append(f.requests, req)
	f.lastTLSProfile = profile
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		f.lastBody = body
		f.bodies = append(f.bodies, append([]byte(nil), body...))
		if err := req.Body.Close(); err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	if f.err != nil {
		return nil, f.err
	}
	if len(f.responses) > 0 {
		response := f.responses[0]
		f.responses = f.responses[1:]
		return response, nil
	}
	return f.resp, nil
}

// newCNProtocolTestCore connects platform dispatch to a test store that supplies target records.
func newCNProtocolTestCore(read func(context.Context, int64) (*providercore.Record, error), transport provideradapter.QoderTransport) *providercore.TestService {
	policy := egress.OperatorURLPolicy{}
	openaiExecutor := &provideradapter.OpenAIProviderTest{Transport: transport, ValidateURL: policy.Validate}
	configureOpenAIProbe(openaiExecutor)
	targets := &provideradapter.TestTargets{Read: read, CN: &provideradapter.CNProviderTest{Transport: transport, ValidateURL: policy.Validate, Responses: openaiExecutor}}
	return providercore.NewTestService(targets, providercore.TestOptions{Now: time.Now, Error: func(string) {}, WriteError: func(error) {}})
}

func successfulOpenAIAutomaticProbeResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(`data: {"type":"response.completed"}

`)),
	}
}

// newOpenAIAutomaticProbeTestService builds the TLS cache, router, and provider test components.
func newOpenAIAutomaticProbeTestService(t *testing.T, values []providercore.Record, transport *openAIProbeTransport, router *egress.TLSFingerprintRouter, profiles map[int64]*egress.TLSFingerprintProfile, urlPolicy *egress.OperatorURLPolicy) *providercore.TestService {
	t.Helper()
	profileStore := &automaticProbeProfileStore{}
	for _, value := range profiles {
		profileStore.values = append(profileStore.values, value)
	}
	profileService := egressadapter.NewTLSProfiles(egress.NewTLSFingerprintProfileService(profileStore, nil))
	profileService.Start()
	t.Cleanup(profileService.Stop)
	records := openAIProbeRecords{providersByID: make(map[int64]*providercore.Record, len(values))}
	for i := range values {
		records.providersByID[values[i].ID] = &values[i]
	}
	store := &openAIProbeStore{openAIProbeRecords: records}
	policy := &provideradapter.OpenAIProbePolicy{Available: true, Read: store.GetByID, DefaultBrowserUserAgent: gateway.DefaultOpenAICodexUserAgent, Profiles: profileService, ManualProfiles: profileService}
	if router != nil {
		routers := egress.NewTLSFingerprintRouterService(&automaticProbeRouterStore{values: []*egress.TLSFingerprintRouter{router}}, nil)
		routers.Start()
		t.Cleanup(routers.Stop)
		policy.Routers = routers
	}
	executor := &provideradapter.OpenAIProviderTest{Store: store, Transport: transport, Prepare: policy.Prepare, ApplyRouting: policy.ApplyTestRouting, ResolveTLS: policy.ResolveTestTLS, ValidateURL: func(raw string) (string, error) {
		if urlPolicy == nil {
			return "", errors.New("config is not available")
		}
		return urlPolicy.Validate(raw)
	}}
	return openAIProbeCore(executor)
}

func (s *automaticProbeProfileStore) List(context.Context) ([]*egress.TLSFingerprintProfile, error) {
	return s.values, nil
}

func (s *automaticProbeRouterStore) List(context.Context) ([]*egress.TLSFingerprintRouter, error) {
	return s.values, nil
}

func (s *qoderProviderTestSessionProviderStub) GetSession(context.Context, *providercore.Record) (*qoder.SessionContext, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.session, nil
}

func (s *qoderProviderTestSessionProviderStub) Invalidate(providerID int64) {
	s.invalidated = append(s.invalidated, providerID)
}

func (s *qoderProviderTestClientStub) StreamRequestContext(ctx context.Context, _ *qoder.SessionContext, _ string, bodyJSON []byte, headers map[string]string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api1.qoder.sh/test", strings.NewReader(string(bodyJSON)))
	s.request = req
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, append([]byte(nil), bodyJSON...))
	s.headers = headers
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

func (s *qoderProviderTestClientStub) StreamRequestContextWithDoer(ctx context.Context, _ *qoder.SessionContext, _ string, bodyJSON []byte, headers map[string]string, doer qoder.RequestDoer) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api1.qoder.sh/test", strings.NewReader(string(bodyJSON)))
	s.request = req
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, append([]byte(nil), bodyJSON...))
	s.headers = headers
	if s.err != nil {
		return nil, s.err
	}
	return doer(req)
}

func (s *qoderProviderTestOAuthClientStub) GetUserInfo(_ context.Context, token string) (*qoder.UserInfo, error) {
	s.token = token
	if s.err != nil {
		return nil, s.err
	}
	return &qoder.UserInfo{ID: "user-1", Name: "Qoder User"}, nil
}

func (u *qoderHTTPUpstreamRecorder) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

func (u *qoderHTTPUpstreamRecorder) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.proxyURL = proxyURL
	u.providerID = providerID
	u.providerConcurrency = providerConcurrency
	u.profileSet = profile != nil
	u.requests = append(u.requests, req)
	body := u.body
	if req.Method == http.MethodGet && strings.Contains(req.URL.Path, qoder.UserInfoPath) {
		body = u.userInfoBody
		if body == "" {
			body = `{"id":"user-1","name":"Qoder User"}`
		}
	}
	statusCode := http.StatusOK
	if req.Method == http.MethodGet && strings.Contains(req.URL.Path, qoder.UserInfoPath) && u.userInfoStatusCode != 0 {
		statusCode = u.userInfoStatusCode
	}
	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func newQoderProviderTestContext() (*qoderTestOutputFixture, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	return &qoderTestOutputFixture{Context: context.Background(), recorder: recorder}, recorder
}

func (l qoderTargetFixture) LoadTestTarget(context.Context, providercore.TestRequest) (providercore.TestTarget, error) {
	return l.target, nil
}

func executeQoderTest(t *testing.T, output *qoderTestOutputFixture, executor *provideradapter.QoderProviderTest, value *providercore.Record, model, prompt, mode string) error {
	t.Helper()
	executor.RewriteModel = openaiprotocol.ReplaceModelInBody
	core := providercore.NewTestService(qoderTargetFixture{target: executor.Target(value)}, providercore.TestOptions{
		Now:        time.Now,
		Error:      func(message string) { log.Printf("Provider test error: %s", message) },
		WriteError: func(err error) { log.Printf("failed to write SSE event: %v", err) },
	})
	return core.Test(output.Context, providercore.TestRequest{ProviderID: value.ID, Model: model, Prompt: prompt, Mode: mode}, NewTestEventSink(output.recorder))
}
