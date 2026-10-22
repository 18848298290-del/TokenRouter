package selection

import (
	"context"
	"fmt"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// probes.go selects availability-probe targets and passes the group-mapped
// model and group identity to provider testing.
// Group availability probes choose an eligible provider using the client model,
// but provider testing accepts the group-mapped identity. Passing a client alias
// directly to that test would skip the group mapping; mapping an intermediate
// name again could follow an unintended second rule.
//
// Selection returns the group, provider, and intermediate group model,
// leaving provider mapping to the testing service. Explicit entries retain the
// route plan and attribute session through selection and testing. Compatible selection is
// preferred when bound, with generic selection supplying the other path.
// The measured provider-test result is then copied into routing's observation
// value, including populated results that accompany an error.

// Probe selects a group provider and tests the group-mapped model.
type Probe struct {
	// Attributes creates the session shared by routed selection and testing. A nil
	// service creates a session without group configuration for isolated callers.
	Attributes      *routing.ModelAttributeService // group probe attribute sessions
	ProviderTest    ProviderTester                 // selected provider testing
	gatewaySvc      *Generic                       // generic probe selector
	openAIGateway   *Compatible                    // preferred probe selector
	geminiCompatSvc *Gemini                        // retained Gemini dependency
}

// ProviderTester runs a test against a selected provider.
type ProviderTester interface {
	// RunTestBackgroundRequest executes a provider test with model,
	// group, prompt, and client metadata. The caller receives the measured result and
	// error for recording the group's availability observation.
	RunTestBackgroundRequest(context.Context, provider.TestRequest) (*provider.ScheduledTestResult, error)
}

// NewProbe binds the provider tester and available selectors for group
// availability checks. Selection prefers the compatible selector, then the
// generic selector; callers supply a tester before invoking Test.
func NewProbe(test ProviderTester, generic *Generic, compatible *Compatible, gemini *Gemini) Probe {
	return Probe{ProviderTest: test, gatewaySvc: generic, openAIGateway: compatible, geminiCompatSvc: gemini}
}

// Select returns the provider and group-mapped model for an availability probe.
// Provider choice uses the incoming client model, and errors stop preparation
// before a probe target is returned.
//
// The testing service applies provider mapping to its model input. This method
// therefore resolves the selected group's intermediate name through the same
// bound selector, keeping a client alias from being sent directly or group
// mapping from being applied twice. Callers pass the returned ModelID to
// provider testing with its associated GroupID and ProviderID. A specified entry
// also retains its resolved upstream protocol and attribute session.
func (s Probe) Select(ctx context.Context, due routing.GroupAvailabilityProbeDueGroup, model string) (routing.GroupProbeTarget, error) {
	if due.Config.Protocol != "" {
		return s.selectProtocolTarget(ctx, due, model)
	}
	provider, err := s.selectProbeProvider(ctx, due, model)
	if err != nil {
		return routing.GroupProbeTarget{}, err
	}
	// The testing service performs provider mapping on its model input. Return
	// the single group-mapping result here so client aliases follow the same stages.
	routingModel := model
	if s.openAIGateway != nil {
		routingModel = s.openAIGateway.resolveGroupRoutingModel(ctx, &due.GroupID, model)
	} else if s.gatewaySvc != nil {
		routingModel = s.gatewaySvc.groupMappedModelForGroup(ctx, &due.GroupID, model)
	}
	return routing.GroupProbeTarget{GroupID: due.GroupID, ProviderID: provider.Record.ID, ModelID: routingModel}, nil
}

// Test delegates one provider probe and converts its timings, status, and error
// text to routing's result. A nil provider-test result returns nil with the
// same error; a populated result can accompany an error.
func (s Probe) Test(ctx context.Context, target routing.GroupProbeTarget, prompt, userAgent string) (*routing.ProbeExecutionResult, error) {
	if target.Attributes != nil {
		ctx = target.Attributes.WithContext(ctx)
	}
	result, err := s.ProviderTest.RunTestBackgroundRequest(ctx, provider.TestRequest{GroupID: target.GroupID, ProviderID: target.ProviderID, Model: target.ModelID, Prompt: prompt, UserAgent: userAgent, ProtocolRoute: target.ProtocolRoute})
	if result == nil {
		return nil, err
	}
	return &routing.ProbeExecutionResult{Status: result.Status, LatencyMs: result.LatencyMs, ErrorMessage: result.ErrorMessage, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt}, err
}

// selectProtocolTarget selects a provider under a fixed group entry and route plan.
// It reads current group policy, applies one group mapping, and asks the existing
// selector for eligible candidates. Unsupported test routes are excluded before
// another candidate is requested; no upstream request occurs during selection.
//
// The route plan retains the group's conversion order and model chain throughout
// that search. Testing receives the intermediate model and both protocols, so
// provider mapping remains one later step and a platform default cannot replace
// the route that passed scheduling. Missing groups and disabled entries stop
// selection before any provider is tested.
func (s Probe) selectProtocolTarget(ctx context.Context, due routing.GroupAvailabilityProbeDueGroup, model string) (routing.GroupProbeTarget, error) {
	var read func(context.Context, int64) (*routing.Group, error)
	var policies *routing.PricingConfigService
	var selectProvider func(context.Context, *int64, string, string, map[int64]struct{}) (*gatewayprovider.ExecutionProvider, error)
	if s.openAIGateway != nil {
		read = s.openAIGateway.readSchedulingGroup
		policies = s.openAIGateway.groupPolicies
		selectProvider = s.openAIGateway.SelectProviderForModelWithExclusions
	} else if s.gatewaySvc != nil {
		read = s.gatewaySvc.readSchedulingGroup
		policies = s.gatewaySvc.groupPolicies
		selectProvider = s.gatewaySvc.SelectProviderForModelWithExclusions
	} else {
		return routing.GroupProbeTarget{}, fmt.Errorf("provider selector not configured")
	}
	group, err := currentSelectionGroup(ctx, &due.GroupID, read)
	if err != nil {
		return routing.GroupProbeTarget{}, err
	}
	if group == nil || !group.AllowsClientProtocol(due.Config.Protocol) {
		return routing.GroupProbeTarget{}, fmt.Errorf("group %d does not allow probe protocol %s", due.GroupID, due.Config.Protocol)
	}
	ctx = requeststate.WithClientProtocol(requeststate.WithGroup(ctx, group), due.Config.Protocol)
	ctx = routing.WithModelAttributes(ctx, s.Attributes)
	if due.Config.Protocol == capability.ProtocolImagesGenerations {
		ctx = context.WithValue(ctx, imageModelRequiredKey{}, true)
		ctx = requeststate.WithOpenAIImagesEndpoint(requeststate.WithOpenAIImageGenerationIntent(ctx))
	}
	plan := gatewayprovider.NewRoutePlanner(policies).PlanRoute(ctx, group, &due.GroupID, model)
	ctx = requeststate.WithRoutePlan(ctx, plan)
	excluded := map[int64]struct{}{}
	for {
		if err := ctx.Err(); err != nil {
			return routing.GroupProbeTarget{}, err
		}
		selected, err := selectProvider(ctx, &due.GroupID, "", model, excluded)
		if err != nil {
			return routing.GroupProbeTarget{}, fmt.Errorf("no testable provider for probe protocol %s: %w", due.Config.Protocol, err)
		}
		candidate, ok := plan.ResolveCandidate(gatewayprovider.ExecutionSnapshot(selected))
		route := provider.TestProtocolRoute{ClientProtocol: candidate.ClientProtocol, UpstreamProtocol: candidate.UpstreamProtocol}
		if ok && route.SupportsProvider(gatewayprovider.ExecutionSnapshot(selected)) {
			return routing.GroupProbeTarget{
				Attributes: routing.ModelAttributesFromContext(ctx),
				GroupID:    due.GroupID, ProviderID: selected.Record.ID,
				ModelID: plan.Models().GroupMappedModel, ProtocolRoute: route,
			}, nil
		}
		// Gateway adapters cover more routes than provider tests. Exclude this
		// candidate and reuse scheduling without changing its resolved protocol.
		excluded[selected.Record.ID] = struct{}{}
	}
}

// selectProbeProvider prefers compatible selection and otherwise uses the
// generic selector for the due group's model. It supplies an empty session hash
// and reports an error when neither selector is configured.
func (s Probe) selectProbeProvider(ctx context.Context, due routing.GroupAvailabilityProbeDueGroup, modelID string) (*gatewayprovider.ExecutionProvider, error) {
	groupID := due.GroupID
	if s.openAIGateway != nil {
		return s.openAIGateway.SelectProviderForModel(ctx, &groupID, "", modelID)
	}
	// The generic selector supplies probe targets when compatible selection is
	// unavailable; an empty session hash lets selection choose by current eligibility.
	if s.gatewaySvc != nil {
		return s.gatewaySvc.SelectProviderForModel(ctx, &groupID, "", modelID)
	}
	return nil, fmt.Errorf("provider selector not configured")
}
