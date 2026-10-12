package completion_test

import (
	"context"
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
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
)

// TestOpenAIUsageBillingModelPreservesImagePricingModel checks that the upstream text model cannot replace an image billing model.
func TestOpenAIUsageBillingModelPreservesImagePricingModel(t *testing.T) {
	tests := []struct {
		name   string
		result forwardcore.OpenAIResult
		fields routing.PricingUsageFields
		want   string
	}{
		{
			name: "上游计费保留图片模型",
			result: forwardcore.OpenAIResult{
				Model:         "gpt-5.6-sol",
				UpstreamModel: "gpt-5.6-sol",
				BillingModel:  "gpt-image-2",
				ImageCount:    1,
			},
			fields: routing.PricingUsageFields{BillingModelSource: routing.BillingModelSourceUpstream},
			want:   "gpt-image-2",
		},
		{
			name: "普通上游计费使用最终模型",
			result: forwardcore.OpenAIResult{
				Model:         "public-alias",
				UpstreamModel: "gpt-5.6-sol",
				BillingModel:  "group-model",
			},
			fields: routing.PricingUsageFields{BillingModelSource: routing.BillingModelSourceUpstream},
			want:   "gpt-5.6-sol",
		},
		{
			name: "分组未映射时的计费保留图片模型",
			result: forwardcore.OpenAIResult{
				Model:         "gpt-5.6-sol",
				UpstreamModel: "gpt-5.6-sol",
				BillingModel:  "gpt-image-2",
				ImageCount:    1,
			},
			fields: routing.PricingUsageFields{
				BillingModelSource: routing.BillingModelSourceGroupMapped,
				OriginalModel:      "gpt-5.6-sol",
				GroupMappedModel:   "gpt-5.6-sol",
			},
			want: "gpt-image-2",
		},
		{
			name: "请求模型来源覆盖图片模型",
			result: forwardcore.OpenAIResult{
				BillingModel: "gpt-image-2",
				ImageCount:   1,
			},
			fields: routing.PricingUsageFields{
				BillingModelSource: routing.BillingModelSourceRequested,
				OriginalModel:      "public-image-alias",
			},
			want: "public-image-alias",
		},
		{
			name: "分组映射计费来源覆盖图片模型",
			result: forwardcore.OpenAIResult{
				BillingModel: "gpt-image-2",
				ImageCount:   1,
			},
			fields: routing.PricingUsageFields{
				BillingModelSource: routing.BillingModelSourceGroupMapped,
				OriginalModel:      "public-image-alias",
				GroupMappedModel:   "priced-group-model",
			},
			want: "priced-group-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, completion.OpenAIUsageBillingModel(gatewaycapture.ProjectOpenAICompletionResult(&tt.result, nil), tt.fields))
		})
	}
}

func TestGatewayServiceCalculateRecordUsageCost_PricingConfigImageBillingUsesImageCount(t *testing.T) {
	groupID := int64(126)
	billingService := NewBillingService(nil)
	svc := completion.NewRecorder(completion.Dependencies{
		Calculator: billingService,
		Prices:     newOpenAIImageConfigPricingResolverForTest(t, groupID, "gemini-image", 0.25),
	}, completion.RecorderOptions{DefaultMultiplier: 1})

	cost := svc.CalculateRecordUsageCost(
		context.Background(), gatewaycapture.ProjectMessagesCompletionResult(&forwardcore.MessagesResult{Model: "gemini-image", ImageCount: 2, ImageSize: "1K"},

			nil), gatewaycapture.ProjectCompletionKey(&apikey.APIKey{GroupID: i64p(groupID), Group: &routing.Group{ID: groupID}}), gatewaycapture.ProjectCompletionProvider(nil), "gemini-image",
		"gemini-image",
		"",
		"",
		0.15,
		1.0,
		nil,
	)

	require.NotNil(t, cost)
	require.Equal(t, string(routing.BillingModeImage), cost.BillingMode)
	require.InDelta(t, 0.5, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.5, cost.ActualCost, 1e-12)
}

func TestGatewayServiceCalculateRecordUsageCost_PricingConfigImageBillingUsesSizeTier(t *testing.T) {
	groupID := int64(127)
	defaultPrice := 0.10
	price4K := 0.40
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Model: "gemini-image"}] = &routing.ModelPricingEntry{
		BillingMode:     routing.BillingModeImage,
		PerRequestPrice: &defaultPrice,
		Intervals: []routing.PricingInterval{{
			TierLabel:       "4K",
			PerRequestPrice: &price4K,
		}},
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	svc := completion.NewRecorder(completion.Dependencies{
		Calculator: NewBillingService(nil),
		Prices:     billingtestkit.PriceResolver(pricingConfigService, NewBillingService(nil)),
	}, completion.RecorderOptions{DefaultMultiplier: 1})

	cost := svc.CalculateRecordUsageCost(
		context.Background(), gatewaycapture.ProjectMessagesCompletionResult(&forwardcore.MessagesResult{Model: "gemini-image", ImageCount: 2, ImageSize: "4K"},

			nil), gatewaycapture.ProjectCompletionKey(&apikey.APIKey{GroupID: i64p(groupID), Group: &routing.Group{ID: groupID}}), gatewaycapture.ProjectCompletionProvider(nil), "gemini-image",
		"gemini-image",
		"",
		"",
		1.0,
		1.0,
		nil,
	)

	require.NotNil(t, cost)
	require.Equal(t, string(routing.BillingModeImage), cost.BillingMode)
	require.InDelta(t, 0.80, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.80, cost.ActualCost, 1e-12)
}

func TestGatewayServiceCalculateRecordUsageCost_UsesSharedImagePrice(t *testing.T) {
	groupID := int64(129)
	pricingConfigPrice := 0.25

	svc := completion.NewRecorder(completion.Dependencies{
		Calculator: NewBillingService(nil),
		Prices:     newOpenAIImageConfigPricingResolverForTest(t, groupID, "gemini-image", pricingConfigPrice),
	}, completion.RecorderOptions{DefaultMultiplier: 1})

	cost := svc.CalculateRecordUsageCost(
		context.Background(), gatewaycapture.ProjectMessagesCompletionResult(&forwardcore.MessagesResult{Model: "gemini-image", ImageCount: 2, ImageSize: pricing.ImageBillingSize2K},

			nil), gatewaycapture.ProjectCompletionKey(&apikey.APIKey{
			GroupID: i64p(groupID),
			Group:   &routing.Group{ID: groupID},
		}), gatewaycapture.ProjectCompletionProvider(nil), "gemini-image",
		"gemini-image",
		"",
		"",
		1.0,
		1.0,
		nil,
	)

	require.NotNil(t, cost)
	require.Equal(t, string(routing.BillingModeImage), cost.BillingMode)
	require.InDelta(t, 0.50, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.50, cost.ActualCost, 1e-12)
}

func TestGatewayServiceCalculateRecordUsageCost_PricingConfigImageBillingNormalizesMissingSizeTier(t *testing.T) {
	groupID := int64(128)
	defaultPrice := 0.10
	price2K := 0.22
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Model: "gemini-image"}] = &routing.ModelPricingEntry{
		BillingMode:     routing.BillingModeImage,
		PerRequestPrice: &defaultPrice,
		Intervals: []routing.PricingInterval{{
			TierLabel:       "2K",
			PerRequestPrice: &price2K,
		}},
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.LoadedAt = time.Now()

	pricingConfigService := routingtestkit.ModelConfigFromData(cache)

	svc := completion.NewRecorder(completion.Dependencies{
		Calculator: NewBillingService(nil),
		Prices:     billingtestkit.PriceResolver(pricingConfigService, NewBillingService(nil)),
	}, completion.RecorderOptions{DefaultMultiplier: 1})

	cost := svc.CalculateRecordUsageCost(
		context.Background(), gatewaycapture.ProjectMessagesCompletionResult(&forwardcore.MessagesResult{Model: "gemini-image", ImageCount: 2, ImageSize: ""},

			nil), gatewaycapture.ProjectCompletionKey(&apikey.APIKey{GroupID: i64p(groupID), Group: &routing.Group{ID: groupID}}), gatewaycapture.ProjectCompletionProvider(nil), "gemini-image",
		"gemini-image",
		"",
		"",
		1.0,
		1.0,
		nil,
	)

	require.NotNil(t, cost)
	require.Equal(t, string(routing.BillingModeImage), cost.BillingMode)
	require.InDelta(t, 0.44, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.44, cost.ActualCost, 1e-12)
}

// TestGroupBillsOpenAIFastAtStandardRequiresOpenAIProvider checks platform, provider, and tier eligibility for standard-priced Fast.
func TestGroupBillsOpenAIFastAtStandardRequiresOpenAIProvider(t *testing.T) {
	apiKey := &completion.KeySnapshot{Group: &completion.GroupSnapshot{FreeOpenAIFast: true, SupportsOpenAIFast: true}}

	require.True(t, completion.GroupBillsOpenAIFastAtStandard(apiKey, gatewaycapture.ProjectCompletionProvider(&providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}), "priority"))
	require.True(t, completion.GroupBillsOpenAIFastAtStandard(apiKey, gatewaycapture.ProjectCompletionProvider(&providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}), " FAST "))
	require.False(t, completion.GroupBillsOpenAIFastAtStandard(apiKey, gatewaycapture.ProjectCompletionProvider(&providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}), "standard"))
	require.False(t, completion.GroupBillsOpenAIFastAtStandard(apiKey, gatewaycapture.ProjectCompletionProvider(&providercore.Record{Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey}), "priority"))
}
