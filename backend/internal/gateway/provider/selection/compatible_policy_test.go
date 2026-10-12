package selection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
)

// TestOpenAIUpstreamRestrictionAppliesPricingConfigThenProviderMapping supplies
// a client alias, group model, and distinct provider target. The allowed final
// name is accepted after both mappings, protecting upstream-stage restriction
// inputs.
func TestOpenAIUpstreamRestrictionAppliesPricingConfigThenProviderMapping(t *testing.T) {
	groupID := int64(4202)
	price := 0.01
	pricingConfig := routingtestkit.Configuration{
		ID:                 73,
		Status:             billing.StatusActive,
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceUpstream,
		ModelMapping:       map[string]string{"client-alias": "group-model"},
		ModelPricing: []routing.ModelPricingEntry{{
			Models:     []string{"upstream-model"},
			InputPrice: &price,
		}},
	}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{},
		Shared: Shared{GroupPolicies: routingtestkit.PricingConfig(groupID, capability.PlatformOpenAI,
			pricingConfig)},
	}, nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"group-model": "upstream-model"},
			},
		},
	}

	require.False(t, upstreamRestrictedForTest(svc, context.Background(), groupID, provider, "client-alias", false))
}

// TestOpenAIUpstreamRestrictionUsesActuallyForwardedOAuthModel varies complete
// OAuth names and automatic passthrough mapping. Restrictions use the final
// forwarded identity, keeping suffix models and bare names distinct from
// independently priced models.
func TestOpenAIUpstreamRestrictionUsesActuallyForwardedOAuthModel(t *testing.T) {
	price := 0.01
	tests := []struct {
		name               string                             // named mapping scenario
		groupID            int64                              // fixture policy group
		pricingConfigModel string                             // group mapping target
		pricingModel       string                             // admitted pricing identity
		provider           *gatewayprovider.ExecutionProvider // final mapping fixture
		restricted         bool                               // expected restriction result
	}{
		{
			name:               "OAuth 后缀型号不能借用基名价格",
			groupID:            4204,
			pricingConfigModel: "gpt-5.6-sol-high",
			pricingModel:       "gpt-5.6-sol",
			restricted:         true,
			provider:           &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}},
		},
		{
			name:               "OAuth 完整后缀型号命中独立价卡",
			groupID:            4205,
			pricingConfigModel: "gpt-5.6-sol-high",
			pricingModel:       "gpt-5.6-sol-high",
			provider:           &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}},
			restricted:         false,
		},
		{
			name:               "裸名称不能隐式使用 Sol 的上游定价",
			groupID:            4207,
			pricingConfigModel: "gpt-5.6",
			pricingModel:       "gpt-5.6-sol",
			provider:           &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}},
			restricted:         true,
		},
		{
			name:               "自动透传仍按提供商映射后的模型检查",
			groupID:            4206,
			pricingConfigModel: "passthrough-model",
			pricingModel:       "mapped-model",
			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
					Type:  capability.ProviderTypeOAuth,
					Extra: map[string]any{"openai_passthrough": true},
					Credentials: map[string]any{
						"model_mapping": map[string]any{"passthrough-model": "mapped-model"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricingConfig := routingtestkit.Configuration{
				ID:                 tt.groupID,
				Status:             billing.StatusActive,
				RestrictModels:     true,
				BillingModelSource: routing.BillingModelSourceUpstream,
				ModelMapping:       map[string]string{"client-alias": tt.pricingConfigModel},
				ModelPricing: []routing.ModelPricingEntry{{
					Models:     []string{tt.pricingModel},
					InputPrice: &price,
				}},
			}
			svc := newCompatibleSelectionForTest(CompatibleDependencies{
				Reads:  Reads{},
				Shared: Shared{GroupPolicies: routingtestkit.PricingConfig(tt.groupID, capability.PlatformOpenAI, pricingConfig)},
			}, nil)

			ctx := context.Background()
			restricted := upstreamRestrictedForTest(svc, ctx, tt.groupID, tt.provider, "client-alias", false)
			require.Equal(t, tt.restricted, restricted)
		})
	}
}

// upstreamRestrictedForTest applies group mapping before compatible upstream
// restriction in the fixture. Callers pass a client model and Compact flag,
// keeping the test's provider-stage check aligned with production's resolved
// routing input.
func upstreamRestrictedForTest(s *Compatible, ctx context.Context, group int64, value *gatewayprovider.ExecutionProvider, model string, compact bool) bool {
	return s.UpstreamRoutingModelRestricted(ctx, group, value, s.resolveGroupRoutingModel(ctx, &group, model), compact)
}
