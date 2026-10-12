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
	transientCooldownProviderRepo     // default health repository test behavior
	setErrorCalls                 int // provider error write count
}

// TestOpenAIWSTerminalEvent_ResponseFailedRecordsModelTransient applies the
// same server-error terminal twice for an API Key provider. Both decisions
// retain response.failed and 502, and the selected model becomes blocked.
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

// TestOpenAIWSTerminalFailureReturnsExplicitPolicyDecision configures custom
// handling for status 422 and submits a failed terminal. The returned
// custom-match decision permits failover and records one provider error
// alongside the supplied terminal status.
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

// TestOpenAIWSTerminalContentPolicyBypassesProviderPolicy submits a
// content-policy failed terminal with a custom-rule repository. Its derived
// 502 receives ErrorPolicyNone, remains ineligible for failover, and writes no
// provider error.
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

// TestOpenAIWSErrorEvent_ServerErrorRecordsModelTransient applies a standalone
// server-error frame twice for an API Key provider. The health observations
// mark the selected model blocked under the WS error policy.
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

// TestOpenAIWSPayloadTransientStatus_Explicit529IsNotModelTransient combines
// an explicit 529 status with a server-error code in a failed terminal.
// Transient-status extraction returns zero, protecting numeric status
// precedence over the text classifier.
func TestOpenAIWSPayloadTransientStatus_Explicit529IsNotModelTransient(t *testing.T) {
	payload := []byte(`{"type":"response.failed","response":{"error":{"status_code":529,"code":"server_error","message":"overloaded"}}}`)

	require.Zero(t, openAIWSPayloadTransientStatus(payload))
}

// TestOpenAIWSErrorPolicyStatus_PreservesExplicitStatusAndFallbackMapping
// compares a failed terminal carrying 529 with a standalone server-error type
// carrying no status. Policy extraction retains 529 and maps the latter event
// to 502.
func TestOpenAIWSErrorPolicyStatus_PreservesExplicitStatusAndFallbackMapping(t *testing.T) {
	require.Equal(t, 529, openAIWSErrorPolicyStatus([]byte(`{"type":"response.failed","response":{"error":{"status_code":529,"code":"server_error"}}}`)))
	require.Equal(t, http.StatusBadGateway, openAIWSErrorPolicyStatus([]byte(`{"type":"error","error":{"type":"server_error"}}`)))
}

// TestOpenAIWSDial5xxRecordsModelTransient applies repeated 502 handshake
// failures with response diagnostics to an API Key provider. The selected
// model becomes blocked within the polling window after these health
// observations.
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

// TestOpenAIWSPoolModeErrorUsesConfiguredRetry submits 502 under a pool-mode
// provider's configured retry list. Policy reports PoolBypassed and
// same-provider retry eligibility while leaving the selected model outside the
// default transient block.
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

// SetError counts provider error writes made by the WS policy fixtures.
func (r *openAIWSPolicyRepo) SetError(context.Context, int64, string) error {
	r.setErrorCalls++
	return nil
}

// TestOpenAIWSCustomNonFailoverStatusStopsScheduling combines pool mode with a
// custom 422 policy match. The administrator's match stops scheduling,
// disables same-provider retry, and writes the provider error exactly once.
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

// fastModeTestContext returns a fresh context carrying the supplied Key Fast
// policy, group 11, and model. These inputs let Fast policy fixtures resolve
// group prices and policy precedence as an authenticated turn would.
func fastModeTestContext(policy, model string) context.Context {
	ctx := apikey.WithFastModePolicy(context.Background(), policy)
	ctx = requeststate.WithGroup(ctx, &routing.Group{ID: 11})
	return context.WithValue(ctx, telemetry.Model, model)
}

// fastModeTestResolver returns a calculator-backed resolver with fixed GPT and
// Claude tier-capable catalog prices. The resolver has no repositories, so
// Fast policy checks use those test prices and cannot read live pricing state.
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

// TestOpenAIWSFastModePolicyContextRefreshesEachTurn supplies force-on then
// force-off from the turn policy hook over a force-on base context. The first
// frame receives priority, and the second frame loses its supplied
// service_tier.
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
