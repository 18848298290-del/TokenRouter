package selection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// probes_test.go checks group probe selection and execution against controlled
// provider and policy stores. The cases retain group identity and each mapping
// stage while recording the complete request sent to background provider tests.

// probePricingStore supplies empty persisted configurations for group-policy
// probe tests.
type probePricingStore struct {
	routing.PricingConfigRepository // unused repository calls panic
}

// probeTestRecorder captures the complete background request passed by group probes.
type probeTestRecorder struct {
	request    providercore.TestRequest       // captured background test inputs
	attributes *routing.ModelAttributeSession // captured test attribute session
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
				require.Equal(t, routing.GroupProbeTarget{GroupID: group.ID, ProviderID: 3678, ModelID: groupModel}, target)
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

// RunTestBackgroundRequest records the supplied request and returns a successful observation.
func (r *probeTestRecorder) RunTestBackgroundRequest(ctx context.Context, request providercore.TestRequest) (*providercore.ScheduledTestResult, error) {
	r.request = request
	r.attributes = routing.ModelAttributesFromContext(ctx)
	return &providercore.ScheduledTestResult{Status: "success"}, nil
}

// TestProbeTestPreservesGroup verifies execution retains group identity after selection.
// Provider mapping receives the intermediate model together with that group's attribute scope,
// and execution reuses the attribute session retained during selection.
func TestProbeTestPreservesGroup(t *testing.T) {
	recorder := &probeTestRecorder{}
	probe := Probe{ProviderTest: recorder}
	session := (&routing.ModelAttributeService{}).NewSession()
	result, err := probe.Test(t.Context(), routing.GroupProbeTarget{GroupID: 59, ProviderID: 3678, ModelID: "mapped-model", Attributes: session}, "draw", "probe-client")
	require.Same(t, session, recorder.attributes)
	require.NoError(t, err)
	require.Equal(t, "success", result.Status)
	require.Equal(t, providercore.TestRequest{GroupID: 59, ProviderID: 3678, Model: "mapped-model", Prompt: "draw", UserAgent: "probe-client"}, recorder.request)
}

// TestProbeSelectUsesEntryRoute exercises both scheduler modes with mapped models and conversion policy.
// Selection retains the chosen entry and endpoint, skips untestable native routes, and rejects disabled conversions before testing.
func TestProbeSelectUsesEntryRoute(t *testing.T) {
	for _, schedulerType := range []routing.GroupSchedulerType{routing.GroupSchedulerTypeBasic, routing.GroupSchedulerTypeAdvanced} {
		for _, tc := range []struct {
			name      string                                            // selected route scenario
			entry     capability.ProtocolID                             // requested client entry
			fallbacks map[capability.ProtocolID][]capability.ProtocolID // group conversion choices
			want      capability.ProtocolID                             // expected upstream endpoint
		}{
			{"native", capability.ProtocolOpenAIChatCompletions, nil, capability.ProtocolOpenAIChatCompletions},
			{"converted", capability.ProtocolAnthropicMessages, nil, capability.ProtocolOpenAIResponses},
			{"ordered", capability.ProtocolAnthropicMessages, map[capability.ProtocolID][]capability.ProtocolID{capability.ProtocolAnthropicMessages: {capability.ProtocolOpenAIChatCompletions, capability.ProtocolOpenAIResponses}}, capability.ProtocolOpenAIChatCompletions},
			{"conversion disabled", capability.ProtocolAnthropicMessages, map[capability.ProtocolID][]capability.ProtocolID{capability.ProtocolAnthropicMessages: {}}, ""},
		} {
			t.Run(string(schedulerType)+"/"+tc.name, func(t *testing.T) {
				group := &routing.Group{
					ID: 59, Hydrated: true, Status: "active", SchedulerType: schedulerType,
					AllowedProtocols: []capability.ProtocolID{tc.entry}, ProtocolFallbacks: tc.fallbacks,
					RoutingPolicy: routing.GroupRoutingPolicy{Enabled: true, ModelMapping: map[string]string{"alias": "group-model", "group-model": "wrong-hop"}},
				}
				policies := routing.NewPricingConfigService(probePricingStore{}, nil, routing.PricingConfigOptions{ReadGroup: func(context.Context, int64) (*routing.Group, error) { return group, nil }})
				reads := Reads{Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{{Record: providercore.Record{
					ID: 9, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: "active", Schedulable: true, Concurrency: 10, LoadLocation: time.LoadLocation,
					Credentials: map[string]any{"upstream_protocols": []string{"openai_responses", "openai_chat_completions"}, "model_mapping": map[string]any{"group-model": "vendor-model"}, "model_whitelist": []string{"vendor-model"}},
				}}}}}
				probe := Probe{openAIGateway: newCompatibleSelectionForTest(CompatibleDependencies{Reads: reads, Shared: Shared{GroupPolicies: policies}}, nil)}
				ctx := requeststate.WithGroup(t.Context(), group)
				target, err := probe.Select(ctx, routing.GroupAvailabilityProbeDueGroup{GroupID: 59, Config: routing.GroupAvailabilityProbeConfig{Protocol: tc.entry}}, "alias")
				if tc.want == "" {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.Equal(t, "group-model", target.ModelID)
				require.Equal(t, providercore.TestProtocolRoute{ClientProtocol: tc.entry, UpstreamProtocol: tc.want}, target.ProtocolRoute)
				group.AllowedProtocols = nil
				_, err = probe.Select(requeststate.WithGroup(t.Context(), group), routing.GroupAvailabilityProbeDueGroup{GroupID: 59, Config: routing.GroupAvailabilityProbeConfig{Protocol: tc.entry}}, "alias")
				require.ErrorContains(t, err, "does not allow")
			})
		}
	}
}

// TestProbeSelectSkipsUntestableRoute starts with a Grok Chat-only candidate ahead of an OpenAI provider.
// The probe excludes the unsupported test endpoint, retains Chat for the next provider, and fails when every candidate is untestable.
func TestProbeSelectSkipsUntestableRoute(t *testing.T) {
	group := &routing.Group{ID: 59, Hydrated: true, Status: "active", AllowedProtocols: []capability.ProtocolID{capability.ProtocolOpenAIChatCompletions}}
	policies := routing.NewPricingConfigService(probePricingStore{}, nil, routing.PricingConfigOptions{ReadGroup: func(context.Context, int64) (*routing.Group, error) { return group, nil }})
	for _, onlyUnsupported := range []bool{false, true} {
		candidates := []gatewayprovider.ExecutionProvider{}
		for index, platform := range []string{capability.PlatformGrok, capability.PlatformOpenAI} {
			if onlyUnsupported && index == 1 {
				continue
			}
			candidates = append(candidates, gatewayprovider.ExecutionProvider{Record: providercore.Record{ID: int64(index + 1), Platform: platform, Type: capability.ProviderTypeAPIKey, Status: "active", Schedulable: true, Priority: index, Concurrency: 10, LoadLocation: time.LoadLocation, Credentials: map[string]any{"upstream_protocols": []string{"openai_chat_completions"}, "model_whitelist": []string{"custom-model"}}}})
		}
		probe := Probe{openAIGateway: newCompatibleSelectionForTest(CompatibleDependencies{Reads: Reads{Providers: selectionProviderFixture{providers: candidates}}, Shared: Shared{GroupPolicies: policies}}, nil)}
		target, err := probe.Select(requeststate.WithGroup(t.Context(), group), routing.GroupAvailabilityProbeDueGroup{GroupID: 59, Config: routing.GroupAvailabilityProbeConfig{Protocol: capability.ProtocolOpenAIChatCompletions}}, "custom-model")
		if onlyUnsupported {
			require.ErrorContains(t, err, "no testable provider")
			continue
		}
		require.NoError(t, err)
		require.Equal(t, int64(2), target.ProviderID)
		require.Equal(t, capability.ProtocolOpenAIChatCompletions, target.ProtocolRoute.UpstreamProtocol)
	}
}
