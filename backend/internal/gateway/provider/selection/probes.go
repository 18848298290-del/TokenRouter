package selection

import (
	"context"
	"fmt"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// probes.go selects availability-probe targets and passes the group-mapped
// model and group identity to provider testing.
// Group availability probes choose an eligible provider using the client model,
// but provider testing accepts the group-mapped identity. Passing a client alias
// directly to that test would skip the group mapping; mapping an intermediate
// name again could follow an unintended second rule.
//
// Selection returns the group, provider, and intermediate group model,
// leaving provider mapping to the testing service. Compatible selection is
// preferred when bound, with generic selection supplying the other path.
// The measured provider-test result is then copied into routing's observation
// value, including populated results that accompany an error.

// Probe selects a group provider and tests the group-mapped model.
type Probe struct {
	ProviderTest    ProviderTester // selected provider testing
	gatewaySvc      *Generic       // generic probe selector
	openAIGateway   *Compatible    // preferred probe selector
	geminiCompatSvc *Gemini        // retained Gemini dependency
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
// provider testing with its associated GroupID and ProviderID.
func (s Probe) Select(ctx context.Context, due routing.GroupAvailabilityProbeDueGroup, model string) (routing.GroupProbeTarget, error) {
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
	result, err := s.ProviderTest.RunTestBackgroundRequest(ctx, provider.TestRequest{GroupID: target.GroupID, ProviderID: target.ProviderID, Model: target.ModelID, Prompt: prompt, UserAgent: userAgent})
	if result == nil {
		return nil, err
	}
	return &routing.ProbeExecutionResult{Status: result.Status, LatencyMs: result.LatencyMs, ErrorMessage: result.ErrorMessage, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt}, err
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
