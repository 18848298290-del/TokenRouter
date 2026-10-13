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

// Token calculations use the final forwarded effort when applying a configured
// Max multiplier. The fixtures separate interval token amounts, independent
// provider-cost cards, and recorder effort evidence so requested audit
// metadata cannot become the price selector after a downgrade.

// TestMaxReasoningPricing_IntervalsAndBillingModes compares max and xhigh for
// interval token amounts and cache buckets. It checks group-rate scaling and
// the unmodified operation totals of the supplied non-token resolved fixtures.
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
		// The four standard token buckets sum to $1.75. Max scales those
		// buckets before the user's separate twofold rate, with dollar
		// tolerances allowing arithmetic rounding.
		require.InDelta(t, 1.75, standard.TotalCost, 1e-12)
		require.InDelta(t, 1.75*factor, maxCost.TotalCost, 1e-12)
		require.InDelta(t, 3.5*factor, maxCost.ActualCost, 1e-12)
		require.InDelta(t, 0.3*factor, maxCost.CacheCreationCost, 1e-12)
		require.InDelta(t, 0.05*factor, maxCost.CacheReadCost, 1e-12)
	}
	// These resolved operation fixtures carry a default amount and no
	// BasePricing record. Their totals check mode dispatch with the supplied
	// max effort present.
	for _, mode := range []routing.BillingMode{routing.BillingModePerRequest, routing.BillingModeImage, routing.BillingModeVideo} {
		cost, err := bs.CalculateCostUnified(billing.CostInput{
			Model: "claude-fable-5-1", ReasoningEffort: "max",
			RequestCount: 2, RateMultiplier: 2, Resolver: resolver,
			Resolved: &pricing.ResolvedPricing{Mode: mode, DefaultPerRequestPrice: 0.1},
		})
		require.NoError(t, err)
		// Operation USD totals allow a small absolute rounding difference.
		require.InDelta(t, 0.2, cost.TotalCost, 1e-12)
	}
}

// TestMaxReasoningPricing_ProviderStatsPriority supplies an independent
// provider-cost card before removing it. It checks the custom amount first,
// then the historical catalog's threefold max-effort amount relative to xhigh.
func TestMaxReasoningPricing_ProviderStatsPriority(t *testing.T) {
	bs := newCalculator(nil)
	pricingConfig := &routingtestkit.Configuration{ID: 1, Status: billing.StatusActive, ProviderStatsPricingRules: []routing.ProviderStatsPricingRule{{
		GroupIDs: []int64{10}, Pricing: []routing.ModelPricingEntry{{Models: []string{"claude-fable-5-1"}, InputPrice: testPtrFloat64(0.01)}},
	}}}
	cs := newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformAnthropic)
	tokens := pricing.UsageTokens{InputTokens: 100}
	cost := contractProviderStatsCost(context.Background(), cs, bs, "", 1, 10, "claude-fable-5-1", "", tokens, 1, 9, "priority", "max")
	require.NotNil(t, cost)
	// Provider-cost USD comparisons allow rounding in token-rate products.
	require.InDelta(t, 1, *cost, 1e-12)
	pricingConfig.ProviderStatsPricingRules = nil
	cs = newTestPricingConfigServiceForStats(t, pricingConfig, 10, capability.PlatformAnthropic)
	standard := contractProviderStatsCost(context.Background(), cs, bs, "", 1, 10, "claude-fable-5-1", "", tokens, 1, 9, "", "xhigh")
	cost = contractProviderStatsCost(context.Background(), cs, bs, "", 1, 10, "claude-fable-5-1", "", tokens, 1, 9, "", "max")
	require.NotNil(t, standard)
	require.NotNil(t, cost)
	// Provider-cost USD comparisons allow rounding in token-rate products.
	require.InDelta(t, *standard*3, *cost, 1e-12)
}

// TestMaxReasoningPricing_OpenAIUsageUsesFinalEffort keeps requested effort at
// max while changing the forwarded effort. It checks threefold cost scaling
// after forwarding max, with both catalog-only and
// resolver-backed recorder dependencies.
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
		// The effort-scaled USD totals allow an absolute rounding difference.
		require.InDelta(t, standard.TotalCost*3, cost.TotalCost, 1e-12)
		require.InDelta(t, standard.ActualCost*3, cost.ActualCost, 1e-12)
	}
}

// TestMaxReasoningPricing_RejectsInvalidMultipliers supplies zero, negative,
// NaN, and infinite Max factors to card validation. It checks that each
// invalid token-pricing multiplier produces an error.
func TestMaxReasoningPricing_RejectsInvalidMultipliers(t *testing.T) {
	for _, factor := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		require.Error(t, routing.CheckBillingModeRequirements(routing.ModelPricingEntry{BillingMode: routing.BillingModeToken, MaxReasoningEffortMultiplier: &factor}))
	}
}
