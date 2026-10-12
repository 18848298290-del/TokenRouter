package httpapi

// HTTP transport scenarios cover forwarding with WS configured, retries, and authentication failures.

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestOpenAIGatewayService_Forward_HTTPIngressStaysHTTPWhenWSEnabled(t *testing.T) {
	wsFallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer wsFallbackServer.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "custom-client/1.0")
	gatewayhttp.SetOpenAIClientTransport(c, gatewayhttp.OpenAIClientTransportHTTP)

	upstream := &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`,
			)),
		},
	}

	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	svc := newWSFixture(wsFixtureInputs{options: options, transport: upstream})

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 101,
			Name:        "openai-apikey",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": wsFallbackServer.URL,
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	body := []byte(`{"model":"gpt-5.1","stream":false,"previous_response_id":"resp_http_keep","input":[{"type":"input_text","text":"hello"}]}`)
	result, err := svc.Responses.Forward(context.Background(), c, provider, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.OpenAIWSMode, "HTTP 入站应保持 HTTP 转发")
	require.NotNil(t, upstream.lastReq, "HTTP 入站应命中 HTTP 上游")
	require.Equal(t, "resp_http_keep", gjson.GetBytes(upstream.lastBody, "previous_response_id").String(), "API-key HTTP must preserve official Responses continuation")

	decision, _ := c.Get("openai_ws_transport_decision")
	reason, _ := c.Get("openai_ws_transport_reason")
	require.Equal(t, string(egress.OpenAIUpstreamTransportHTTPSSE), decision)
	require.Equal(t, "client_protocol_http", reason)
}

func TestOpenAIGatewayService_Forward_HTTPIngressRetriesInvalidEncryptedContentOnce(t *testing.T) {
	wsFallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer wsFallbackServer.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "custom-client/1.0")
	gatewayhttp.SetOpenAIClientTransport(c, gatewayhttp.OpenAIClientTransportHTTP)

	upstream := &httpUpstreamSequenceRecorder{
		responses: []*http.Response{
			{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"The encrypted content could not be verified."}}`,
				)),
			},
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"resp_http_retry_ok","usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`,
				)),
			},
		},
	}

	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	svc := newWSFixture(wsFixtureInputs{options: options, transport: upstream})

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 102,
			Name:        "openai-apikey",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": wsFallbackServer.URL,
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	body := []byte(`{"model":"gpt-5.1","stream":false,"previous_response_id":"resp_http_retry","input":[{"type":"reasoning","encrypted_content":"gAAA","summary":[{"type":"summary_text","text":"keep me"}]},{"type":"input_text","text":"hello"}]}`)
	result, err := svc.Responses.Forward(context.Background(), c, provider, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.OpenAIWSMode, "HTTP 入站应保持 HTTP 转发")
	require.Equal(t, 2, upstream.callCount, "命中 invalid_encrypted_content 后应只在 HTTP 路径重试一次")
	require.Len(t, upstream.bodies, 2)

	firstBody := upstream.bodies[0]
	secondBody := upstream.bodies[1]
	require.Equal(t, "resp_http_retry", gjson.GetBytes(firstBody, "previous_response_id").String(), "API-key HTTP must preserve continuation on the first attempt")
	require.True(t, gjson.GetBytes(firstBody, "input.0.encrypted_content").Exists(), "首次请求不应做发送前预清理")
	require.Equal(t, "keep me", gjson.GetBytes(firstBody, "input.0.summary.0.text").String())

	require.Equal(t, "resp_http_retry", gjson.GetBytes(secondBody, "previous_response_id").String(), "encrypted-content retry must preserve API-key continuation")
	require.False(t, gjson.GetBytes(secondBody, "input.0.encrypted_content").Exists(), "精确重试应移除 reasoning.encrypted_content")
	require.Equal(t, "keep me", gjson.GetBytes(secondBody, "input.0.summary.0.text").String(), "精确重试应保留有效 reasoning summary")
	require.Equal(t, "input_text", gjson.GetBytes(secondBody, "input.1.type").String(), "非 reasoning input 应保持原样")

	decision, _ := c.Get("openai_ws_transport_decision")
	reason, _ := c.Get("openai_ws_transport_reason")
	require.Equal(t, string(egress.OpenAIUpstreamTransportHTTPSSE), decision)
	require.Equal(t, "client_protocol_http", reason)
}

func TestOpenAIGatewayService_Forward_HTTPIngressRetriesWrappedInvalidEncryptedContentOnce(t *testing.T) {
	wsFallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer wsFallbackServer.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "custom-client/1.0")
	gatewayhttp.SetOpenAIClientTransport(c, gatewayhttp.OpenAIClientTransportHTTP)

	upstream := &httpUpstreamSequenceRecorder{
		responses: []*http.Response{
			{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"code":null,"message":"{\"error\":{\"message\":\"The encrypted content could not be verified.\",\"type\":\"invalid_request_error\",\"param\":null,\"code\":\"invalid_encrypted_content\"}}（traceid: fb7ad1dbc7699c18f8a02f258f1af5ab）","param":null,"type":"invalid_request_error"}}`,
				)),
			},
			{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"x-request-id": []string{"req_http_retry_wrapped_ok"},
				},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"resp_http_retry_wrapped_ok","usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`,
				)),
			},
		},
	}

	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	svc := newWSFixture(wsFixtureInputs{options: options, transport: upstream})

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 103,
			Name:        "openai-apikey-wrapped",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": wsFallbackServer.URL,
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	body := []byte(`{"model":"gpt-5.1","stream":false,"previous_response_id":"resp_http_retry_wrapped","input":[{"type":"reasoning","encrypted_content":"gAAA","summary":[{"type":"summary_text","text":"keep me too"}]},{"type":"input_text","text":"hello"}]}`)
	result, err := svc.Responses.Forward(context.Background(), c, provider, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.OpenAIWSMode, "HTTP 入站应保持 HTTP 转发")
	require.Equal(t, 2, upstream.callCount, "wrapped invalid_encrypted_content 也应只在 HTTP 路径重试一次")
	require.Len(t, upstream.bodies, 2)

	firstBody := upstream.bodies[0]
	secondBody := upstream.bodies[1]
	require.True(t, gjson.GetBytes(firstBody, "input.0.encrypted_content").Exists(), "首次请求不应做发送前预清理")
	require.False(t, gjson.GetBytes(secondBody, "input.0.encrypted_content").Exists(), "wrapped exact retry 应移除 reasoning.encrypted_content")
	require.Equal(t, "keep me too", gjson.GetBytes(secondBody, "input.0.summary.0.text").String(), "wrapped exact retry 应保留有效 reasoning summary")

	decision, _ := c.Get("openai_ws_transport_decision")
	reason, _ := c.Get("openai_ws_transport_reason")
	require.Equal(t, string(egress.OpenAIUpstreamTransportHTTPSSE), decision)
	require.Equal(t, "client_protocol_http", reason)
}

func TestOpenAIGatewayService_Forward_APIKeyHTTPPreservesPreviousResponseIDWhenWSDisabled(t *testing.T) {
	wsFallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer wsFallbackServer.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "custom-client/1.0")

	upstream := &auxiliaryHTTPRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`,
			)),
		},
	}

	options := &wsFixtureOptions{}
	options.Request.URLPolicy.Enabled = false
	options.Request.URLPolicy.AllowInsecureHTTP = true

	svc := newWSFixture(wsFixtureInputs{options: options, transport: upstream})

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 1,
			Name:        "openai-apikey",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": wsFallbackServer.URL,
			},
			Extra: map[string]any{
				"responses_websockets_v2_enabled": true,
			},
		},
	}

	body := []byte(`{"model":"gpt-5.1","stream":false,"previous_response_id":"resp_123","input":[{"type":"input_text","text":"hello"}]}`)
	result, err := svc.Responses.Forward(context.Background(), c, provider, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_123", gjson.GetBytes(upstream.lastBody, "previous_response_id").String())
}

// TestOpenAIGatewayService_APIKeyPassthrough_PoolModeAuthErrorsTriggerFailover checks configured same-provider retries before failover.
func TestOpenAIGatewayService_APIKeyPassthrough_PoolModeAuthErrorsTriggerFailover(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		credentials map[string]any
	}{
		{
			name:       "configured_401",
			statusCode: http.StatusUnauthorized,
			credentials: map[string]any{
				"pool_mode_retry_status_codes": []any{float64(http.StatusUnauthorized)},
			},
		},
		{
			name:        "default_403",
			statusCode:  http.StatusForbidden,
			credentials: map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

			upstreamBody := `{"error":{"message":"upstream credential rejected"}}`
			svc := newWSFixture(wsFixtureInputs{options: &wsFixtureOptions{Request: gatewayhttp.OpenAIRequestOptions{ForceCLI: false}}, health: newUpstreamHealthForTest(transientCooldownProviderRepo{}, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil), transport: &auxiliaryHTTPRecorder{resp: &http.Response{
				StatusCode: tt.statusCode,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(upstreamBody)),
			}}})
			credentials := map[string]any{
				"api_key":   "sk-test",
				"base_url":  "https://api.example.test",
				"pool_mode": true,
			}
			maps.Copy(credentials, tt.credentials)
			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 129, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Concurrency: 1,
					Credentials: credentials,
					Extra:       map[string]any{"openai_passthrough": true}, Status: billing.StatusActive, Schedulable: true,
				},
			}

			_, err := svc.Responses.Forward(context.Background(), c, provider, []byte(`{"model":"gpt-5.2","input":"hello"}`))

			var failoverErr *forwardcore.UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, tt.statusCode, failoverErr.StatusCode)
			require.True(t, failoverErr.RetryableOnSameProvider)
			require.False(t, c.Writer.Written(), "池模式认证失败必须在提交响应前进入故障转移")
			require.False(t, gatewayhttp.IsResponseCommitted(c))
		})
	}
}
