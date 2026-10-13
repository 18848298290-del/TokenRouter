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

// A completed OpenAI-compatible request can report zero tokens and still
// provide successful provider-health evidence. Four provider platforms use the
// recorder fixture to check one forbidden-counter reset and one usage-log call
// after that completion is recorded.

// openAI403CounterResetStub records provider identifiers reset by
// the health fixture.
type openAI403CounterResetStub struct {
	// resetCalls accumulates provider IDs during recorder completion; each
	// subtest owns a fresh stub and inspects this slice
	// after recording returns.
	resetCalls []int64 // observed provider reset identifiers
}

// forbiddenResetFixture connects recorder counter resets to the
// provider health service.
type forbiddenResetFixture struct {
	// core receives completion reset calls through ResetForbiddenCounter. Its
	// nil-safe method leaves counter state untouched when no health
	// service is supplied.
	core *providercore.HealthService // provider health service
}

// IncrementOpenAI403Count returns zero with no error for every increment
// request. Provider identifiers, window arguments, and cancellation have no
// effect on this stub result; the fixture records
// reset observations separately.
func (s *openAI403CounterResetStub) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	return 0, nil
}

// ResetOpenAI403Count appends providerID to the stub's reset observations and
// reports success. Callers supply a nonnil stub, and assertions read those
// observations after the synchronous recorder call returns.
func (s *openAI403CounterResetStub) ResetOpenAI403Count(_ context.Context, providerID int64) error {
	s.resetCalls = append(s.resetCalls, providerID)
	return nil
}

// TestOpenAIGatewayServiceRecordUsageResets403CounterForZeroUsage records
// zero-token results on four OpenAI-compatible provider platforms. It checks a
// successful return, one provider-counter reset, and one usage-log insertion
// call for each fixture.
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

// ResetOpenAI403Counter asks the provider health service to clear one
// provider's forbidden counter. Nil services, absent counters, and nonpositive
// identifiers produce no reset; deletion failures are observed by the service.
func (f forbiddenResetFixture) ResetOpenAI403Counter(ctx context.Context, id int64) {
	f.core.ResetForbiddenCounter(ctx, id)
}
