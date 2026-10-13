package pricingcontract

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// WebSearchCalls selects a dedicated per-call calculation before token
// pricing. The fixture supplies its operation default through a local catalog
// snapshot, then checks configured overrides, free calls, multiplier clamping,
// and the token-pricing branch reached when the call count becomes zero.

// TestCalculateWebSearchCostDefaultAndOverride calculates search operations
// with default, overridden, and zero prices. It checks base/user amounts,
// negative-rate clamping, and the empty breakdown produced by a
// zero call count.
func TestCalculateWebSearchCostDefaultAndOverride(t *testing.T) {
	t.Parallel()
	s := alphaSearchCalculator()

	// The fixture default is $10 per 1,000 calls, or $0.01 per call.
	cost := s.CalculateWebSearchCost(1, nil, 1.0)
	// Search-operation USD totals allow absolute tolerances for
	// rate multiplication rounding.
	require.InDelta(t, 0.01, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.01, cost.ActualCost, 1e-12)
	require.Equal(t, string(routing.BillingModePerRequest), cost.BillingMode)

	// Apply the configured price and group multiplier.
	cost = s.CalculateWebSearchCost(1, testPtrFloat64(0.02), 2.5)
	// Search-operation USD totals allow absolute tolerances for
	// rate multiplication rounding.
	require.InDelta(t, 0.02, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.05, cost.ActualCost, 1e-12)

	// An explicit zero price is free; nil selects the default.
	cost = s.CalculateWebSearchCost(1, testPtrFloat64(0), 3.0)
	require.Zero(t, cost.TotalCost)
	require.Zero(t, cost.ActualCost)

	// The negative user rate selects the calculator's zero-charge clamp, while
	// the operation's positive base amount remains available for the log.
	cost = s.CalculateWebSearchCost(1, nil, -1)
	// Search-operation USD totals allow absolute tolerances for
	// rate multiplication rounding.
	require.InDelta(t, 0.01, cost.TotalCost, 1e-12)
	require.Zero(t, cost.ActualCost)

	// Zero calls produce no charge.
	cost = s.CalculateWebSearchCost(0, testPtrFloat64(0.02), 1.0)
	require.Zero(t, cost.TotalCost)
	require.Empty(t, cost.BillingMode)
}

// TestCalculateOpenAIRecordUsageCostWebSearchPerCall passes one search call
// through the completion recorder's dedicated operation branch. It checks
// base-rate charging and a snapshot override, then a missing-token-price error
// after removing the call.
func TestCalculateOpenAIRecordUsageCostWebSearchPerCall(t *testing.T) {
	t.Parallel()
	svc := completion.NewRecorder(completion.Dependencies{Calculator: alphaSearchCalculator()}, completion.RecorderOptions{DefaultMultiplier: 1})

	groupID := int64(11)

	// With no configured price, use $0.01 per call and the 2x base multiplier.
	// Search calls exclude the peak factor included in the
	// 3x token multiplier.
	apiKey := &completion.KeySnapshot{ID: 1, GroupID: &groupID, Group: &completion.GroupSnapshot{ID: groupID}}
	result := &forwardcore.OpenAIResult{Model: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol", WebSearchCalls: 1}
	cost, err := svc.CalculateOpenAIRecordUsageCostAt(context.Background(), gatewaycapture.ProjectOpenAICompletionResult(result, nil), apiKey, []string{"gpt-5.6-sol"}, 3.0, 1.0, 1.0, 2.0, pricing.UsageTokens{}, "", time.Time{})
	require.NoError(t, err)
	require.Equal(t, string(routing.BillingModePerRequest), cost.BillingMode)
	// Search-operation USD totals allow absolute tolerances for
	// rate multiplication rounding.
	require.InDelta(t, 0.01, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.02, cost.ActualCost, 1e-12)

	// A present group amount overrides the catalog operation default for this
	// per-call branch, including independently configured zero prices.
	apiKey.Group.WebSearchPricePerCall = testPtrFloat64(0.005)
	cost, err = svc.CalculateOpenAIRecordUsageCostAt(context.Background(), gatewaycapture.ProjectOpenAICompletionResult(result, nil), apiKey, []string{"gpt-5.6-sol"}, 1.0, 1.0, 1.0, 1.0, pricing.UsageTokens{}, "", time.Time{})
	require.NoError(t, err)
	// Search-operation USD totals allow absolute tolerances for
	// rate multiplication rounding.
	require.InDelta(t, 0.005, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.005, cost.ActualCost, 1e-12)

	// Zero search calls select token billing. The missing token price makes
	// that branch return a pricing error.
	result.WebSearchCalls = 0
	_, err = svc.CalculateOpenAIRecordUsageCostAt(context.Background(), gatewaycapture.ProjectOpenAICompletionResult(result, nil), apiKey, []string{"gpt-5.6-sol"}, 1.0, 1.0, 1.0, 1.0, pricing.UsageTokens{InputTokens: 10}, "", time.Time{})
	require.Error(t, err)
}

// alphaSearchCalculator constructs a calculator with a local search-operation
// default. The snapshot contains no token prices, so token billing in callers
// observes missing pricing even though the per-call operation
// can be calculated.
func alphaSearchCalculator() *billing.Calculator {
	catalog := provider.NewServiceFromSnapshot(provider.Options{}, nil, provider.Snapshot{BillingDefaults: pricing.OperationPrices{WebSearchPricePerCall: testPtrFloat64(0.01)}})
	return billing.NewCalculator(catalog, billing.CalculatorOptions{})
}
