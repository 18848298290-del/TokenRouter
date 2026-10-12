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

// TestResolveOpenAIWSRoutingModelForProviderStrictlyFollowsBillingBasis checks each turn against the configured requested, group-mapped, or upstream billing model.
func TestResolveOpenAIWSRoutingModelForProviderStrictlyFollowsBillingBasis(t *testing.T) {
	price := 0.01
	tests := []struct {
		name           string
		billingSource  string
		pricingModel   string
		expectRejected bool
	}{
		{name: "requested", billingSource: routing.BillingModelSourceRequested, pricingModel: "client-alias"},
		{name: "group_mapped", billingSource: routing.BillingModelSourceGroupMapped, pricingModel: "group-model"},
		{name: "upstream", billingSource: routing.BillingModelSourceUpstream, pricingModel: "upstream-model"},
		{name: "upstream_rejected", billingSource: routing.BillingModelSourceUpstream, pricingModel: "other-model", expectRejected: true},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groupID := int64(4300 + index)
			pricingConfig := routingtestkit.Configuration{
				ID:                 int64(80 + index),
				Status:             billing.StatusActive,
				RestrictModels:     true,
				BillingModelSource: tt.billingSource,
				ModelMapping:       map[string]string{"client-alias": "group-model"},
				ModelPricing: []routing.ModelPricingEntry{{
					Models:     []string{tt.pricingModel},
					InputPrice: &price,
				}},
			}
			svc := NewCompatible(CompatibleDependencies{Shared: Shared{GroupPolicies: routingtestkit.PricingConfig(groupID, capability.PlatformOpenAI, pricingConfig)}}, DefaultOptions())
			provider := &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 90,
					Platform:    capability.PlatformOpenAI,
					Type:        capability.ProviderTypeAPIKey,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{
						"model_mapping":   map[string]any{"group-model": "upstream-model"},
						"model_whitelist": []any{"upstream-model"},
					},
				},
			}

			routingModel, err := svc.ResolveOpenAIWSRoutingModelForProvider(
				context.Background(), &groupID, provider, "client-alias", providercore.OpenAIEndpointCapabilityTextGeneration,
			)
			if tt.expectRejected {
				require.Error(t, err)
				require.Empty(t, routingModel)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "group-model", routingModel)
		})
	}
}

// TestResolveOpenAIWSRoutingModelForProviderRejectsUnsupportedMappedModel checks that later turns respect the fixed provider's final-model allowlist.
func TestResolveOpenAIWSRoutingModelForProviderRejectsUnsupportedMappedModel(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 91,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"model_mapping":   map[string]any{"group-model": "upstream-model"},
				"model_whitelist": []any{"different-upstream-model"},
			},
		},
	}
	svc := NewCompatible(CompatibleDependencies{}, DefaultOptions())

	routingModel, err := svc.ResolveOpenAIWSRoutingModelForProvider(
		context.Background(), nil, provider, "group-model", providercore.OpenAIEndpointCapabilityTextGeneration,
	)
	require.Error(t, err)
	require.Empty(t, routingModel)
}
