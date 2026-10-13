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

// Bedrock aliases inherit the configured source region's model eligibility.
// The public marketplace and direct requestable catalogue resolve the same
// provider mapping, so enabling global routing changes alias visibility in both
// results for the Tokyo fixture.

// bedrockMarketplaceGroups supplies fixed active-group rows to marketplace tests.
type bedrockMarketplaceGroups struct {
	// GroupRepository supplies unused methods through a nil interface; these
	// fixtures exercise the ListActive override and leave other calls unsupported.
	routing.GroupRepository
	// groups is returned directly by ListActive, so tests keep its storage stable
	// while the marketplace resolves the rows and their model lists.
	groups []routing.Group
}

// newBedrockRoutingTestProvider builds a schedulable SigV4 fixture for the supplied
// region and optional global routing. Credentials are test placeholders consumed
// by catalogue rules; callers attach group memberships and model mappings.
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
		// Bedrock's credential reader accepts the saved string form of this flag,
		// allowing the fixture to exercise the same representation as stored providers.
		provider.Credentials["aws_force_global"] = "true"
	}
	return provider
}

// ListActive returns the configured group slice for marketplace enumeration.
// Callers treat the returned storage as borrowed, and this stub relies on each
// test to supply active groups.
func (s *bedrockMarketplaceGroups) ListActive(context.Context) ([]routing.Group, error) {
	return s.groups, nil
}

// TestBedrockRegionRouting_MarketplaceUsesSharedResolution maps a client alias to
// a Claude model whose Tokyo eligibility depends on global routing. Both catalogue
// paths must expose matching model IDs and agree on the alias's regional visibility.
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
