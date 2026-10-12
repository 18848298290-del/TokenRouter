package selection

import (
	"context"
	"fmt"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Probe selects a group provider and tests the group-mapped model.
type Probe struct {
	ProviderTest    ProviderTester
	gatewaySvc      *Generic
	openAIGateway   *Compatible
	geminiCompatSvc *Gemini
}

// ProviderTester runs a test against a selected provider.
type ProviderTester interface {
	RunTestBackgroundWithPromptAndUserAgent(context.Context, int64, string, string, string) (*provider.ScheduledTestResult, error)
}

// NewProbe binds platform selectors and the provider tester.
func NewProbe(test ProviderTester, generic *Generic, compatible *Compatible, gemini *Gemini) Probe {
	return Probe{ProviderTest: test, gatewaySvc: generic, openAIGateway: compatible, geminiCompatSvc: gemini}
}

func (s Probe) Select(ctx context.Context, due routing.GroupAvailabilityProbeDueGroup, model string) (routing.GroupProbeTarget, error) {
	provider, err := s.selectProbeProvider(ctx, due, model)
	if err != nil {
		return routing.GroupProbeTarget{}, err
	}
	// Selection checks the requested model; the probe receives the group-mapped model for subsequent provider mapping.
	routingModel := model
	if s.openAIGateway != nil {
		routingModel = s.openAIGateway.resolveGroupRoutingModel(ctx, &due.GroupID, model)
	} else if s.gatewaySvc != nil {
		routingModel = s.gatewaySvc.groupMappedModelForGroup(ctx, &due.GroupID, model)
	}
	return routing.GroupProbeTarget{ProviderID: provider.Record.ID, ModelID: routingModel}, nil
}

func (s Probe) Test(ctx context.Context, id int64, model, prompt, userAgent string) (*routing.ProbeExecutionResult, error) {
	result, err := s.ProviderTest.RunTestBackgroundWithPromptAndUserAgent(ctx, id, model, prompt, userAgent)
	if result == nil {
		return nil, err
	}
	return &routing.ProbeExecutionResult{Status: result.Status, LatencyMs: result.LatencyMs, ErrorMessage: result.ErrorMessage, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt}, err
}

func (s Probe) selectProbeProvider(ctx context.Context, due routing.GroupAvailabilityProbeDueGroup, modelID string) (*gatewayprovider.ExecutionProvider, error) {
	groupID := due.GroupID
	if s.openAIGateway != nil {
		return s.openAIGateway.SelectProviderForModel(ctx, &groupID, "", modelID)
	}
	if s.gatewaySvc != nil {
		return s.gatewaySvc.SelectProviderForModel(ctx, &groupID, "", modelID)
	}
	return nil, fmt.Errorf("provider selector not configured")
}
