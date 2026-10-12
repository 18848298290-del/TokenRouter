package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	billingcore "github.com/TokenFlux/TokenRouter/internal/billing"
	billingpricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	catalogprovider "github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openAIWSPolicyRepo records provider error writes triggered by WebSocket policy.
type openAIWSPolicyRepo struct {
	transientCooldownProviderRepo
	setErrorCalls int
}

func TestOpenAIWSTerminalEvent_ResponseFailedRecordsModelTransient(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	setWSFixtureHealth(svc, newUpstreamHealthForTest(transientCooldownProviderRepo{}, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil))

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 5201, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"Internal error"}}}`)

	for range 2 {
		terminalPolicy := svc.handleOpenAIWSTerminalTransientFailure(context.Background(), provider, "gpt-5.5", http.Header{}, payload)
		require.Equal(t, "response.failed", terminalPolicy.TerminalEvent)
		require.Equal(t, http.StatusBadGateway, terminalPolicy.StatusCode)
	}

	require.True(t, wsFixtureModelBlocked(svc, provider, "gpt-5.5"))
}

// TestOpenAIWSTerminalFailureReturnsExplicitPolicyDecision checks that response.failed
// returns the applied provider policy to the client-event writer.
func TestOpenAIWSTerminalFailureReturnsExplicitPolicyDecision(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	repo := &openAIWSPolicyRepo{}
	setWSFixtureHealth(svc, newUpstreamHealthForTest(repo, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil))

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5206,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusUnprocessableEntity)},
			},
		},
	}
	payload := []byte(`{"type":"response.failed","response":{"error":{"status_code":422,"message":"configured"}}}`)

	terminalPolicy := svc.handleOpenAIWSTerminalTransientFailure(
		context.Background(), provider, "gpt-5.5", http.Header{}, payload,
	)

	require.Equal(t, "response.failed", terminalPolicy.TerminalEvent)
	require.Equal(t, http.StatusUnprocessableEntity, terminalPolicy.StatusCode)
	require.Equal(t, providercore.ErrorPolicyCustomMatched, terminalPolicy.Decision.Policy)
	require.True(t, terminalPolicy.Decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(provider), terminalPolicy.StatusCode, false))
	require.Equal(t, 1, repo.setErrorCalls)
}

// TestOpenAIWSTerminalContentPolicyBypassesProviderPolicy checks that content rejections bypass custom policy even when mapped to 502.
func TestOpenAIWSTerminalContentPolicyBypassesProviderPolicy(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	repo := &openAIWSPolicyRepo{}
	setWSFixtureHealth(svc, newUpstreamHealthForTest(repo, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil))

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5207,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusBadGateway)},
			},
		},
	}
	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"content_policy","message":"request blocked by policy"}}}`)

	terminalPolicy := svc.handleOpenAIWSTerminalTransientFailure(
		context.Background(), provider, "gpt-5.5", http.Header{}, payload,
	)

	require.Equal(t, http.StatusBadGateway, terminalPolicy.StatusCode)
	require.Equal(t, providercore.ErrorPolicyNone, terminalPolicy.Decision.Policy)
	require.False(t, terminalPolicy.Decision.ShouldFailover(
		gatewayprovider.ExecutionErrorPolicy(provider), terminalPolicy.StatusCode, openai.OpenAIStreamFailedEventShouldFailover(payload, "request blocked by policy"),
	))
	require.Zero(t, repo.setErrorCalls)
}

func TestOpenAIWSErrorEvent_ServerErrorRecordsModelTransient(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	setWSFixtureHealth(svc, newUpstreamHealthForTest(transientCooldownProviderRepo{}, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil))

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 5203, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	payload := []byte(`{"type":"error","error":{"code":"server_error","type":"server_error","message":"Internal error"}}`)

	for range 2 {
		svc.handleOpenAIWSErrorEventTransientFailure(context.Background(), provider, "gpt-5.5", http.Header{}, payload)
	}

	require.True(t, wsFixtureModelBlocked(svc, provider, "gpt-5.5"))
}

func TestOpenAIWSPayloadTransientStatus_Explicit529IsNotModelTransient(t *testing.T) {
	payload := []byte(`{"type":"response.failed","response":{"error":{"status_code":529,"code":"server_error","message":"overloaded"}}}`)

	require.Zero(t, openAIWSPayloadTransientStatus(payload))
}

func TestOpenAIWSErrorPolicyStatus_PreservesExplicitStatusAndFallbackMapping(t *testing.T) {
	require.Equal(t, 529, openAIWSErrorPolicyStatus([]byte(`{"type":"response.failed","response":{"error":{"status_code":529,"code":"server_error"}}}`)))
	require.Equal(t, http.StatusBadGateway, openAIWSErrorPolicyStatus([]byte(`{"type":"error","error":{"type":"server_error"}}`)))
}

func TestOpenAIWSDial5xxRecordsModelTransient(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	setWSFixtureHealth(svc, newUpstreamHealthForTest(transientCooldownProviderRepo{}, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil))

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 5202, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	dialErr := &openai.WSDialError{
		StatusCode:      http.StatusBadGateway,
		ResponseHeaders: http.Header{"X-Request-Id": []string{"req-ws-502"}},
		ResponseBody:    []byte(`{"error":{"message":"bad gateway"}}`),
	}

	for range 2 {
		svc.handleOpenAIWSDialTransientFailure(context.Background(), provider, "gpt-5.5", dialErr)
	}

	require.Eventually(t, func() bool {
		return wsFixtureModelBlocked(svc, provider, "gpt-5.5")
	}, time.Second, 10*time.Millisecond)
}

// TestOpenAIWSPoolModeErrorUsesConfiguredRetry checks pool retry rules without adding default transient model cooldowns.
func TestOpenAIWSPoolModeErrorUsesConfiguredRetry(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	setWSFixtureHealth(svc, newUpstreamHealthForTest(transientCooldownProviderRepo{}, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil))

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5204,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"pool_mode":                    true,
				"pool_mode_retry_status_codes": []any{float64(http.StatusBadGateway)},
			},
		},
	}

	decision := svc.applyOpenAIWSEventErrorPolicy(
		context.Background(), provider, "gpt-5.5", http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"bad gateway"}}`),
	)

	require.Equal(t, providercore.ErrorPolicyPoolBypassed, decision.Policy)
	require.True(t, decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), http.StatusBadGateway))
	require.False(t, wsFixtureModelBlocked(svc, provider, "gpt-5.5"))
}

func (r *openAIWSPolicyRepo) SetError(context.Context, int64, string) error {
	r.setErrorCalls++
	return nil
}

// TestOpenAIWSCustomNonFailoverStatusStopsScheduling checks administrator policy for a derived status
// outside the default failover set and disables same-provider retries.
func TestOpenAIWSCustomNonFailoverStatusStopsScheduling(t *testing.T) {
	svc := newWSFixture(wsFixtureInputs{})
	repo := &openAIWSPolicyRepo{}
	setWSFixtureHealth(svc, newUpstreamHealthForTest(repo, &wsFixtureOptions{}, nil, providercore.HealthOptions{}, nil))

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 5205,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"pool_mode":                  true,
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusUnprocessableEntity)},
			},
		},
	}

	decision := svc.applyOpenAIWSEventErrorPolicy(
		context.Background(), provider, "gpt-5.5", http.StatusUnprocessableEntity, http.Header{}, []byte(`{"error":{"message":"configured"}}`),
	)

	require.Equal(t, providercore.ErrorPolicyCustomMatched, decision.Policy)
	require.True(t, decision.StopScheduling)
	require.False(t, decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), http.StatusUnprocessableEntity))
	require.Equal(t, 1, repo.setErrorCalls)
}

func fastModeTestContext(policy, model string) context.Context {
	ctx := apikey.WithFastModePolicy(context.Background(), policy)
	ctx = requeststate.WithGroup(ctx, &routing.Group{ID: 11})
	return context.WithValue(ctx, telemetry.Model, model)
}

func fastModeTestResolver() *billingcore.PriceResolver {
	pricing := catalogprovider.NewServiceFromSnapshot(catalogprovider.Options{ModelLookupCandidates: modelidentity.CandidatesFactory}, nil, catalogprovider.Snapshot{Data: map[string]*billingpricing.CatalogModelPricing{
		"gpt-5.5": {
			InputCostPerToken:     5e-6,
			OutputCostPerToken:    30e-6,
			SupportsServiceTier:   true,
			SupportsPromptCaching: true,
		},
		"claude-opus-4-8": {
			InputCostPerToken:     5e-6,
			OutputCostPerToken:    25e-6,
			SupportsServiceTier:   true,
			SupportsPromptCaching: true,
		},
	}})
	billing := billingtestkit.Calculator(pricing, nil)
	return billingtestkit.PriceResolver(nil, billing)
}

// TestOpenAIWSFastModePolicyContextRefreshesEachTurn checks that each turn reads current policy.
func TestOpenAIWSFastModePolicyContextRefreshesEachTurn(t *testing.T) {
	svc := newWSFastPolicy(t, tierpolicy.Default())
	svc.Prices = fastModeTestResolver()
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	baseCtx := fastModeTestContext(apikey.APIKeyFastModePolicyForceOn, "gpt-5.5")
	policies := map[int]string{
		1: apikey.APIKeyFastModePolicyForceOn,
		2: apikey.APIKeyFastModePolicyForceOff,
	}
	hooks := &gatewayws.OpenAIIngressHooks{
		ResolveFastModePolicy: func(turn int) string {
			return policies[turn]
		},
	}

	turnOneCtx := openAIWSFastModePolicyContext(baseCtx, hooks, 1)
	updated, blocked, err := gatewayws.ApplyServiceTierFrame([]byte(`{"type":"response.create","model":"gpt-5.5"}`), "gpt-5.5", svc.Input(turnOneCtx, provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, tierpolicy.OpenAIFastTierPriority, gjson.GetBytes(updated, "service_tier").String())

	turnTwoCtx := openAIWSFastModePolicyContext(baseCtx, hooks, 2)
	updated, blocked, err = gatewayws.ApplyServiceTierFrame([]byte(`{"type":"response.create","model":"gpt-5.5","service_tier":"priority"}`), "gpt-5.5", svc.Input(turnTwoCtx, provider, "gpt-5.5"))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.False(t, gjson.GetBytes(updated, "service_tier").Exists())
}
