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

const (
	// Expected USD cost for one million input tokens at the configured price.
	configPricingExpectedPricingConfigCost = 0.4
	// Distinct price used to detect an unrelated card match.
	configPricingUnrelatedCost = 0.9
)

// tokenPricingForModels creates a token price card; inputPerMillion is USD per million tokens.
func tokenPricingForModels(models []string, inputPerMillion float64) routing.ModelPricingEntry {
	return routing.ModelPricingEntry{
		Models:          models,
		BillingMode:     routing.BillingModeToken,
		InputPrice:      new(float64(inputPerMillion / 1e6)),
		OutputPrice:     new(float64(2.4e-6)),
		CacheWritePrice: new(float64(0.5e-6)),
		CacheReadPrice:  new(float64(0.04e-6)),
	}
}

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

// recordUsageWithConfigPricing records usage with the supplied cards and returns the log captured by the test store.
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

// TestConfigPricing_ExactModelMatch checks that an exact card match supplies the input price.
func TestConfigPricing_ExactModelMatch(t *testing.T) {
	log := recordUsageWithConfigPricing(t, "gpt-5.6-luna", []routing.ModelPricingEntry{
		tokenPricingForModels([]string{"gpt-5.6-luna"}, configPricingExpectedPricingConfigCost),
	})
	require.InDelta(t, configPricingExpectedPricingConfigCost, log.InputCost, 1e-9)
}

// TestConfigPricing_SuffixedModelUsesNormalizedConfigPricing checks that suffixed models cannot borrow the base model's card.
func TestConfigPricing_SuffixedModelUsesNormalizedConfigPricing(t *testing.T) {
	log := recordUsageWithConfigPricing(t, "gpt-5.6-luna-high", []routing.ModelPricingEntry{
		tokenPricingForModels([]string{"gpt-5.6-luna"}, configPricingExpectedPricingConfigCost),
	})
	require.Zero(t, log.InputCost, "缺价不能借用基础型号或无关价卡")
}

// TestConfigPricing_DateSuffixedModelUsesNormalizedConfigPricing checks that dated variants need their own price.
func TestConfigPricing_DateSuffixedModelUsesNormalizedConfigPricing(t *testing.T) {
	log := recordUsageWithConfigPricing(t, "gpt-5.6-luna-2026-08-01", []routing.ModelPricingEntry{
		tokenPricingForModels([]string{"gpt-5.6-luna"}, configPricingExpectedPricingConfigCost),
	})
	require.Zero(t, log.InputCost, "缺价不能借用基础型号或无关价卡")
}

// TestConfigPricing_ExactVariantWinsOverNormalizedBaseName checks that the full model name selects its own card.
func TestConfigPricing_ExactVariantWinsOverNormalizedBaseName(t *testing.T) {
	log := recordUsageWithConfigPricing(t, "gpt-5.6-luna-high", []routing.ModelPricingEntry{
		tokenPricingForModels([]string{"gpt-5.6-luna-high"}, configPricingUnrelatedCost),
		tokenPricingForModels([]string{"gpt-5.6-luna"}, configPricingExpectedPricingConfigCost),
	})
	require.InDelta(t, configPricingUnrelatedCost, log.InputCost, 1e-9,
		"explicit per-variant channel pricing must win over the normalized base name")
}

// TestConfigPricing_UnrelatedPricingConfigModelNotMatched checks that an unrelated card cannot price an unknown model.
func TestConfigPricing_UnrelatedPricingConfigModelNotMatched(t *testing.T) {
	log := recordUsageWithConfigPricing(t, "gpt-5.6-luna-high", []routing.ModelPricingEntry{
		tokenPricingForModels([]string{"gpt-5.4"}, configPricingUnrelatedCost),
	})
	require.Zero(t, log.InputCost, "缺价不能借用基础型号或无关价卡")
}
