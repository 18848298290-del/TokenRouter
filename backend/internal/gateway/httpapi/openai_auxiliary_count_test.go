package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tokenestimate"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Compatibility counting can query the official Responses endpoint or return
// local estimates for platform and scope limitations. The fixtures compare
// converted request fields, client count framing, and provider-health writes;
// an optional credentialed comparison checks estimator error against actual
// count responses.

// countTokensRuntimeStateRepo records health mutations attempted during
// token-count execution.
type countTokensRuntimeStateRepo struct {
	gatewayprovider.ExecutionProviderStore // unused embedded repository methods

	tempUnschedCalls int // temporary exclusion write count
	setErrorCalls    int // persistent provider-error write count
}

// SetTempUnschedulable counts attempted temporary scheduling exclusions in the
// fixture.
func (r *countTokensRuntimeStateRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, _ string) error {
	r.tempUnschedCalls++
	return nil
}

// SetError counts attempted persistent provider-error writes in the counting
// fixture.
func (r *countTokensRuntimeStateRepo) SetError(_ context.Context, _ int64, _ string) error {
	r.setErrorCalls++
	return nil
}

// TestOpenAIGatewayService_ForwardCountTokensAsAnthropic_APIKeyUsesResponsesInputTokens
// converts Messages input for an API-key count target. It checks Anthropic
// count framing, authentication, mapped model, Responses input, and absence of
// Messages fields.
func TestOpenAIGatewayService_ForwardCountTokensAsAnthropic_APIKeyUsesResponsesInputTokens(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4-5","system":"You are helpful.","messages":[{"role":"user","content":"hello"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"object":"response.input_tokens","input_tokens":42}`)),
	}}

	svc := newAuxiliaryFixture(auxiliaryFixtureInputs{
		allowHTTP: true,
		transport: upstream,
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 101,
			Name:        "openai-apikey",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "http://upstream.example",
			},
			Status:      billing.StatusActive,
			Schedulable: true,
		},
	}

	err := svc.ForwardCountTokensAsAnthropic(context.Background(), c, provider, body, "gpt-5.3-codex")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"input_tokens":42}`, rec.Body.String())
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "http://upstream.example/v1/responses/input_tokens", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("authorization"))
	require.Equal(t, "gpt-5.3-codex", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
}

// TestOpenAIGatewayServiceForwardCountTokensCNProvidersAlwaysEstimateLocally
// varies CN platform, provider mode, and configured protocol. It checks
// positive local counts, absent upstream requests, and zero temporary or
// persistent health writes.
func TestOpenAIGatewayServiceForwardCountTokensCNProvidersAlwaysEstimateLocally(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
	tests := []struct {
		name     string // counting test scenario label
		platform string // selected CN provider platform
		mode     string // configured provider billing mode
		protocol string // configured upstream protocol kind
	}{
		{name: "kimi_payg_chat", platform: capability.PlatformKimi, mode: providercore.ProviderModePayG, protocol: providercore.APIProtocolChatCompletions},
		{name: "kimi_coding_anthropic", platform: capability.PlatformKimi, mode: providercore.ProviderModeCoding, protocol: providercore.APIProtocolAnthropic},
		{name: "zhipu_payg_anthropic", platform: capability.PlatformZhipu, mode: providercore.ProviderModePayG, protocol: providercore.APIProtocolAnthropic},
		{name: "zhipu_coding_chat", platform: capability.PlatformZhipu, mode: providercore.ProviderModeCoding, protocol: providercore.APIProtocolChatCompletions},
		{name: "deepseek_responses", platform: capability.PlatformDeepseek, mode: providercore.ProviderModePayG, protocol: providercore.APIProtocolResponses},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))

			upstream := &auxiliaryHTTPRecorder{}
			repo := &countTokensRuntimeStateRepo{}
			svc := newAuxiliaryFixture(auxiliaryFixtureInputs{
				transport: upstream,
				observer:  gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo}),
			})
			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 301,
					Platform: tt.platform,
					Type:     capability.ProviderTypeAPIKey,
					Credentials: map[string]any{
						"api_key":       "sk-test",
						"provider_mode": tt.mode,
						"api_protocol":  tt.protocol,
					},
				},
			}

			err := svc.ForwardCountTokensAsAnthropic(context.Background(), c, provider, body, "")
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Positive(t, gjson.GetBytes(rec.Body.Bytes(), "input_tokens").Int())
			require.Nil(t, upstream.lastReq, "本地估算不得访问上游")
			require.Zero(t, repo.tempUnschedCalls)
			require.Zero(t, repo.setErrorCalls)
		})
	}
}

// TestOpenAIGatewayService_ForwardCountTokensAsAnthropic_OAuthFallsBackWhenPlatformEndpointUnsupported
// returns scope, HTML, and absent-endpoint errors from OAuth counting. It
// checks the local Responses estimate, official count target, bearer
// authentication, and zero temporary-exclusion and persistent-error writes.
func TestOpenAIGatewayService_ForwardCountTokensAsAnthropic_OAuthFallsBackWhenPlatformEndpointUnsupported(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-1","messages":[{"role":"user","content":"hello"}]}`)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 202,
			Name:        "openai-oauth",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":  "oauth-token",
				"refresh_token": "oauth-refresh-token",
			},
			Status:      billing.StatusActive,
			Schedulable: true,
		},
	}

	prepared, err := gatewayprovider.PrepareAnthropicInputTokens(body, provider, "gpt-5.4")
	require.NoError(t, err)
	expectedEstimate, err := tokenestimate.Responses(prepared.Request)
	require.NoError(t, err)

	cases := []struct {
		name       string // counting test scenario label
		statusCode int    // upstream counting HTTP status
		body       string // upstream counting error body
	}{
		{
			name:       "401_missing_responses_write_scope",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":{"type":"invalid_request_error","code":"missing_scope","message":"You have insufficient permissions for this operation. Missing scopes: api.responses.write."}}`,
		},
		{
			name:       "403_missing_responses_write_scope",
			statusCode: http.StatusForbidden,
			body:       `{"error":{"type":"invalid_request_error","code":"missing_scope","message":"Missing scopes: api.responses.write"}}`,
		},
		{
			name:       "403_html_proxy_page",
			statusCode: http.StatusForbidden,
			body:       "<!doctype html><html><body>Forbidden</body></html>",
		},
		{
			name:       "404_input_tokens_unsupported",
			statusCode: http.StatusNotFound,
			body:       `{"error":{"type":"invalid_request_error","message":"The /v1/responses/input_tokens endpoint was not found"}}`,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("User-Agent", "Claude-Code/1.0")

			upstream := &auxiliaryHTTPRecorder{resp: &http.Response{
				StatusCode: tt.statusCode,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}}
			repo := &countTokensRuntimeStateRepo{}
			svc := newAuxiliaryFixture(auxiliaryFixtureInputs{
				transport: upstream,
				observer:  gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: repo}),
			})

			err := svc.ForwardCountTokensAsAnthropic(context.Background(), c, provider, body, "gpt-5.4")
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code)
			require.JSONEq(t, `{"input_tokens":`+strconv.Itoa(expectedEstimate)+`}`, rec.Body.String())
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, "https://api.openai.com/v1/responses/input_tokens", upstream.lastReq.URL.String())
			require.Equal(t, "Bearer oauth-token", upstream.lastReq.Header.Get("authorization"))
			require.Empty(t, upstream.lastReq.Header.Get("Chatgpt-Account-Id"))
			require.Zero(t, repo.tempUnschedCalls, "OAuth input_tokens unsupported errors must not temp-unschedule the provider")
			require.Zero(t, repo.setErrorCalls, "OAuth input_tokens unsupported errors must not mark the provider error")
		})
	}
}

// TestOpenAIGatewayService_OpenAIOAuthInputTokensFallbackUsesMinimumWhenEstimateFails
// supplies malformed prepared Responses input. It checks the compatibility
// helper writes HTTP 200 with the minimum positive input-token count after
// estimator failure.
func TestOpenAIGatewayService_OpenAIOAuthInputTokensFallbackUsesMinimumWhenEstimateFails(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	prepared := &gatewayprovider.InputTokensPrepared{
		Request: tokenestimate.Request{
			Model: "gpt-5",
			Input: json.RawMessage(`[`),
		},
		UpstreamModel: "gpt-5",
	}

	writeOpenAIOAuthInputTokensFallback(c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 303}}, prepared, http.StatusUnauthorized)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"input_tokens":1}`, rec.Body.String())
}

// TestEstimateOpenAIInputTokens_CompareWithOpenAIAPI compares local estimates
// with credentialed upstream counts for prepared Messages examples. It checks
// absolute error within the larger of 24 tokens or a quarter of the actual
// count, skipping unavailable credentials and recognized network failures.
func TestEstimateOpenAIInputTokens_CompareWithOpenAIAPI(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	// This live comparison requires a plausible credential. Skip short sk-
	// values before any external request so placeholder configuration does
	// not trigger authentication traffic.
	if strings.HasPrefix(apiKey, "sk-") && len(apiKey) < 20 {
		t.Skip("OPENAI_API_KEY looks incomplete")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	cases := []struct {
		name               string // counting test scenario label
		anthropicBody      []byte // source Messages request bytes
		defaultOpenAIModel string // mapped OpenAI count model
	}{
		{
			name:               "simple user text",
			defaultOpenAIModel: "gpt-5",
			anthropicBody:      []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello world from tokenrouter"}]}`),
		},
		{
			name:               "system plus tool",
			defaultOpenAIModel: "gpt-5",
			anthropicBody:      []byte(`{"model":"claude-sonnet-4-5","system":"You are helpful.","messages":[{"role":"user","content":"find weather in shanghai"}],"tools":[{"name":"lookup_weather","description":"Look up current weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]}`),
		},
		{
			name:               "multi turn text",
			defaultOpenAIModel: "gpt-4.1",
			anthropicBody:      []byte(`{"model":"claude-opus-4-1","messages":[{"role":"user","content":"summarize this repo"},{"role":"assistant","content":"which repo?"},{"role":"user","content":"tokenrouter"}]}`),
		},
	}

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepared, err := gatewayprovider.PrepareAnthropicInputTokens(tc.anthropicBody, provider, tc.defaultOpenAIModel)
			require.NoError(t, err)

			estimated, err := tokenestimate.Responses(prepared.Request)
			require.NoError(t, err)

			actual, err := callOpenAIInputTokensAPIForTest(client, apiKey, prepared.Request)
			if err != nil {
				// Authentication rejection and recognized
				// network failures make the live reference
				// unavailable. Skip those cases rather than
				// treating them as estimator discrepancies.
				var netErr net.Error
				if strings.Contains(err.Error(), "status=401") ||
					strings.Contains(err.Error(), "invalid_api_key") ||
					errors.As(err, &netErr) {
					t.Skipf("OpenAI live comparison unavailable: %v", err)
				}
				require.NoError(t, err)
			}

			diff := estimated - actual
			if diff < 0 {
				diff = -diff
			}
			t.Logf("model=%s estimated=%d actual=%d diff=%d", prepared.Request.Model, estimated, actual, diff)
			// Small examples tolerate 24 tokens; larger counts
			// allow 25 percent because this estimator comparison
			// measures approximation rather than protocol field
			// identity.
			require.LessOrEqual(t, diff, maxLocalInt(24, actual/4))
		})
	}
}

// callOpenAIInputTokensAPIForTest sends prepared counting input with the
// supplied client and credential. It closes the response after reading and
// returns input_tokens when present. HTTP status failures and missing fields
// return diagnostic errors to the live comparison.
func callOpenAIInputTokensAPIForTest(client *http.Client, apiKey string, reqBody tokenestimate.Request) (int, error) {
	body, err := wirejson.Marshal(reqBody)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodPost, openaiPlatformAPIInputTokensURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("authorization", "Bearer "+apiKey)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("openai input_tokens api error: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	value := gjson.GetBytes(respBody, "input_tokens")
	if !value.Exists() {
		return 0, fmt.Errorf("openai input_tokens api missing input_tokens: %s", string(respBody))
	}
	return int(value.Int()), nil
}

// maxLocalInt returns the larger integer for the live-comparison error
// allowance.
func maxLocalInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
