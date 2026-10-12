package selection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// probePricingStore supplies empty persisted configurations for group-policy
// probe tests.
type probePricingStore struct {
	routing.PricingConfigRepository // unused repository calls panic
}

// TestProbeSelectPreservesGroupMappedModel supplies a client alias and a
// second-hop trap for both selectors. Probe targets retain one group mapping
// for later provider testing, and restricted intermediate names cannot be
// reintroduced.
func TestProbeSelectPreservesGroupMappedModel(t *testing.T) {
	for _, selector := range []string{"compatible", "generic"} {
		for _, mapped := range []bool{false, true} {
			t.Run(selector+map[bool]string{false: "/unchanged", true: "/mapped"}[mapped], func(t *testing.T) {
				requested := "gemini-3.8-flash"
				groupModel := requested
				mapping := map[string]string{}
				if mapped {
					groupModel = "gemini-3.8-flash-tiered"
					mapping[requested] = groupModel
					// A second rule turns the intermediate name into a trap. Applying
					// group mapping twice would produce a target the provider cannot serve.
					mapping[groupModel] = "wrong-second-hop"
				}
				group := &routing.Group{ID: 59, RoutingPolicy: routing.GroupRoutingPolicy{
					Enabled: true, RestrictModels: true, RestrictionModelSource: routing.BillingModelSourceRequested,
					AllowedModels: []string{requested}, ModelMapping: mapping,
				}}
				policies := routing.NewPricingConfigService(probePricingStore{}, nil, routing.PricingConfigOptions{
					ReadGroup: func(context.Context, int64) (*routing.Group, error) { return group, nil },
				})
				platform := capability.PlatformOpenAI
				if selector == "generic" {
					platform = capability.PlatformAnthropic
				}
				reads := Reads{Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{{Record: providercore.Record{
					ID: 3678, Platform: platform, Type: capability.ProviderTypeAPIKey,
					Status: "active", Schedulable: true, LoadLocation: time.LoadLocation,
					Credentials: map[string]any{
						"model_mapping":   map[string]any{groupModel: "vendor-model"},
						"model_whitelist": []string{"vendor-model"},
					},
				}}}}}
				probe := Probe{}
				if selector == "compatible" {
					probe.openAIGateway = newCompatibleSelectionForTest(CompatibleDependencies{Reads: reads, Shared: Shared{GroupPolicies: policies}}, nil)
				} else {
					probe.gatewaySvc = newGenericSelectionForTest(GenericDependencies{Reads: reads, Shared: Shared{GroupPolicies: policies}}, nil)
				}
				target, err := probe.Select(context.Background(), routing.GroupAvailabilityProbeDueGroup{GroupID: group.ID}, requested)
				require.NoError(t, err)
				// The testing service applies provider mapping after group selection.
				require.Equal(t, routing.GroupProbeTarget{ProviderID: 3678, ModelID: groupModel}, target)
				if mapped {
					_, err = probe.Select(context.Background(), routing.GroupAvailabilityProbeDueGroup{GroupID: group.ID}, groupModel)
					require.Error(t, err)
				}
			})
		}
	}
}

// ListAll returns no persisted pricing configurations for the probe
// group-policy fixture.
func (probePricingStore) ListAll(context.Context) ([]routing.PricingConfig, error) {
	return nil, nil
}
