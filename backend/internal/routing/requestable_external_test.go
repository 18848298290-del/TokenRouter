package routing_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// countedCataloguePolicies counts billing-source reads per resolution.
type countedCataloguePolicies struct {
	*routing.PricingConfigService
	pricingReads int
}

// countedCatalogueRules counts eligibility checks for catalogue and selected-model queries.
type countedCatalogueRules struct {
	routing.CatalogueRules
	checks *int
}

// TestCatalogueIncludesGroupMappingTargets checks that concrete group-mapping targets enter candidate validation.
func TestCatalogueIncludesGroupMappingTargets(t *testing.T) {
	for _, source := range []string{"client-alias", "client-*"} {
		t.Run(source, func(t *testing.T) {
			groupID := int64(1)
			group := &routing.Group{ID: groupID, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}, RoutingPolicy: routing.GroupRoutingPolicy{Enabled: true, ModelMapping: map[string]string{source: "gpt-5.6-sol"}}}
			policies := &countedCataloguePolicies{PricingConfigService: routing.NewPricingConfigService(nil, nil, routing.PricingConfigOptions{ReadGroup: func(context.Context, int64) (*routing.Group, error) {
				return group, nil
			}})}
			resolver := routing.RequestableResolver{GroupPolicies: policies}
			records := []provider.Record{{Platform: "openai", Type: "apikey", Credentials: map[string]any{}}}
			providers := gatewayprovider.CatalogueProviders(records)
			result := resolver.ResolveWithProviders(context.Background(), &groupID, "", nil, providers)
			want := []string{"gpt-5.6-sol"}
			if source == "client-alias" {
				want = append(want, source)
			}
			require.ElementsMatch(t, want, routing.RequestableModelIDs(result.Models))
			selected := resolver.ResolveSelectedWithProviders(context.Background(), &groupID, "", []string{"gpt-5.6-sol"}, providers)
			require.Equal(t, []string{"gpt-5.6-sol"}, routing.RequestableModelIDs(selected.Models))

			group.RoutingPolicy.RestrictModels = true
			group.RoutingPolicy.RestrictionModelSource = routing.BillingModelSourceRequested
			group.RoutingPolicy.AllowedModels = []string{source}
			result = resolver.ResolveWithProviders(context.Background(), &groupID, "", nil, providers)
			require.NotContains(t, routing.RequestableModelIDs(result.Models), "gpt-5.6-sol")

			group.RoutingPolicy.RestrictModels = false
			records[0].Credentials["model_whitelist"] = []string{"other-model"}
			result = resolver.ResolveWithProviders(context.Background(), &groupID, "", nil, gatewayprovider.CatalogueProviders(records))
			require.NotContains(t, routing.RequestableModelIDs(result.Models), "gpt-5.6-sol")
		})
	}
}

// TestCatalogueReadsGroupOnce checks reuse of one group snapshot across catalogue and restriction stages.
func TestCatalogueReadsGroupOnce(t *testing.T) {
	for _, stage := range []string{routing.BillingModelSourceRequested, routing.BillingModelSourceGroupMapped, routing.BillingModelSourceUpstream} {
		t.Run(stage, func(t *testing.T) {
			ids := []string{"allowed"}
			for i := range 12000 {
				ids = append(ids, fmt.Sprintf("model-%d", i))
			}
			reads := 0
			policies := &countedCataloguePolicies{PricingConfigService: routing.NewPricingConfigService(nil, nil, routing.PricingConfigOptions{ReadGroup: func(context.Context, int64) (*routing.Group, error) {
				reads++
				return &routing.Group{ID: 1, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}, RoutingPolicy: routing.GroupRoutingPolicy{Enabled: true, RestrictModels: true, RestrictionModelSource: stage, AllowedModels: []string{"allowed"}}}, nil
			}})}
			resolver := routing.RequestableResolver{GroupPolicies: policies}
			records := []provider.Record{{Platform: "openai", Type: "apikey", Credentials: map[string]any{"model_whitelist": []string{"allowed"}}}}
			group := int64(1)
			result := resolver.ResolveWithProviders(context.Background(), &group, "", ids, gatewayprovider.CatalogueProviders(records))
			require.Equal(t, []string{"allowed"}, routing.RequestableModelIDs(result.Models))
			require.Equal(t, "allowed", result.Models[0].PricingModel)
			require.Equal(t, 1, reads)
			require.Equal(t, 1, policies.pricingReads)
		})
	}
}

// GetPricingConfigForGroup counts billing-source reads.
func (p *countedCataloguePolicies) GetPricingConfigForGroup(context.Context, int64) (*routing.PricingConfig, error) {
	p.pricingReads++
	return &routing.PricingConfig{BillingModelSource: routing.BillingModelSourceUpstream}, nil
}

func (r countedCatalogueRules) Supports(ctx context.Context, model string) bool {
	*r.checks++
	return r.CatalogueRules.Supports(ctx, model)
}

func (r countedCatalogueRules) SupportsClientProtocol(model string, source protocol.ProtocolID) bool {
	if rules, ok := r.CatalogueRules.(interface {
		SupportsClientProtocol(string, protocol.ProtocolID) bool
	}); ok {
		return rules.SupportsClientProtocol(model, source)
	}
	return true
}

// TestSelectedCatalogueChecksOnlyTargets checks one policy read and eligibility checks limited to selected names.
func TestSelectedCatalogueChecksOnlyTargets(t *testing.T) {
	groupID := int64(1)
	reads := 0
	policies := &countedCataloguePolicies{PricingConfigService: routing.NewPricingConfigService(nil, nil, routing.PricingConfigOptions{ReadGroup: func(context.Context, int64) (*routing.Group, error) {
		reads++
		return &routing.Group{ID: groupID, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}, ModelsListConfig: routing.GroupModelsListConfig{Enabled: true, Models: []string{"manual"}}}, nil
	}})}
	resolver := routing.RequestableResolver{GroupPolicies: policies}
	records := []provider.Record{{Platform: "openai", Type: "apikey", Credentials: map[string]any{"model_whitelist": []string{}, "model_mapping": map[string]any{"alias": "target"}}}}
	providers := gatewayprovider.CatalogueProviders(records)
	checks := 0
	providers[0].Rules = countedCatalogueRules{providers[0].Rules, &checks}
	result := resolver.ResolveSelectedWithProviders(context.Background(), &groupID, "", []string{"target", "manual", "not-declared"}, providers)
	require.ElementsMatch(t, []string{"target", "manual"}, routing.RequestableModelIDs(result.Models))
	require.Equal(t, 2, checks)
	require.Equal(t, 1, reads)
	require.Equal(t, 1, policies.pricingReads)
	result = resolver.ResolveWithProviders(context.Background(), &groupID, "", nil, providers)
	require.ElementsMatch(t, []string{"alias", "target", "manual"}, routing.RequestableModelIDs(result.Models))
}

// BenchmarkRequestableConfiguredProviders measures cold resolution, including rule preparation, for 65 providers
// and 186 entries.
func BenchmarkRequestableConfiguredProviders(b *testing.B) {
	whitelist := make([]any, 186)
	mapping := map[string]any{}
	for i := range whitelist {
		model := fmt.Sprintf("probe-model-%03d", i)
		whitelist[i] = model
		mapping[model] = model
	}
	records := make([]provider.Record, 65)
	for i := range records {
		records[i] = provider.Record{ID: int64(i + 1), Platform: "openai", Type: "apikey", Credentials: map[string]any{"model_whitelist": whitelist, "model_mapping": mapping}}
	}
	groupID := int64(1)
	policies := routing.NewPricingConfigService(nil, nil, routing.PricingConfigOptions{ReadGroup: func(context.Context, int64) (*routing.Group, error) {
		return &routing.Group{ID: groupID, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolOpenAIResponses}}, nil
	}})
	resolver := routing.RequestableResolver{GroupPolicies: &countedCataloguePolicies{PricingConfigService: policies}}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		result := resolver.ResolveWithProviders(context.Background(), &groupID, "", nil, gatewayprovider.CatalogueProviders(records))
		if len(result.Models) != 186 {
			b.Fatalf("models=%d", len(result.Models))
		}
	}
}

func TestRequestableCompactAttributesUseFinalModel(t *testing.T) {
	ctx := context.Background()
	id := int64(1)
	record := provider.Record{ID: 1, Platform: "openai", Type: "oauth", Credentials: map[string]any{"model_whitelist": []string{"gpt-5.4", "gpt-5.6-sol"}, "compact_model_mapping": map[string]any{"gpt-5.4": "gpt-5.6-sol"}}}
	group := &routing.Group{ID: id, AllowedProtocols: []protocol.ProtocolID{protocol.ProtocolResponsesCompact}}
	policies := routing.NewPricingConfigService(nil, nil, routing.PricingConfigOptions{ReadGroup: func(context.Context, int64) (*routing.Group, error) { return group, nil }})
	resolver := routing.RequestableResolver{GroupPolicies: &countedCataloguePolicies{PricingConfigService: policies}}
	result := resolver.ResolveSelectedWithProviders(ctx, &id, "", []string{"gpt-5.4"}, gatewayprovider.CatalogueProviders([]provider.Record{record}))
	require.Len(t, result.Models, 1)
	require.Len(t, result.Models[0].AttributeTargets, 1)
	_, runtimeModel := (gatewayprovider.ModelPolicy{Record: &record}).ForwardMappedModels("gpt-5.4", true)
	require.Equal(t, "gpt-5.6-sol", runtimeModel)
	require.Equal(t, runtimeModel, result.Models[0].AttributeTargets[0].Model)
}
