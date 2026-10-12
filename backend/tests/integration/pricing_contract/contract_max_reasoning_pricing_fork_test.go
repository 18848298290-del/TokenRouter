package pricingcontract

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
)

// TestMaxReasoningPricing_IntervalsAndBillingModes checks interval and cache prices with group rates, and excludes reasoning multipliers from non-token modes.
func TestMaxReasoningPricing_IntervalsAndBillingModes(t *testing.T) {
	bs := newCalculator(nil)
	resolver := billingtestkit.PriceResolver(nil, bs)
	for _, factor := range []float64{1, 1.5, 3} {
		resolved := &pricing.ResolvedPricing{
			Mode:        routing.BillingModeToken,
			BasePricing: &pricing.ModelPricing{MaxReasoningEffortMultiplier: &factor},
			Intervals: []routing.PricingInterval{{
				MinTokens: 0, InputPrice: testPtrFloat64(0.01), OutputPrice: testPtrFloat64(0.02),
				CacheWritePrice: testPtrFloat64(0.03), CacheReadPrice: testPtrFloat64(0.001),
			}},
		}
		input := billing.CostInput{
			Ctx: context.Background(), Model: "claude-fable-5-1", RateMultiplier: 2,
			Tokens:   pricing.UsageTokens{InputTokens: 100, OutputTokens: 20, CacheCreationTokens: 10, CacheReadTokens: 50},
			Resolver: resolver, Resolved: resolved, ReasoningEffort: "max",
		}
		maxCost, err := bs.CalculateCostUnified(input)
		require.NoError(t, err)
		input.ReasoningEffort = "xhigh"
		standard, err := bs.CalculateCostUnified(input)
		require.NoError(t, err)
		require.InDelta(t, 1.75, standard.TotalCost, 1e-12)
		require.InDelta(t, 1.75*factor, maxCost.TotalCost, 1e-12)
		require.InDelta(t, 3.5*factor, maxCost.ActualCost, 1e-12)
		require.InDelta(t, 0.3*factor, maxCost.CacheCreationCost, 1e-12)
		require.InDelta(t, 0.05*factor, maxCost.CacheReadCost, 1e-12)
	}
	for _, mode := range []routing.BillingMode{routing.BillingModePerRequest, routing.BillingModeImage, routing.BillingModeVideo} {
		cost, err := bs.CalculateCostUnified(billing.CostInput{
			Model: "claude-fable-5-1", ReasoningEffort: "max",
			RequestCount: 2, RateMultiplier: 2, Resolver: resolver,
			Resolved: &pricing.ResolvedPricing{Mode: mode, DefaultPerRequestPrice: 0.1},
		})
		require.NoError(t, err)
		require.InDelta(t, 0.2, cost.TotalCost, 1e-12)
	}
}

// TestMaxReasoningPricing_ProviderStatsPriority checks custom provider costs first, then catalog costs at the final reasoning effort.
func TestMaxReasoningPricing_ProviderStatsPriority(t *testing.T) {
	bs := newCalculator(nil)
	pricingConfig := &routingtestkit.Configuration{ID: 1, Status: billing.StatusActive, ProviderStatsPricingRules: []routing.ProviderStatsPricingRule{{
		GroupIDs: []int64{10}, Pricing: []routing.ModelPricingEntry{{Models: []string{"claude-fable-5-1"}, InputPrice: testPtrFloat64(0.01)}},
	}}}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformAnthropic)
	tokens := pricing.UsageTokens{InputTokens: 100}
	cost := contractProviderStatsCost(context.Background(), cs, bs, "", 1, 10, "claude-fable-5-1", "", tokens, 1, 9, "priority", "max")
	require.NotNil(t, cost)
	require.InDelta(t, 1, *cost, 1e-12)
	pricingConfig.ProviderStatsPricingRules = nil
	cs = newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformAnthropic)
	standard := contractProviderStatsCost(context.Background(), cs, bs, "", 1, 10, "claude-fable-5-1", "", tokens, 1, 9, "", "xhigh")
	cost = contractProviderStatsCost(context.Background(), cs, bs, "", 1, 10, "claude-fable-5-1", "", tokens, 1, 9, "", "max")
	require.NotNil(t, standard)
	require.NotNil(t, cost)
	require.InDelta(t, *standard*3, *cost, 1e-12)
}

// TestMaxReasoningPricing_OpenAIUsageUsesFinalEffort checks that billing uses the final effort while the requested effort remains audit data.
func TestMaxReasoningPricing_OpenAIUsageUsesFinalEffort(t *testing.T) {
	bs := newCalculator(nil)
	for _, resolver := range []*billing.PriceResolver{nil, billingtestkit.PriceResolver(nil, bs)} {
		svc := completion.NewRecorder(completion.Dependencies{Calculator: bs, Prices: resolver}, completion.RecorderOptions{DefaultMultiplier: 1})

		requested, final := "max", "xhigh"
		result := &forwardcore.OpenAIResult{ReasoningEffort: &final, RequestedReasoningEffort: &requested}
		key := &apikey.APIKey{Group: &routing.Group{ID: 1}}
		tokens := pricing.UsageTokens{InputTokens: 1000}
		standard, err := svc.CalculateOpenAIRecordUsageCostAt(context.Background(), gatewaycapture.ProjectOpenAICompletionResult(result, nil), gatewaycapture.ProjectCompletionKey(key), []string{"claude-fable-5-1"}, 2, 1, 1, 1, tokens, "", time.Time{})
		require.NoError(t, err)
		final = "max"
		cost, err := svc.CalculateOpenAIRecordUsageCostAt(context.Background(), gatewaycapture.ProjectOpenAICompletionResult(result, nil), gatewaycapture.ProjectCompletionKey(key), []string{"claude-fable-5-1"}, 2, 1, 1, 1, tokens, "", time.Time{})
		require.NoError(t, err)
		require.InDelta(t, standard.TotalCost*3, cost.TotalCost, 1e-12)
		require.InDelta(t, standard.ActualCost*3, cost.ActualCost, 1e-12)
	}
}

func TestMaxReasoningPricing_RejectsInvalidMultipliers(t *testing.T) {
	for _, factor := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		require.Error(t, routing.CheckBillingModeRequirements(routing.ModelPricingEntry{BillingMode: routing.BillingModeToken, MaxReasoningEffortMultiplier: &factor}))
	}
}
