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

// TestModelForRestriction_Requested supplies different client and group-mapped
// names to the restriction-stage resolver. Requested basis selects the client
// identity, protecting the stage used by early group policy checks.
func TestModelForRestriction_Requested(t *testing.T) {
	t.Parallel()
	got := routing.ModelForRestriction(routing.BillingModelSourceRequested, "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-5", got)
}

// TestModelForRestriction_GroupMapped supplies different names before and after
// group mapping. The resolver selects the intermediate group identity,
// protecting restriction checks configured to use that model stage.
func TestModelForRestriction_GroupMapped(t *testing.T) {
	t.Parallel()
	got := routing.ModelForRestriction(routing.BillingModelSourceGroupMapped, "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-6", got)
}

// TestModelForRestriction_Upstream asks the early resolver for a model
// requiring provider mapping. The empty result defers restriction to
// per-provider checks, protecting the separation between early and final
// upstream policy.
func TestModelForRestriction_Upstream(t *testing.T) {
	t.Parallel()
	got := routing.ModelForRestriction(routing.BillingModelSourceUpstream, "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "", got, "upstream should return empty (per-provider check needed)")
}

// TestModelForRestriction_Empty leaves the restriction source unconfigured
// while supplying different model names. Group-mapped identity is selected by
// default, protecting the legacy default used by early group checks.
func TestModelForRestriction_Empty(t *testing.T) {
	t.Parallel()
	got := routing.ModelForRestriction("", "claude-sonnet-4-5", "claude-sonnet-4-6")
	require.Equal(t, "claude-sonnet-4-6", got, "empty source defaults to group_mapped")
}

// TestCheckGroupModelRestriction_NilGroupID binds a pricing service but
// supplies no request group. The precheck returns false, protecting the
// missing-group behavior before any group-specific model rule is applied.
func TestCheckGroupModelRestriction_NilGroupID(t *testing.T) {
	t.Parallel()
	svc := newGenericSelectionForTest(GenericDependencies{
		Reads:  Reads{},
		Shared: Shared{GroupPolicies: routingtestkit.NewPricingConfigService(nil, nil, routing.PricingConfigOptions{Warn: slog.Warn, Now: time.Now, LoadLocation: pricingprovider.LoadPricingLocation})},
	}, nil)

	require.False(t, svc.checkGroupModelRestriction(context.Background(), nil, "claude-sonnet-4"))
}

// TestCheckGroupModelRestriction_NilPricingConfigService supplies a request
// group with no pricing policy service. The precheck returns false, protecting
// selection callers using an omitted optional group-policy binding.
func TestCheckGroupModelRestriction_NilPricingConfigService(t *testing.T) {
	t.Parallel()
	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{}}, nil)

	gid := int64(10)
	require.False(t, svc.checkGroupModelRestriction(context.Background(), &gid, "claude-sonnet-4"))
}

// TestCheckGroupModelRestriction_EmptyModel supplies a group and policy service
// with an empty model name. Restriction remains false, protecting auxiliary
// inputs that provide no model for the precheck.
func TestCheckGroupModelRestriction_EmptyModel(t *testing.T) {
	t.Parallel()
	svc := newGenericSelectionForTest(GenericDependencies{
		Reads:  Reads{},
		Shared: Shared{GroupPolicies: routingtestkit.NewPricingConfigService(nil, nil, routing.PricingConfigOptions{Warn: slog.Warn, Now: time.Now, LoadLocation: pricingprovider.LoadPricingLocation})},
	}, nil)

	gid := int64(10)
	require.False(t, svc.checkGroupModelRestriction(context.Background(), &gid, ""))
}

// TestCheckGroupModelRestriction_GroupMapped_Restricted maps the client name to
// an intermediate model absent from the policy. The group precheck rejects it,
// protecting the configured group-mapped restriction basis.
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

// TestCheckGroupModelRestriction_GroupMapped_Allowed maps a client name onto a
// configured policy model. The precheck accepts that intermediate identity,
// protecting a permitted alias under group-mapped restrictions.
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

// TestCheckGroupModelRestriction_QoderGroupMappedBasisAllowsConfiguredRouteKey
// includes a Qoder routing name in the allowed model list with no price. The
// precheck admits it, protecting model scope independently of price
// completeness.
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

// TestCheckGroupModelRestriction_Requested_Restricted prices a name different
// from the client's requested model. Requested-basis policy rejects the
// incoming identity, protecting that stage from borrowing the configured
// alternative's admission.
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

// TestCheckGroupModelRestriction_Requested_Allowed lists the client's requested
// identity in the model policy. The precheck returns false for restriction,
// protecting requested-basis admission even before provider mapping is known.
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

// TestCheckGroupModelRestriction_Upstream_SkipsPreCheck enables restrictions at
// the final upstream stage and passes an unknown client name. Early checking
// returns false so each provider can supply its own final identity.
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

// TestCheckGroupModelRestriction_RestrictModelsDisabled supplies a narrow
// pricing list while the restriction switch is false. The arbitrary model
// passes the precheck, protecting the explicit disabled-policy setting.
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

// TestCheckGroupModelRestriction_NoPricingConfig gives the service an empty
// persisted policy list for the request group. The model precheck permits the
// name, protecting the absent independent group-policy behavior.
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

// TestIsUpstreamModelRestrictedByGroup_Restricted resolves a valid Antigravity
// model omitted from the group's allowed list. Final upstream checking rejects
// it, protecting restriction after platform model resolution.
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
		LoadLocation, Platform: capability.PlatformAntigravity}}

	require.True(t, svc.isUpstreamModelRestrictedByGroup(context.Background(), 10, provider, "claude-sonnet-4-6"),
		"upstream model claude-sonnet-4-6 NOT in pricing → restricted")
}

// TestIsUpstreamModelRestrictedByGroup_Allowed places the resolved Antigravity
// upstream name in the group's model list. The final-model check permits it,
// protecting a configured upstream identity during provider selection.
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

// TestIsUpstreamModelRestrictedByGroup_AppliesGroupMappingBeforeProviderMapping
// creates different names at the client, group, and provider stages. Only the
// final target is allowed, protecting the order of both routing
// transformations.
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

// TestIsUpstreamModelRestrictedByGroup_QoderUpstreamBasisAllowsConfiguredUpstream
// configures a Qoder upstream identity with an empty price. The allowlist still
// admits the final model, protecting scope checks independently of pricing
// availability.
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

// TestIsUpstreamModelRestrictedByGroup_UnsupportedModel gives Antigravity an
// unmapped name and a different group allowlist. The passed-through final name
// remains restricted, protecting group policy even when provider mapping has no
// match.
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
