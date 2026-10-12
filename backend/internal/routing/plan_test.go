package routing

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestRoutePlanCandidateRecalculationAndIsolation checks candidate protocol resolution and isolation from later
// configuration or slice mutations.
func TestRoutePlanCandidateRecalculationAndIsolation(t *testing.T) {
	group := &Group{
		ID: 7, SchedulerType: GroupSchedulerTypeAdvanced,
		AllowedProtocols:  []capability.ProtocolID{capability.ProtocolAnthropicMessages},
		ProtocolFallbacks: map[capability.ProtocolID][]capability.ProtocolID{capability.ProtocolAnthropicMessages: {capability.ProtocolOpenAIResponses}},
	}
	plan := Plan(PlanInput{Group: group, ClientProtocol: capability.ProtocolAnthropicMessages})
	responses := provider.ProviderSnapshot{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, EnabledProtocols: []capability.ProtocolID{capability.ProtocolOpenAIResponses}}
	chat := provider.ProviderSnapshot{ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, EnabledProtocols: []capability.ProtocolID{capability.ProtocolOpenAIChatCompletions}}
	first, ok := plan.ResolveCandidate(responses)
	require.True(t, ok)
	require.Equal(t, int64(1), first.ProviderID)
	require.Equal(t, capability.ProtocolOpenAIResponses, first.UpstreamProtocol)
	_, ok = plan.ResolveCandidate(chat)
	require.False(t, ok)
	group.ProtocolFallbacks[capability.ProtocolAnthropicMessages][0] = capability.ProtocolOpenAIChatCompletions
	plan.AllowedProtocols()[0] = capability.ProtocolLive
	again, ok := plan.ResolveCandidate(responses)
	require.True(t, ok)
	require.Equal(t, first, again)
	fresh := Plan(PlanInput{Group: group, ClientProtocol: capability.ProtocolAnthropicMessages})
	second, ok := fresh.ResolveCandidate(chat)
	require.True(t, ok)
	require.Equal(t, int64(2), second.ProviderID)
	require.Equal(t, capability.ProtocolOpenAIChatCompletions, second.UpstreamProtocol)
	require.Equal(t, GroupSchedulerTypeAdvanced, plan.SchedulerType())
	require.Equal(t, int64(7), plan.GroupID())
	require.Equal(t, []capability.ProtocolID{capability.ProtocolAnthropicMessages}, plan.AllowedProtocols())
}

// TestRoutePlanModelChainAndAttemptSnapshots checks fixed client/key/group model values and fresh provider mapping
// per attempt.
func TestRoutePlanModelChainAndAttemptSnapshots(t *testing.T) {
	groupID := int64(7)
	mapping := GroupMappingResult{MappedModel: "group-model", PricingConfigID: 9, Mapped: true, BillingModelSource: "requested", RestrictModels: true, RestrictionModelSource: BillingModelSourceUpstream, ClientModel: "prefix/client-model", APIKeyRedirected: true}
	plan := Plan(PlanInput{GroupID: &groupID, RequestedModel: "key-model", GroupMapping: mapping, ClientProtocol: capability.ProtocolOpenAIResponses})
	groupID = 8
	require.Equal(t, int64(7), plan.GroupID())
	require.Equal(t, mapping, plan.Mapping())
	require.Equal(t, "prefix/client-model", plan.Models().ClientModel)
	require.Equal(t, "key-model", plan.Models().RequestedModel)
	candidate, ok := plan.ResolveCandidate(provider.ProviderSnapshot{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, EnabledProtocols: []capability.ProtocolID{capability.ProtocolOpenAIResponses}})
	require.True(t, ok)
	rules := map[string]string{"group-model": "upstream-one", "upstream-one": "must-not-recurse"}
	snapshot := provider.ProviderSnapshot{ID: 1, ModelPolicy: provider.NewModelRoutingSnapshot(rules)}
	rules["group-model"] = "upstream-two"
	first, matched := candidate.ResolveModel(snapshot, "group-model")
	require.True(t, matched)
	require.Equal(t, "upstream-one", first.Models.ProviderMappedModel)
	require.True(t, first.Models.RestrictModels)
	require.Equal(t, BillingModelSourceUpstream, first.Models.RestrictionModelSource)
	require.Empty(t, candidate.Models.ProviderMappedModel)
	require.Empty(t, plan.Models().ProviderMappedModel)
	fresh := provider.ProviderSnapshot{ID: 1, ModelPolicy: provider.NewModelRoutingSnapshot(rules)}
	second, matched := candidate.ResolveModel(fresh, "group-model")
	require.True(t, matched)
	require.Equal(t, "upstream-two", second.Models.ProviderMappedModel)
}
