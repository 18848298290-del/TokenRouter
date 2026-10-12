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

func TestCalculateWebSearchCostDefaultAndOverride(t *testing.T) {
	t.Parallel()
	s := alphaSearchCalculator()

	// The fixture default is $10 per 1,000 calls, or $0.01 per call.
	cost := s.CalculateWebSearchCost(1, nil, 1.0)
	require.InDelta(t, 0.01, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.01, cost.ActualCost, 1e-12)
	require.Equal(t, string(routing.BillingModePerRequest), cost.BillingMode)

	// Apply the configured price and group multiplier.
	cost = s.CalculateWebSearchCost(1, testPtrFloat64(0.02), 2.5)
	require.InDelta(t, 0.02, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.05, cost.ActualCost, 1e-12)

	// An explicit zero price is free; nil selects the default.
	cost = s.CalculateWebSearchCost(1, testPtrFloat64(0), 3.0)
	require.Zero(t, cost.TotalCost)
	require.Zero(t, cost.ActualCost)

	// A negative multiplier clamps the user charge to zero.
	cost = s.CalculateWebSearchCost(1, nil, -1)
	require.InDelta(t, 0.01, cost.TotalCost, 1e-12)
	require.Zero(t, cost.ActualCost)

	// Zero calls produce no charge.
	cost = s.CalculateWebSearchCost(0, testPtrFloat64(0.02), 1.0)
	require.Zero(t, cost.TotalCost)
	require.Empty(t, cost.BillingMode)
}

func TestCalculateOpenAIRecordUsageCostWebSearchPerCall(t *testing.T) {
	t.Parallel()
	svc := completion.NewRecorder(completion.Dependencies{Calculator: alphaSearchCalculator()}, completion.RecorderOptions{DefaultMultiplier: 1})

	groupID := int64(11)

	// With no configured price, use $0.01 per call and the 2x base multiplier.
	// Search calls exclude the peak factor included in the 3x token multiplier.
	apiKey := &completion.KeySnapshot{ID: 1, GroupID: &groupID, Group: &completion.GroupSnapshot{ID: groupID}}
	result := &forwardcore.OpenAIResult{Model: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol", WebSearchCalls: 1}
	cost, err := svc.CalculateOpenAIRecordUsageCostAt(context.Background(), gatewaycapture.ProjectOpenAICompletionResult(result, nil), apiKey, []string{"gpt-5.6-sol"}, 3.0, 1.0, 1.0, 2.0, pricing.UsageTokens{}, "", time.Time{})
	require.NoError(t, err)
	require.Equal(t, string(routing.BillingModePerRequest), cost.BillingMode)
	require.InDelta(t, 0.01, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.02, cost.ActualCost, 1e-12)

	// Use the group snapshot's $0.005 per-call price.
	apiKey.Group.WebSearchPricePerCall = testPtrFloat64(0.005)
	cost, err = svc.CalculateOpenAIRecordUsageCostAt(context.Background(), gatewaycapture.ProjectOpenAICompletionResult(result, nil), apiKey, []string{"gpt-5.6-sol"}, 1.0, 1.0, 1.0, 1.0, pricing.UsageTokens{}, "", time.Time{})
	require.NoError(t, err)
	require.InDelta(t, 0.005, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.005, cost.ActualCost, 1e-12)

	// Zero search calls select token billing.
	// The missing token price makes that branch return a pricing error.
	result.WebSearchCalls = 0
	_, err = svc.CalculateOpenAIRecordUsageCostAt(context.Background(), gatewaycapture.ProjectOpenAICompletionResult(result, nil), apiKey, []string{"gpt-5.6-sol"}, 1.0, 1.0, 1.0, 1.0, pricing.UsageTokens{InputTokens: 10}, "", time.Time{})
	require.Error(t, err)
}

// alphaSearchCalculator supplies a search operation price and an empty token catalog.
func alphaSearchCalculator() *billing.Calculator {
	catalog := provider.NewServiceFromSnapshot(provider.Options{}, nil, provider.Snapshot{BillingDefaults: pricing.OperationPrices{WebSearchPricePerCall: testPtrFloat64(0.01)}})
	return billing.NewCalculator(catalog, billing.CalculatorOptions{})
}
