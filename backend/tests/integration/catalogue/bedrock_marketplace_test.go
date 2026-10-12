package catalogue_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type bedrockMarketplaceGroups struct {
	routing.GroupRepository
	groups []routing.Group
}

// newBedrockRoutingTestProvider creates a Bedrock provider with a region and optional global routing.
func newBedrockRoutingTestProvider(id int64, region string, forceGlobal bool) providercore.Record {
	provider := providercore.Record{
		ID: id, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeBedrock,
		Status: billing.StatusActive, Schedulable: true, Concurrency: 5, Priority: int(id),
		Credentials: map[string]any{
			"aws_region": region, "auth_mode": "sigv4",
			"aws_access_key_id": "test-akid", "aws_secret_access_key": "test-secret",
		},
	}
	if forceGlobal {
		provider.Credentials["aws_force_global"] = "true"
	}
	return provider
}

func (s *bedrockMarketplaceGroups) ListActive(context.Context) ([]routing.Group, error) {
	return s.groups, nil
}

func TestBedrockRegionRouting_MarketplaceUsesSharedResolution(t *testing.T) {
	groupID := int64(5201)
	for _, forceGlobal := range []bool{false, true} {
		name := "地域不可用"
		if forceGlobal {
			name = "全局可用"
		}
		t.Run(name, func(t *testing.T) {
			provider := newBedrockRoutingTestProvider(1, "ap-northeast-1", forceGlobal)
			provider.GroupIDs = []int64{groupID}
			provider.ProviderGroups = []providercore.GroupMembership{{ProviderID: 1, GroupID: groupID}}
			provider.Credentials["model_mapping"] = map[string]any{"client-alias": "claude-sonnet-5"}
			repo := &modelsListProviderRepoStub{all: []providercore.Record{provider}, byGroup: map[int64][]providercore.Record{groupID: {provider}}}
			gateway := newCatalogueFixture(repo, nil, nil)
			models := gateway.ResolveRequestableModels(context.Background(), &groupID, capability.PlatformAnthropic)
			if forceGlobal {
				require.Contains(t, routing.RequestableModelIDs(models.Models), "client-alias")
			} else {
				require.NotContains(t, routing.RequestableModelIDs(models.Models), "client-alias")
			}
			marketplace := newCatalogueMarketplace(
				&bedrockMarketplaceGroups{groups: []routing.Group{{ID: groupID, Name: "Bedrock", Status: billing.StatusActive, RateMultiplier: 1, ActiveProviderCount: 1}}},
				gateway, billingtestkit.Calculator(nil, nil),
			)
			groups, err := marketplace.ListPublic(context.Background(), routing.MarketplaceListOptions{IncludeCapacity: true})
			require.NoError(t, err)
			require.Len(t, groups, 1)
			var publicModels []string
			for _, model := range groups[0].Models {
				publicModels = append(publicModels, model.ID)
			}
			// The marketplace and requestable catalogue agree on regional exclusions, including aliases.
			require.ElementsMatch(t, routing.RequestableModelIDs(models.Models), publicModels)
			if forceGlobal {
				require.Contains(t, publicModels, "client-alias")
			} else {
				require.NotContains(t, publicModels, "client-alias")
				require.NotContains(t, publicModels, "claude-sonnet-5")
			}
		})
	}
}
