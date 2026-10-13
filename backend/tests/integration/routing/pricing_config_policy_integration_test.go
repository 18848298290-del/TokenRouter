//go:build integration

package routing_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/routing/postgres"
)

// Group mappings and allowlists are persisted independently of shared price
// configurations. A duplicated group keeps its own policy, while creating,
// disabling, and deleting a shared price configuration leaves the source group's
// routing decisions available through the pricing service.

// TestPricingConfigCRUDPreservesIndependentGroupPolicies saves and duplicates a
// group policy before changing a shared price configuration's lifecycle. Stored
// zero prices and group associations must survive creation, and subsequent
// pricing changes must leave the source mapping and restrictions intact.
func TestPricingConfigCRUDPreservesIndependentGroupPolicies(t *testing.T) {
	ctx := context.Background()
	client, db := routingDatabase(t)
	groups := newGroupStoreFixture(client, db)
	source := &routing.Group{
		Name: "price-policy-source", Status: routing.StatusActive, RateMultiplier: 1,
		AllowedProtocols:     capability.DefaultGroupClientProtocols(routing.PlatformOpenAI),
		ProtocolFallbacks:    capability.DefaultProtocolFallbacks(routing.PlatformOpenAI),
		ResponsesImagePolicy: "inherit",
		RoutingPolicy: routing.GroupRoutingPolicy{
			Enabled: true, RestrictModels: true, RestrictionModelSource: routing.BillingModelSourceGroupMapped,
			ModelMapping: map[string]string{"alias": "gpt-allowed"}, AllowedModels: []string{"gpt-allowed"},
			FeaturesConfig: map[string]any{"codex_image_generation_bridge": map[string]any{"openai": false}},
		},
	}
	require.NoError(t, groups.Create(ctx, source))
	loaded, err := groups.GetByID(ctx, source.ID)
	require.NoError(t, err)
	require.Equal(t, source.RoutingPolicy, loaded.RoutingPolicy)
	duplicate := routing.CloneGroupForDuplicate(loaded, "pricing-policy-copy")
	require.NoError(t, groups.CreateFromSource(ctx, duplicate, source.ID))
	copied, err := groups.GetByID(ctx, duplicate.ID)
	require.NoError(t, err)
	require.Equal(t, source.RoutingPolicy, copied.RoutingPolicy)
	copied.RoutingPolicy.RestrictionModelSource = routing.BillingModelSourceRequested
	require.NoError(t, groups.Update(ctx, copied))
	require.Equal(t, routing.BillingModelSourceGroupMapped, source.RoutingPolicy.RestrictionModelSource)

	store := postgres.NewPricingConfigStore(db)
	service := routing.NewPricingConfigService(store, nil, routing.PricingConfigOptions{ReadGroup: groups.GetByIDLite, LoadLocation: time.LoadLocation})
	// A nonnil zero price distinguishes a configured free price from a missing
	// amount, so the persisted pointer is checked before exercising the policy reads.
	zero := 0.0
	config, err := service.Create(ctx, &routing.CreatePricingConfigInput{Name: "shared-price", GroupIDs: []int64{source.ID, duplicate.ID}, ModelPricing: []routing.ModelPricingEntry{{Models: []string{"gpt-other"}, InputPrice: &zero, BillingMode: routing.BillingModeToken}}})
	require.NoError(t, err)
	require.Len(t, config.ModelPricing, 1)
	require.NotNil(t, config.ModelPricing[0].InputPrice)
	require.Zero(t, *config.ModelPricing[0].InputPrice)
	require.ElementsMatch(t, []int64{source.ID, duplicate.ID}, config.GroupIDs)
	// check rereads the source group's alias mapping and model restrictions through
	// the pricing service. Running it after each price mutation verifies that
	// group policy remains available across price-cache invalidation.
	check := func() {
		t.Helper()
		require.Equal(t, "gpt-allowed", service.ResolveGroupMapping(ctx, source.ID, "alias").MappedModel)
		require.False(t, service.IsModelRestricted(ctx, source.ID, "gpt-allowed"))
		require.True(t, service.IsModelRestricted(ctx, source.ID, "gpt-other"))
	}
	check()
	_, err = service.Update(ctx, config.ID, &routing.UpdatePricingConfigInput{Status: routing.StatusDisabled, BillingModelSource: routing.BillingModelSourceUpstream})
	require.NoError(t, err)
	check()
	require.NoError(t, service.Delete(ctx, config.ID))
	check()
	loaded, err = groups.GetByID(ctx, source.ID)
	require.NoError(t, err)
	require.Equal(t, source.RoutingPolicy, loaded.RoutingPolicy)
}
