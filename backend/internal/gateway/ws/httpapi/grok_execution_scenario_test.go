package httpapi

// Grok execution scenarios cover model resolution, provider health updates, and HTTP error output.

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

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

func TestGrokFinalUpstreamModelNormalization(t *testing.T) {
	tests := []struct {
		name     string
		provider *gatewayprovider.ExecutionProvider
		model    string
		want     string
	}{
		{
			name:     "oauth normalizes builtin alias",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}},
			model:    "grok",
			want:     "grok",
		},
		{
			name:     "api key normalizes builtin alias",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey}},
			model:    " grok-latest ",
			want:     "grok-latest",
		},
		{
			name:     "grok oauth does not use codex normalization",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}},
			model:    "gpt-5.6",
			want:     "gpt-5.6",
		},
		{
			name:     "unknown model passes through",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey}},
			model:    "custom-grok-model",
			want:     "custom-grok-model",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, gatewayprovider.ExecutionModelPolicy(test.provider).NormalizeOpenAI(test.model))
		})
	}
}

// TestGrokExplicitMappingPrecedesBuiltinNormalization checks platform alias resolution after provider mapping.
func TestGrokExplicitMappingPrecedesBuiltinNormalization(t *testing.T) {
	direct := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"grok": "grok-4.3"},
			},
		},
	}
	require.Equal(t, "grok-4.3", gatewayprovider.ExecutionModelPolicy(direct).OpenAIUpstream("grok", false))

	aliasTarget := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"client-alias": "grok-latest"},
			},
		},
	}
	require.Equal(t, "grok-latest", gatewayprovider.ExecutionModelPolicy(aliasTarget).OpenAIUpstream("client-alias", false))
	require.Equal(t, "grok-latest", gatewayprovider.ExecutionModelPolicy(aliasTarget).UpstreamModel(context.Background(), "client-alias"))
}

// TestGrokRuntimeModelKeysUseFinalUpstreamID checks that aliases share blocking and rate-limit keys.
func TestGrokRuntimeModelKeysUseFinalUpstreamID(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"client-alias": "grok-latest",
					"grok-4.5":     "grok-4.3",
				},
			},
		},
	}

	require.Equal(t, "grok", gatewayprovider.ExecutionModelPolicy(provider).CanonicalSchedulingModel("grok"))
	require.Equal(t, "grok-latest", gatewayprovider.ExecutionModelPolicy(provider).CanonicalSchedulingModel("client-alias"))
	require.Equal(t, []string{"grok"}, gatewayprovider.ExecutionModelPolicy(provider).LimitKeys(context.Background(), "grok"))
	require.Equal(t, "grok", (&provideradapter.ModelHealth{}).LimitKey(gatewayprovider.ExecutionRecord(provider), "grok", nil))
	// The final upstream model must not be remapped from grok-4.5 to grok-4.3 during health handling.
	require.Equal(t, grok.DefaultResponsesModel, (&provideradapter.ModelHealth{}).LimitKey(gatewayprovider.ExecutionRecord(provider), grok.DefaultResponsesModel, nil))
}

// TestGrokModelNotFoundWritesFinalUpstreamID checks the model key written by default error handling.
func TestGrokModelNotFoundWritesFinalUpstreamID(t *testing.T) {
	repo := &grokModelStateProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{health: newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 4511,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"client-alias": "grok-latest",
					"grok-4.5":     "grok-4.3",
				},
			},
		},
	}
	providerMappedModel := gatewayprovider.ExecutionModelPolicy(provider).Mapped("client-alias")
	require.Equal(t, "grok-latest", providerMappedModel)

	decision := gatewayprovider.ApplyGrokExecutionHealth(context.Background(), svc.Output.GrokHealth, provider, http.StatusNotFound, nil, []byte(`{"error":{"code":"model_not_found","message":"model not found"}}`), "", providerMappedModel)

	require.True(t, decision.StopScheduling)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "grok-latest", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, providercore.ModelNotFoundReason, repo.modelRateLimitCalls[0].reason)
	require.False(t, wsFixtureProviderBlocked(svc, provider))
}

// TestGrokTransientErrorBlocksOnlyFinalModel checks that repeated API Key transient errors cool down the final model.
func TestGrokTransientErrorBlocksOnlyFinalModel(t *testing.T) {
	repo := &grokModelStateProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{health: newUpstreamHealthForTest(repo, nil, nil, providercore.HealthOptions{}, nil)})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 4512,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"client-alias": "grok-latest",
					"grok-4.5":     "grok-4.3",
				},
			},
		},
	}
	canonicalModel := gatewayprovider.ExecutionModelPolicy(provider).NormalizeOpenAI(gatewayprovider.ExecutionModelPolicy(provider).Mapped("client-alias"))
	body := []byte(`{"error":{"message":"temporary upstream failure"}}`)

	first := gatewayprovider.ApplyGrokExecutionHealth(context.Background(), svc.Output.GrokHealth, provider, http.StatusBadGateway, nil, body, "", canonicalModel)
	second := gatewayprovider.ApplyGrokExecutionHealth(context.Background(), svc.Output.GrokHealth, provider, http.StatusBadGateway, nil, body, "", canonicalModel)

	require.False(t, first.StopScheduling)
	require.False(t, second.StopScheduling)
	require.False(t, wsFixtureProviderBlocked(svc, provider))
	require.True(t, wsFixtureModelBlocked(svc, provider, "client-alias"))
	require.False(t, wsFixtureModelBlocked(svc, provider, "grok-4.3"))
	require.Empty(t, repo.modelRateLimitCalls)
}

// TestGrokCountTokensUsesCanonicalModel checks that count-tokens records the mapped model and sends the final model.
func TestGrokCountTokensUsesCanonicalModel(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok,
			Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"claude-sonnet-4-5": "grok-latest"},
			},
		},
	}
	prepared, err := gatewayprovider.PrepareAnthropicInputTokens(
		[]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`),
		provider,
		"",
	)
	require.NoError(t, err)
	require.Equal(t, "grok-latest", prepared.BillingModel)
	require.Equal(t, "grok-latest", prepared.UpstreamModel)
	require.Equal(t, "grok-latest", prepared.Request.Model)
}

func TestIsGrokContentPolicyRejection(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "new sensitive code",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"new_sensitive","message":"image is sensitive"}}`,
			want:   true,
		},
		{
			name:   "content policy violation code",
			status: http.StatusForbidden,
			body:   `{"response":{"error":{"code":"content_policy_violation"}}}`,
			want:   true,
		},
		{
			name:   "cyber policy code",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"cyber_policy","message":"request rejected"}}`,
			want:   true,
		},
		{
			name:   "moderation feature unavailable",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"The moderation feature is not available for this request"}}`,
			want:   true,
		},
		{
			name:   "explicit prompt moderation rejection",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"request rejected by content moderation"}}`,
			want:   true,
		},
		{
			name:   "entitlement forbidden",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"subscription required"}}`,
			want:   false,
		},
		{
			name:   "provider policy suspension is not request policy",
			status: http.StatusForbidden,
			body:   `{"error":{"message":"account suspended due to policy violation"}}`,
			want:   false,
		},
		{
			name:   "structured provider suspension overrides policy reason",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"account_suspended","reason":"policy_violation","message":"account suspended due to policy violation"}}`,
			want:   false,
		},
		{
			name:   "ambiguous policy violation code is not enough",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"policy_violation","message":"policy violation"}}`,
			want:   false,
		},
		{
			name:   "policy violation with request scoped message",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"policy_violation","message":"request blocked by policy"}}`,
			want:   true,
		},
		{
			name:   "permission-denied usage guidelines is request scoped",
			status: http.StatusForbidden,
			body:   `{"code":"permission-denied","error":"Content violates usage guidelines. "}`,
			want:   true,
		},
		{
			name:   "permission-denied entitlement stays on the provider path",
			status: http.StatusForbidden,
			body:   `{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`,
			want:   false,
		},
		{
			name:   "structured provider code overrides usage guidelines phrase",
			status: http.StatusForbidden,
			body:   `{"error":{"code":"account_suspended","message":"Content violates usage guidelines."}}`,
			want:   false,
		},
		{
			name:   "wrong status",
			status: http.StatusBadRequest,
			body:   `{"error":{"code":"new_sensitive"}}`,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, grok.IsGrokContentPolicyRejection(tt.status, []byte(tt.body)))
		})
	}
}

func TestGrokContentPolicy403SharedErrorFallbackDoesNotMutate(t *testing.T) {
	body := []byte(`{"error":{"code":"content_filter","message":"prohibited content"}}`)
	repo := &grokQuotaProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{providers: repo})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 4719,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusTooManyRequests)},
			},
		},
	}

	newContext := func() (*gin.Context, *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		return c, recorder
	}

	c, recorder := newContext()
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
	_, err := svc.Output.ResponseError(context.Background(), resp, c, provider, nil, "grok-4.5")
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "invalid_request_error")

	c, recorder = newContext()
	resp = &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
	_, err = svc.Output.CompatError(resp, c, provider, gatewayhttp.WriteForwardChatError, gatewayhttp.WriteForwardChatErrorBody, "grok-4.5")
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "invalid_request_error")

	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
}

func TestGrokContentPolicy403MediaResponseBypassesCustomErrorCodes(t *testing.T) {
	body := `{"error":{"code":"new_sensitive","message":"image is sensitive"}}`
	repo := &grokQuotaProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{providers: repo})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 4720,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusTooManyRequests)},
			},
		},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	_, err := mediaErrorResponseFixture(svc.Grok, context.Background(), resp, c, provider, "request-id", "grok-imagine")
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "invalid_request_error")
	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
}

func TestGrokPermissionDeniedContentRefusalDoesNotMutateOrFailover(t *testing.T) {
	repo := &grokQuotaProviderRepo{}
	svc := newWSFixture(wsFixtureInputs{providers: repo})
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 4785, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	body := []byte(`{"code":"permission-denied","error":"Content violates usage guidelines. "}`)

	svc.handleGrokProviderUpstreamError(context.Background(), provider, http.StatusForbidden, nil, body)

	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
	require.False(t, wsFixtureProviderBlocked(svc, provider))
	require.False(t, gatewayprovider.ShouldFailoverGrokResponse(http.StatusForbidden, body))
}

func mediaErrorResponseFixture(executor *gatewayhttp.GrokExecutor, ctx context.Context, resp *http.Response, c *gin.Context, target *gatewayprovider.ExecutionProvider, requestID, model string) (*forward.OpenAIResult, error) {
	local := *executor
	local.Transport = &auxiliaryHTTPRecorder{resp: resp}
	local.Credentials = testkit.RequestCredentials(nil, &providercore.OpenAIExecutionCredentials{Grok: func(context.Context, *providercore.Record) (string, error) { return "fixture-token", nil }}, nil, nil)
	local.Credentials.HasGrokTokenSource = true
	if target.Record.Credentials == nil {
		target.Record.Credentials = map[string]any{}
	}
	target.Record.Credentials["api_key"] = "fixture-token"
	resp.Header.Set("x-request-id", requestID)
	return local.ForwardGrokMedia(ctx, c, target, grok.GrokMediaEndpointImagesGenerations, "", []byte(`{"model":"`+model+`","prompt":"fixture"}`), "application/json")
}
