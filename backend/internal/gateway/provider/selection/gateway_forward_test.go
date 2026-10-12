package selection

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	pricingprovider "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
)

func TestModelForRestriction_Requested(t *testing.T) {
	t.Parallel()
	got := routing.ModelForRestriction(routing.BillingModelSourceRequested, "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-5", got)
}

func TestModelForRestriction_GroupMapped(t *testing.T) {
	t.Parallel()
	got := routing.ModelForRestriction(routing.BillingModelSourceGroupMapped, "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-6", got)
}

func TestModelForRestriction_Upstream(t *testing.T) {
	t.Parallel()
	got := routing.ModelForRestriction(routing.BillingModelSourceUpstream, "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "", got, "upstream should return empty (per-provider check needed)")
}

func TestModelForRestriction_Empty(t *testing.T) {
	t.Parallel()
	got := routing.ModelForRestriction("", "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-6", got, "empty source defaults to group_mapped")
}

func TestCheckGroupModelRestriction_NilGroupID(t *testing.T) {
	t.Parallel()
	svc := newGenericSelectionForTest(GenericDependencies{
		Reads:  Reads{},
		Shared: Shared{GroupPolicies: routingtestkit.NewPricingConfigService(nil, nil, routing.PricingConfigOptions{Warn: slog.Warn, Now: time.Now, LoadLocation: pricingprovider.LoadPricingLocation})},
	}, nil)

	require.False(t, svc.checkGroupModelRestriction(context.Background(), nil, "claude-sonnet-4"))
}

func TestCheckGroupModelRestriction_NilPricingConfigService(t *testing.T) {
	t.Parallel()
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{}}, nil)

	gid := int64(10)
	require.False(t, svc.checkGroupModelRestriction(context.Background(), &gid, "claude-sonnet-4"))
}

func TestCheckGroupModelRestriction_EmptyModel(t *testing.T) {
	t.Parallel()
	svc := newGenericSelectionForTest(GenericDependencies{
		Reads:  Reads{},
		Shared: Shared{GroupPolicies: routingtestkit.NewPricingConfigService(nil, nil, routing.PricingConfigOptions{Warn: slog.Warn, Now: time.Now, LoadLocation: pricingprovider.LoadPricingLocation})},
	}, nil)

	gid := int64(10)
	require.False(t, svc.checkGroupModelRestriction(context.Background(), &gid, ""))
}

func TestCheckGroupModelRestriction_GroupMapped_Restricted(t *testing.T) {
	t.Parallel()
	// The group maps claude-sonnet-4-5 to claude-sonnet-4-6, but pricing lists only claude-opus-4-6.
	ch := routingtestkit.Configuration{
		ID:                 1,
		Status:             billing.StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceGroupMapped,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"claude-opus-4-6"}},
		},
		ModelMapping: map[string]string{"claude-sonnet-4-5": "claude-sonnet-4-6"},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: "anthropic"}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	gid := int64(10)
	require.True(t, svc.checkGroupModelRestriction(context.Background(), &gid, "claude-sonnet-4-5"),
		"mapped model claude-sonnet-4-6 is NOT in pricing → restricted")
}

func TestCheckGroupModelRestriction_GroupMapped_Allowed(t *testing.T) {
	t.Parallel()
	// The group maps claude-sonnet-4-5 to claude-sonnet-4-6, which pricing includes.
	ch := routingtestkit.Configuration{
		ID:                 1,
		Status:             billing.StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceGroupMapped,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"claude-sonnet-4-6"}},
		},
		ModelMapping: map[string]string{"claude-sonnet-4-5": "claude-sonnet-4-6"},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: "anthropic"}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	gid := int64(10)
	require.False(t, svc.checkGroupModelRestriction(context.Background(), &gid, "claude-sonnet-4-5"),
		"mapped model claude-sonnet-4-6 IS in pricing → allowed")
}

func TestCheckGroupModelRestriction_QoderGroupMappedBasisAllowsConfiguredRouteKey(t *testing.T) {
	t.Parallel()
	price := 1e-6
	ch := routingtestkit.Configuration{
		ID:                 1,
		Status:             billing.StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceGroupMapped,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"qmodel"}, BillingMode: routing.BillingModeToken},
			{Models: []string{"qwen3.7-plus"}, BillingMode: routing.BillingModeToken, InputPrice: &price},
		},
		ModelMapping: map[string]string{"qwen3.7-plus": "qmodel"},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: capability.PlatformQoder}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	gid := int64(10)
	require.False(t, svc.checkGroupModelRestriction(context.Background(), &gid, "qwen3.7-plus"),
		"Qoder 与其他平台一样按已配置的模型白名单放行，不要求填写单价")
}

func TestCheckGroupModelRestriction_Requested_Restricted(t *testing.T) {
	t.Parallel()
	// With requested billing, a priced claude-sonnet-4-6 does not admit a claude-sonnet-4-5 request.
	ch := routingtestkit.Configuration{
		ID:                 1,
		Status:             billing.StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceRequested,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"claude-sonnet-4-6"}},
		},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: "anthropic"}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	gid := int64(10)
	require.True(t, svc.checkGroupModelRestriction(context.Background(), &gid, "claude-sonnet-4-5"),
		"requested model claude-sonnet-4-5 is NOT in pricing → restricted")
}

func TestCheckGroupModelRestriction_Requested_Allowed(t *testing.T) {
	t.Parallel()
	ch := routingtestkit.Configuration{
		ID:                 1,
		Status:             billing.StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceRequested,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"claude-sonnet-4-5"}},
		},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: "anthropic"}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	gid := int64(10)
	require.False(t, svc.checkGroupModelRestriction(context.Background(), &gid, "claude-sonnet-4-5"),
		"requested model IS in pricing → allowed")
}

func TestCheckGroupModelRestriction_Upstream_SkipsPreCheck(t *testing.T) {
	t.Parallel()
	// Upstream restrictions skip the precheck and are evaluated per provider.
	ch := routingtestkit.Configuration{
		ID:                 1,
		Status:             billing.StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceUpstream,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"claude-opus-4-6"}},
		},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: "anthropic"}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	gid := int64(10)
	require.False(t, svc.checkGroupModelRestriction(context.Background(), &gid, "unknown-model"),
		"upstream mode should skip pre-check (per-provider check needed)")
}

func TestCheckGroupModelRestriction_RestrictModelsDisabled(t *testing.T) {
	t.Parallel()
	ch := routingtestkit.Configuration{
		ID:             1,
		Status:         billing.StatusActive,
		GroupIDs:       []int64{10},
		RestrictModels: false, // Model restrictions are disabled.
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"claude-opus-4-6"}},
		},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: "anthropic"}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	gid := int64(10)
	require.False(t, svc.checkGroupModelRestriction(context.Background(), &gid, "any-model"),
		"RestrictModels=false → always allowed")
}

func TestCheckGroupModelRestriction_NoPricingConfig(t *testing.T) {
	t.Parallel()
	// The group has no independent model policy.
	repo := &routingtestkit.PricingConfigRepositoryStub{
		ListAllFn: func(_ context.Context) ([]routingtestkit.Configuration, error) { return nil, nil },
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(repo)
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	gid := int64(999)
	require.False(t, svc.checkGroupModelRestriction(context.Background(), &gid, "any-model"),
		"no channel for group → allowed")
}

func TestIsUpstreamModelRestrictedByGroup_Restricted(t *testing.T) {
	t.Parallel()
	ch := routingtestkit.Configuration{
		ID:             1,
		Status:         billing.StatusActive,
		GroupIDs:       []int64{10},
		RestrictModels: true,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"claude-opus-4-6"}},
		},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: "anthropic"}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.
		// The default Antigravity mapping keeps claude-sonnet-4-6 unchanged,
		// but pricing lists only claude-opus-4-6.
		LoadLocation, Platform: capability.PlatformAntigravity}}

	require.True(t, svc.isUpstreamModelRestrictedByGroup(context.Background(), 10, provider, "claude-sonnet-4-6"),
		"upstream model claude-sonnet-4-6 NOT in pricing → restricted")
}

func TestIsUpstreamModelRestrictedByGroup_Allowed(t *testing.T) {
	t.Parallel()
	ch := routingtestkit.Configuration{
		ID:             1,
		Status:         billing.StatusActive,
		GroupIDs:       []int64{10},
		RestrictModels: true,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"claude-sonnet-4-6"}},
		},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: "anthropic"}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, Platform: capability.PlatformAntigravity}}
	require.False(t, svc.isUpstreamModelRestrictedByGroup(context.Background(), 10, provider, "claude-sonnet-4-6"),
		"upstream model claude-sonnet-4-6 IS in pricing → allowed")
}

func TestIsUpstreamModelRestrictedByGroup_AppliesGroupMappingBeforeProviderMapping(t *testing.T) {
	t.Parallel()
	price := 1e-6
	ch := routingtestkit.Configuration{
		ID:                 1,
		Status:             billing.StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceUpstream,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"ultimate"}, BillingMode: routing.BillingModeToken, InputPrice: &price},
		},
		ModelMapping: map[string]string{"my-qoder": "qmodel"},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: capability.PlatformQoder}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformQoder,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"qmodel": "ultimate"},
			},
		},
	}

	require.False(t, svc.isUpstreamModelRestrictedByGroup(context.Background(), 10, provider, "my-qoder"),
		"upstream restriction should check the final model after channel mapping and provider mapping")
}

func TestIsUpstreamModelRestrictedByGroup_QoderUpstreamBasisAllowsConfiguredUpstream(t *testing.T) {
	t.Parallel()
	price := 1e-6
	ch := routingtestkit.Configuration{
		ID:                 1,
		Status:             billing.StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceUpstream,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"qmodel"}, BillingMode: routing.BillingModeToken},
			{Models: []string{"qwen3.7-plus"}, BillingMode: routing.BillingModeToken, InputPrice: &price},
		},
		ModelMapping: map[string]string{"qwen3.7-plus": "qmodel"},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: capability.PlatformQoder}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, Platform: capability.PlatformQoder}}

	require.False(t, svc.isUpstreamModelRestrictedByGroup(context.Background(), 10, provider, "qwen3.7-plus"),
		"已配置的上游模型属于白名单，价格为空不影响放行")
}

func TestIsUpstreamModelRestrictedByGroup_UnsupportedModel(t *testing.T) {
	t.Parallel()
	ch := routingtestkit.Configuration{
		ID:             1,
		Status:         billing.StatusActive,
		GroupIDs:       []int64{10},
		RestrictModels: true,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"claude-opus-4-6"}},
		},
	}
	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(ch, map[int64]string{10: "anthropic"}))
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{GroupPolicies: pricingConfigSvc}}, nil)

	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAntigravity}}

	require.True(t, svc.isUpstreamModelRestrictedByGroup(context.Background(), 10, provider, "totally-unknown-model"),
		"未命中提供商映射的模型继续接受分组模型限制")
}
