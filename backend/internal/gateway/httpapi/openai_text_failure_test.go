package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type textFailureStore struct {
	gatewayprovider.ExecutionProviderStore
	tempUnschedCalls, rateLimitedCalls, updateCalls int
	modelRateLimitProviderID                        int64
	modelRateLimitKey                               string
}

func (s *textFailureStore) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	s.tempUnschedCalls++
	return nil
}

func (s *textFailureStore) SetRateLimited(context.Context, int64, time.Time) error {
	s.rateLimitedCalls++
	return nil
}

func (s *textFailureStore) SetRateLimitedIfLater(c context.Context, id int64, t time.Time) error {
	return s.SetRateLimited(c, id, t)
}

func (s *textFailureStore) UpdateExtra(context.Context, int64, map[string]any) error {
	s.updateCalls++
	return nil
}

func (s *textFailureStore) SetModelRateLimit(_ context.Context, id int64, key string, _ time.Time, _ ...string) error {
	s.modelRateLimitProviderID = id
	s.modelRateLimitKey = key
	return nil
}

// textFailureFixture combines provider health, blocking, and protocol classification with a write-recording
// store stub.
func textFailureFixture(store gatewayprovider.ExecutionProviderStore, observe bool) *OpenAITextExecutor {
	blocks := providercore.NewRuntimeBlockState(time.Now)
	models := providercore.NewModelTransientState(0)
	var health *provideradapter.UpstreamHealth
	if observe {
		health = gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: store, Options: providercore.HealthOptions{Block: blocks.BlockProviderScheduling}})
	}
	grokHealth := &provideradapter.GrokHealth{NormalizeModel: func(value *providercore.Record, model string) string {
		return (gatewayprovider.ModelPolicy{Record: value}).NormalizeOpenAI(model)
	}, Store: store, Health: health, Runtime: blocks, ModelTransient: models}
	return &OpenAITextExecutor{Output: &OpenAIResponseOutput{Health: &provideradapter.OpenAIResponseHealth{Health: health, Runtime: blocks, ModelTransient: models}}, Grok: &GrokExecutor{Health: grokHealth}}
}

// TestOpenAIRequestBodyLimitFailover_CompatAndWSBridgeKeepProviderFailover checks that
// Chat error handling and the WS HTTP bridge treat provider proxy 413 responses as provider failures.
func TestOpenAIRequestBodyLimitFailover_CompatAndWSBridgeKeepProviderFailover(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(nil))

	const upstreamMessage = "request body exceeds this provider's proxy limit"
	upstreamBody := []byte(`{"error":{"message":"` + upstreamMessage + `"}}`)
	resp := &http.Response{
		StatusCode: http.StatusRequestEntityTooLarge,
		Header:     http.Header{"X-Request-Id": []string{"rid-compat-413"}},
	}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 163, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	svc := textFailureFixture(nil, false)

	failoverErr := svc.httpFailover(
		context.Background(), c, provider, resp, upstreamBody, upstreamMessage, "gpt-5.2",
	)

	require.NotNil(t, failoverErr)
	require.Equal(t, forwardcore.GatewayFailureScopeProvider, failoverErr.Scope)
	require.Equal(t, forwardcore.NextProviderRetry, failoverErr.NextProviderAction)
	require.False(t, failoverErr.RetryableOnSameProvider)
	require.False(t, gatewayprovider.OpenAIWSHTTPBridgeRequestScopedError(
		provider, http.StatusRequestEntityTooLarge, upstreamMessage, upstreamBody,
	))
}

func TestFailoverOpenAIUpstreamHTTPError_NilContextSkipsTempUnschedulablePolicy(t *testing.T) {
	repo := &textFailureStore{}
	svc := textFailureFixture(repo, true)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5099, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{map[string]any{
					"error_code":       float64(http.StatusBadRequest),
					"keywords":         []any{"custom temporary outage"},
					"duration_minutes": float64(1),
				}},
			},
		},
	}
	body := []byte(`{"error":{"message":"Custom temporary outage."}}`)
	resp := &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{}}

	got := svc.httpFailover(
		context.Background(), nil, provider, resp, body,
		"Custom temporary outage.", "gpt-5.4",
	)

	require.Nil(t, got)
	require.Zero(t, repo.modelRateLimitProviderID)
	require.Empty(t, repo.modelRateLimitKey)
}

func TestGrokContentPolicy403DoesNotMutateOrFailover(t *testing.T) {
	repo := &textFailureStore{}
	svc := textFailureFixture(repo, false)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 4715, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	body := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)

	gatewayprovider.ApplyGrokExecutionHealth(context.Background(), svc.Grok.Health, provider, http.StatusForbidden, nil, body, "")

	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
	require.False(t, svc.Output.Health.Runtime.Blocked(provider.Record.ID, nil))
	require.False(t, gatewayprovider.ShouldFailoverGrokResponse(http.StatusForbidden, body))

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
	got := svc.httpFailover(context.Background(), c, provider, resp, body, "text is sensitive", "grok-4.5")
	require.Nil(t, got)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestGrokNonFailoverDoesNotApplyGenericTempUnschedulablePolicy(t *testing.T) {
	repo := &textFailureStore{}
	svc := textFailureFixture(repo, true)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5099,
			Platform: capability.PlatformGrok,
			Type:     capability.ProviderTypeOAuth,
			Credentials: map[string]any{
				"temp_unschedulable_enabled": true,
				"temp_unschedulable_rules": []any{map[string]any{
					"error_code":       float64(http.StatusForbidden),
					"keywords":         []any{"text is sensitive"},
					"duration_minutes": float64(1),
				}},
			},
		},
	}
	body := []byte(`{"error":{"code":"new_sensitive","message":"text is sensitive"}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}

	got := svc.httpFailover(
		context.Background(), c, provider, resp, body, "text is sensitive", "",
	)

	require.Nil(t, got)
	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
	require.False(t, svc.Output.Health.Runtime.Blocked(provider.Record.ID, nil))
}

func TestFailoverOpenAIUpstreamHTTPErrorUsesOnlyGrokRateLimitPolicy(t *testing.T) {
	repo := &textFailureStore{}
	svc := textFailureFixture(repo, false)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 70, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"45"}},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	failoverErr := svc.httpFailover(
		context.Background(), c, provider, resp,
		[]byte(`{"error":{"message":"rate limited"}}`), "rate limited", "grok-4.3",
	)

	require.NotNil(t, failoverErr)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Zero(t, repo.tempUnschedCalls)
}

// TestOpenAIPoolModeTempRule_StopsSameProviderRetryAndIsolatesBlockToModel checks model-scoped temporary
// blocking (#4547).
// Known models cool down by provider and model; unknown models cool down the provider.
// See TestOpenAITempUnschedulable_UnknownModelKeepsProviderRuntimeBlock.
// Pool rules stop same-provider retries and temporarily block the matching model (issue 4470).
func TestOpenAIPoolModeTempRule_StopsSameProviderRetryAndIsolatesBlockToModel(t *testing.T) {
	repo := &gatewaytestkit.ErrorPolicyStore{}
	gateway := textFailureFixture(repo, true)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 46,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"pool_mode":                    true,
				"pool_mode_retry_status_codes": []any{float64(http.StatusServiceUnavailable)},
				"temp_unschedulable_enabled":   true,
				"temp_unschedulable_rules": []any{
					map[string]any{
						"error_code":       float64(http.StatusServiceUnavailable),
						"keywords":         []any{"unavailable"},
						"duration_minutes": float64(30),
					},
				},
			},
		},
	}
	body := []byte(`{"error":{"message":"Service temporarily unavailable"}}`)
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{},
	}

	failoverErr := gateway.httpFailover(
		context.Background(),
		nil,
		provider,
		resp,
		body,
		"Service temporarily unavailable",
		"gpt-5.4",
	)

	require.NotNil(t, failoverErr)
	require.False(t, failoverErr.RetryableOnSameProvider)
	require.Zero(t, repo.TempCalls)
	require.Equal(t, 0, repo.SetErrCalls)
	require.Equal(t, billing.StatusActive, provider.Record.Status)
	require.Len(t, repo.ModelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.ModelRateLimitCalls[0].Scope)
	require.False(t, gateway.Output.Health.Runtime.Blocked(provider.Record.ID, nil))
	require.False(t, gateway.Output.Health.Runtime.Blocked(provider.Record.ID, nil) || gateway.Output.Health.ModelTransient.IsBlocked(provider.Record.ID, "gpt-5.5", time.Now()))
}
