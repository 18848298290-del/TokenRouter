package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/openaiattempt"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

type turnAuthentication struct {
	key        *apikey.APIKey
	err        error
	input      apikey.AuthenticationInput
	acquired   *apikey.APIKey
	acquireErr error
}

type turnSubscriptions struct {
	current *billing.UserSubscription
}

type entryKeyReader struct{ calls int }

func (a *turnAuthentication) GetByKey(context.Context, string) (*apikey.APIKey, error) {
	panic("unexpected cached lookup")
}

func (a *turnAuthentication) Reauthenticate(_ context.Context, _ *apikey.APIKey, input apikey.AuthenticationInput) (*apikey.APIKey, error) {
	a.input = input
	return a.key, a.err
}

// AcquireRequest supplies releasable admission for authentication tests with no limiter.
func (a *turnAuthentication) AcquireRequest(ctx context.Context, key *apikey.APIKey) (context.Context, func(), time.Duration, error) {
	a.acquired = key
	return ctx, func() {}, 0, a.acquireErr
}

// TestAuthorizeTurn checks funding with the current snapshot and retains the prior snapshot on authentication failure.
func TestAuthorizeTurn(t *testing.T) {
	for _, name := range []string{"allowed", "deleted", "funding_denied", "subscription_missing"} {
		t.Run(name, func(t *testing.T) {
			original := &apikey.APIKey{ID: 1, User: &identity.User{ID: 2}, BillingMode: billing.APIKeyBillingModeBalance, RateLimit5h: 10}
			original.Group = &routing.Group{AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolResponsesWebSocket}}
			current := apikey.CopyAPIKey(original)
			current.RateLimit5h = 1
			auth := &turnAuthentication{key: current}
			if name == "deleted" {
				auth.err = apikey.ErrAPIKeyNotFound
			}
			if name == "subscription_missing" {
				current.BillingMode = billing.APIKeyBillingModeSubscription
			}
			called := false
			denied := errors.New("insufficient funds")
			adapter := &openAIWSEntryAdapter{key: original, bindings: Bindings{Keys: auth, CheckFunding: func(_ context.Context, key *apikey.APIKey, sub *billing.UserSubscription, _ string, afterWait bool) error {
				called = true
				require.Same(t, current, key)
				require.Nil(t, sub)
				require.False(t, afterWait)
				if name == "funding_denied" {
					return denied
				}
				return nil
			}}}
			adapter.call.ClientIP = "192.0.2.1"
			err := adapter.AuthorizeTurn(context.Background())
			if name == "allowed" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, name == "allowed" || name == "funding_denied", called)
			require.True(t, auth.input.CheckMemberLimits)
			require.Equal(t, "192.0.2.1", auth.input.ClientIP)
			require.Equal(t, 10.0, original.RateLimit5h)
		})
	}
}

func (s turnSubscriptions) GetUsableSubscription(context.Context, int64, ...int64) (*billing.UserSubscription, bool, error) {
	return s.current, false, nil
}

func (s turnSubscriptions) GetSubscriptionForAPIKey(context.Context, int64, int64) (*billing.UserSubscription, error) {
	return s.current, nil
}

func (s turnSubscriptions) ValidateAndCheckLimits(*billing.UserSubscription) (bool, error) {
	return false, nil
}

// TestAuthorizeTurnReloadsSubscription checks funding against current subscription usage.
func TestAuthorizeTurnReloadsSubscription(t *testing.T) {
	key := &apikey.APIKey{ID: 1, User: &identity.User{ID: 2}, Group: &routing.Group{AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolResponsesWebSocket}}}
	old := &billing.UserSubscription{ID: 3, DailyUsageUSD: 0}
	current := &billing.UserSubscription{ID: 3, DailyUsageUSD: 10}
	denied := errors.New("subscription exhausted")
	adapter := &openAIWSEntryAdapter{key: key, subscription: old, bindings: Bindings{
		Keys: &turnAuthentication{key: key}, Subscriptions: turnSubscriptions{current: current},
		CheckFunding: func(_ context.Context, _ *apikey.APIKey, sub *billing.UserSubscription, _ string, _ bool) error {
			require.Same(t, current, sub)
			return denied
		},
	}}
	require.ErrorIs(t, adapter.AuthorizeTurn(context.Background()), denied)
	require.Same(t, old, adapter.subscription)
	require.Zero(t, old.DailyUsageUSD)
}

// TestKeyAdmissionUsesCurrentTurnLimits verifies that later turns use limits from reauthentication.
func TestKeyAdmissionUsesCurrentTurnLimits(t *testing.T) {
	original := &apikey.APIKey{ID: 1, User: &identity.User{ID: 2}, BillingMode: billing.APIKeyBillingModeBalance, RPMLimit: 20}
	original.Group = &routing.Group{AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolResponsesWebSocket}}
	current := apikey.CopyAPIKey(original)
	current.RPMLimit = 1
	auth := &turnAuthentication{key: current}
	adapter := &openAIWSEntryAdapter{key: original, bindings: Bindings{Keys: auth, CheckFunding: func(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error { return nil }}}
	_, release, err := adapter.AcquireKey(context.Background())
	require.NoError(t, err)
	release()
	require.Same(t, original, auth.acquired)
	require.NoError(t, adapter.AuthorizeTurn(context.Background()))
	auth.acquireErr = apikey.ErrKeyRPMExceeded
	_, _, err = adapter.AcquireKey(context.Background())
	require.ErrorIs(t, err, apikey.ErrKeyRPMExceeded)
	require.Equal(t, 1013, adapter.CloseInfo(err).Status)
	require.Same(t, current, auth.acquired)
	require.Equal(t, 20, original.RPMLimit)
}

// TestAuthorizeTurnRejectsRevokedWSProtocol rejects before funding checks and keeps the completed-turn snapshot independent.
func TestAuthorizeTurnRejectsRevokedWSProtocol(t *testing.T) {
	original := &apikey.APIKey{
		ID: 1, User: &identity.User{ID: 2}, BillingMode: billing.APIKeyBillingModeBalance,
		Group: &routing.Group{ID: 3, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolResponsesWebSocket}},
	}
	current := apikey.CopyAPIKey(original)
	current.Group.AllowedProtocols = []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}
	fundingCalled := false
	adapter := &openAIWSEntryAdapter{key: original, bindings: Bindings{
		Keys: &turnAuthentication{key: current},
		CheckFunding: func(context.Context, *apikey.APIKey, *billing.UserSubscription, string, bool) error {
			fundingCalled = true
			return nil
		},
	}}
	require.Error(t, adapter.AuthorizeTurn(context.Background()))
	require.False(t, fundingCalled)
	require.True(t, original.Group.AllowsClientProtocol(protocol.ProtocolResponsesWebSocket))
	require.Nil(t, adapter.requestKey)
}

func (r *entryKeyReader) GetByKey(context.Context, string) (*apikey.APIKey, error) {
	r.calls++
	return nil, nil
}

func (r *entryKeyReader) Reauthenticate(context.Context, *apikey.APIKey, apikey.AuthenticationInput) (*apikey.APIKey, error) {
	r.calls++
	return nil, apikey.ErrAPIKeyNotFound
}

// AcquireRequest supplies admission for access-snapshot tests.
func (r *entryKeyReader) AcquireRequest(ctx context.Context, _ *apikey.APIKey) (context.Context, func(), time.Duration, error) {
	return ctx, func() {}, 0, nil
}

// TestEntryAccessKeepsProjectionIndependentAndLazy checks lazy policy refresh and independent model and effort mappings.
func TestEntryAccessKeepsProjectionIndependentAndLazy(t *testing.T) {
	reader := &entryKeyReader{}
	key := &apikey.APIKey{ID: 9, UserID: 7, Key: "fixture-key", ModelMapping: map[string]string{"alias": "original"}, Group: &routing.Group{ReasoningEffortMappings: []routing.ReasoningEffortMapping{{}}}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(string(keyhttp.ContextKeyAPIKey), key)
	view, ok := (openAIWSHTTPBackend{bindings: Bindings{Keys: reader}}).Access(c)
	require.True(t, ok)
	require.True(t, view.RefreshFastPolicy)
	require.Zero(t, reader.calls)
	view.ModelMapping["alias"] = "changed"
	require.Equal(t, "original", key.ModelMapping["alias"])
	require.NotSame(t, &key.Group.ReasoningEffortMappings[0], &view.Group.ReasoningEffortMappings[0])
}

// TestWSTurnPricing covers model changes, zero per-request prices, and group-specific pricing.
func TestWSTurnPricing(t *testing.T) {
	zero := 0.0
	prices := &admission.ModelPricing{Resolver: testkit.ResolverWithCards(t, billing.NewCalculator(nil, billing.CalculatorOptions{}), []routing.ModelPricingEntry{{
		Models: []string{"priced-review"}, BillingMode: pricing.BillingModePerRequest, PerRequestPrice: &zero,
	}})}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	target := &openAIWSEntryTarget{
		provider: gatewayprovider.NewExecutionProvider(&provider.Record{ID: 1, Platform: "openai", Type: "apikey"}),
		root: &openAIWSEntryAdapter{c: c, key: &apikey.APIKey{GroupID: testkit.GroupID()}, bindings: Bindings{
			Pricing: prices,
			ResolveRouting: func(_ context.Context, _ *int64, _ *gatewayprovider.ExecutionProvider, model string, _ provider.OpenAIEndpointCapability) (string, error) {
				return model, nil
			},
		}},
	}
	for _, model := range []string{"priced-review", "codex-auto-review", "priced-review"} {
		plan := routing.Plan(routing.PlanInput{GroupID: testkit.GroupID(), RequestedModel: model, GroupMapping: routing.GroupMappingResult{MappedModel: model}})
		ctx := requeststate.WithRoutePlan(context.Background(), plan)
		_, err := target.ResolveRouting(ctx, model, false)
		if model == "codex-auto-review" {
			require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
			require.Equal(t, admission.ModelPricingUnavailableMessage, gatewayws.EntryLocalRoutingErrorReason(model, err))
			require.False(t, gatewayws.EntryShouldReportFailure(gatewayws.EntryLocalRoutingCause(err)))
		} else {
			require.NoError(t, err)
		}
	}
	otherGroup := int64(101)
	target.root.key.GroupID = &otherGroup
	_, err := target.ResolveRouting(context.Background(), "priced-review", false)
	require.ErrorIs(t, err, pricing.ErrModelPricingUnavailable)
}

// TestPrepareCompletionKeepsTurnIdentity catches connection context leaking into turn settlement.
func TestPrepareCompletionKeepsTurnIdentity(t *testing.T) {
	root := telemetry.WithRequestCapture(context.WithValue(context.Background(), telemetry.RequestID, "connection"), telemetry.RequestRecord{RequestID: "connection", UserID: 2, APIKeyID: 1, StartedAt: time.Now(), State: "running"}, nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil).WithContext(root)
	target := &openAIWSEntryTarget{
		root:     &openAIWSEntryAdapter{bindings: Bindings{Common: openaiattempt.Bindings{Support: &openaiattempt.Support{}}}, c: c, key: &apikey.APIKey{ID: 1, UserID: 2, User: &identity.User{ID: 2}}},
		provider: &gatewayprovider.ExecutionProvider{Record: provider.Record{ID: 3, Platform: "openai"}},
	}
	for _, id := range []string{"first-turn", "second-turn"} {
		ctx := telemetry.WithChildRequest(root, telemetry.RequestRecord{RequestID: id, State: "running", StartedAt: time.Now()})
		result := &gatewayws.ForwardResult{RequestID: "response-" + id, Model: "model", OpenAIWSMode: true}
		input := target.PrepareCompletion(ctx, result, gatewayws.TurnCapture{Turn: 1, StartedAt: time.Now()}, "model", routing.GroupMappingResult{}, nil, false)
		require.Equal(t, id, input.RequestID)
		require.Equal(t, "response-"+id, input.BillingKey)
	}
	require.Equal(t, "connection", telemetry.RequestIDValue(c.Request.Context()))
}
