package pricingcontract

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewaycapture "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// A configured model card is matched against the complete requested identity
// when recording token usage. The fixed million-token fixture distinguishes
// exact variants from base names, dated suffixes, and unrelated cards by
// inspecting the input-cost bucket saved in the usage log.

// The configured and unrelated dollar amounts distinguish exact card selection
// when the recording fixture prices one million input tokens for
// a requested model.
const (
	// Expected USD cost for one million input tokens at the configured price.
	configPricingExpectedPricingConfigCost = 0.4
	// Distinct price used to detect an unrelated card match.
	configPricingUnrelatedCost = 0.9
)

// tokenPricingForModels creates a token card for the supplied model patterns.
// inputPerMillion is a dollar amount per million tokens and is converted to a
// per-token price alongside fixed output and cache fixture rates.
func tokenPricingForModels(models []string, inputPerMillion float64) routing.ModelPricingEntry {
	// Only the input argument uses a per-million unit; the fixed output and
	// cache amounts below already use dollars per token.
	return routing.ModelPricingEntry{
		Models:          models,
		BillingMode:     routing.BillingModeToken,
		InputPrice:      new(float64(inputPerMillion / 1e6)),
		OutputPrice:     new(float64(2.4e-6)),
		CacheWritePrice: new(float64(0.5e-6)),
		CacheReadPrice:  new(float64(0.04e-6)),
	}
}

// newPricingConfigServiceWithPricings constructs one active pricing
// configuration associated with groupID. The supplied cards are compiled
// through the routing testkit, with OpenAI platform metadata available to
// the configuration fixture.
func newPricingConfigServiceWithPricings(groupID int64, pricings []routing.ModelPricingEntry) *routing.PricingConfigService {
	ch := routingtestkit.Configuration{
		ID:           1,
		Name:         "codex-channel",
		Status:       billing.StatusActive,
		ModelPricing: pricings,
		GroupIDs:     []int64{groupID},
	}

	cs := routingtestkit.ModelConfigFromData(routingtestkit.ModelConfigDataFromRows([]routingtestkit.Configuration{ch}, map[int64]string{groupID: capability.PlatformOpenAI}))
	return cs
}

// recordUsageWithConfigPricing records one million input tokens using the
// supplied full requested model name and cards. It fails the test on recording
// errors or a missing captured log, then returns the stored row.
func recordUsageWithConfigPricing(t *testing.T, requestedModel string, pricings []routing.ModelPricingEntry) *usage.UsageLog {
	t.Helper()
	const groupID = int64(777)

	usageRepo := &gatewaytestkit.UsageLogStore{Inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &gatewaytestkit.UserStore{}, &gatewaytestkit.SubscriptionStore{}, nil)
	cs := newPricingConfigServiceWithPricings(groupID, pricings)
	svc.GroupPolicies = cs
	svc.Dependencies.Prices = billingtestkit.PriceResolver(cs, svc.Dependencies.Calculator)

	group := &routing.Group{
		ID: groupID,

		RateMultiplier: 1,
	}
	err := svc.RecordOpenAI(context.Background(), &gatewaycapture.OpenAICapture{
		Result: &forwardcore.OpenAIResult{
			RequestID:    "resp_luna_5256",
			Model:        requestedModel,
			BillingModel: requestedModel,
			Usage: openai.ForwardUsage{
				InputTokens:  1_000_000,
				OutputTokens: 0,
			},
			Duration: time.Second,
		},
		PricingUsageFields: routing.PricingUsageFields{
			OriginalModel:    requestedModel,
			GroupMappedModel: requestedModel,
		},
		APIKey: &apikey.APIKey{
			ID:      1,
			GroupID: new(groupID),
			Group:   group,
		},
		User:     &identity.User{ID: 1},
		Provider: gatewaycapture.ExecutionCompletionRecord(&gatewaycapture.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI}}),
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.LastLog)
	return usageRepo.LastLog
}

// TestConfigPricing_ExactModelMatch records a model whose complete name
// appears on the configured card. It checks that one million input tokens
// receive the expected configured dollar amount.
func TestConfigPricing_ExactModelMatch(t *testing.T) {
	log := recordUsageWithConfigPricing(t, "gpt-5.6-luna", []routing.ModelPricingEntry{
		tokenPricingForModels([]string{"gpt-5.6-luna"}, configPricingExpectedPricingConfigCost),
	})
	// The million-token USD charge allows a small
	// absolute rounding difference.
	require.InDelta(t, configPricingExpectedPricingConfigCost, log.InputCost, 1e-9)
}

// TestConfigPricing_SuffixedModelUsesNormalizedConfigPricing records a
// high-suffixed model while configuring its base name. It checks a zero
// input-cost bucket for the complete unpriced variant.
func TestConfigPricing_SuffixedModelUsesNormalizedConfigPricing(t *testing.T) {
	log := recordUsageWithConfigPricing(t, "gpt-5.6-luna-high", []routing.ModelPricingEntry{
		tokenPricingForModels([]string{"gpt-5.6-luna"}, configPricingExpectedPricingConfigCost),
	})
	require.Zero(t, log.InputCost, "缺价不能借用基础型号或无关价卡")
}

// TestConfigPricing_DateSuffixedModelUsesNormalizedConfigPricing records a
// dated model variant while configuring the undated base name. It checks a
// zero input-cost bucket for that unpriced complete identity.
func TestConfigPricing_DateSuffixedModelUsesNormalizedConfigPricing(t *testing.T) {
	log := recordUsageWithConfigPricing(t, "gpt-5.6-luna-2026-08-01", []routing.ModelPricingEntry{
		tokenPricingForModels([]string{"gpt-5.6-luna"}, configPricingExpectedPricingConfigCost),
	})
	require.Zero(t, log.InputCost, "缺价不能借用基础型号或无关价卡")
}

// TestConfigPricing_ExactVariantWinsOverNormalizedBaseName gives the full
// high-suffixed variant and base model distinct configured prices. It checks
// that recorded input cost uses the complete variant's amount.
func TestConfigPricing_ExactVariantWinsOverNormalizedBaseName(t *testing.T) {
	log := recordUsageWithConfigPricing(t, "gpt-5.6-luna-high", []routing.ModelPricingEntry{
		tokenPricingForModels([]string{"gpt-5.6-luna-high"}, configPricingUnrelatedCost),
		tokenPricingForModels([]string{"gpt-5.6-luna"}, configPricingExpectedPricingConfigCost),
	})
	// The million-token USD charge allows a small
	// absolute rounding difference.
	require.InDelta(t, configPricingUnrelatedCost, log.InputCost, 1e-9,
		"explicit per-variant channel pricing must win over the normalized base name")
}

// TestConfigPricing_UnrelatedPricingConfigModelNotMatched records an unknown
// complete model while configuring a different model's card. It checks that
// the saved input-cost bucket remains zero for the unresolved price.
func TestConfigPricing_UnrelatedPricingConfigModelNotMatched(t *testing.T) {
	log := recordUsageWithConfigPricing(t, "gpt-5.6-luna-high", []routing.ModelPricingEntry{
		tokenPricingForModels([]string{"gpt-5.4"}, configPricingUnrelatedCost),
	})
	require.Zero(t, log.InputCost, "缺价不能借用基础型号或无关价卡")
}
