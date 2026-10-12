package pricingcontract

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type openAI403CounterResetStub struct {
	resetCalls []int64
}

// forbiddenResetFixture forwards completion counter resets to the provider health service.
type forbiddenResetFixture struct{ core *providercore.HealthService }

func (s *openAI403CounterResetStub) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	return 0, nil
}

func (s *openAI403CounterResetStub) ResetOpenAI403Count(_ context.Context, providerID int64) error {
	s.resetCalls = append(s.resetCalls, providerID)
	return nil
}

func TestOpenAIGatewayServiceRecordUsageResets403CounterForZeroUsage(t *testing.T) {
	for _, platform := range []string{capability.PlatformOpenAI, capability.PlatformKimi, capability.PlatformZhipu, capability.PlatformDeepseek} {
		t.Run(platform, func(t *testing.T) {
			counter := &openAI403CounterResetStub{}
			rateLimitSvc := gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Options: providercore.HealthOptions{ForbiddenCounter: counter}})

			usageRepo := &gatewaytestkit.UsageLogStore{Inserted: true}
			billingRepo := &gatewaytestkit.SettlementStore{Result: &billing.UsageBillingApplyResult{Applied: true}}
			userRepo := &gatewaytestkit.UserStore{}
			subRepo := &gatewaytestkit.SubscriptionStore{}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)
			svc.Dependencies.Health = forbiddenResetFixture{rateLimitSvc.Core}

			err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
				Result: &forwardcore.OpenAIResult{
					RequestID: "resp_zero_usage_reset_403_" + platform,
					Model:     "gpt-5.1",
				},
				APIKey:   &apikey.APIKey{ID: 1001, Group: &routing.Group{RateMultiplier: 1}},
				User:     &identity.User{ID: 2001},
				Provider: gatewaycapture.ExecutionCompletionRecord(&gatewaycapture.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 777, Platform: platform}}),
			})

			require.NoError(t, err)
			require.Equal(t, []int64{777}, counter.resetCalls)
			require.Equal(t, 1, usageRepo.Calls)
		})
	}
}

func (f forbiddenResetFixture) ResetOpenAI403Counter(ctx context.Context, id int64) {
	f.core.ResetForbiddenCounter(ctx, id)
}
