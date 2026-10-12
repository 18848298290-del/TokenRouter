package httpapi

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewaylive "github.com/TokenFlux/TokenRouter/internal/gateway/live"
	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// liveCreatePorts supplies selection, model tracing, and single-provider session creation.
type liveCreatePorts struct {
	service *OpenAILiveExecutor
}

// liveCreateTarget holds the selected provider and routing inputs for session creation.
type liveCreateTarget struct {
	service  *OpenAILiveExecutor
	provider *gatewayprovider.ExecutionProvider
	groupID  *int64
	router   egress.TLSFingerprintRouterMatchResult
}

func (p *liveCreatePorts) PrepareAttestation(ctx context.Context) (string, string, error) {
	return p.service.prepareLiveAttestation(ctx)
}

func (p *liveCreatePorts) Select(ctx context.Context, groupID *int64, model string, excluded map[int64]struct{}) (*gatewaylive.Candidate, error) {
	selection, _, err := p.service.Selection.SelectProviderWithSchedulerForCapability(ctx, groupID, "", uuid.NewString(), model, excluded, egress.OpenAIUpstreamTransportHTTPSSE, providercore.OpenAIEndpointCapabilityLive, false, false)
	if err != nil || selection == nil {
		return nil, err
	}
	result := &gatewaylive.Candidate{Acquired: selection.Acquired, ReleaseFunc: selection.ReleaseFunc}
	if selection.Provider != nil {
		result.ID = selection.Provider.Record.ID
		result.Concurrency = selection.Provider.Record.Concurrency
		result.Target = &liveCreateTarget{service: p.service, provider: selection.Provider, groupID: groupID}
	}
	return result, nil
}

func (p *liveCreatePorts) TraceModels(ctx context.Context, routing, upstream string) {
	modeltrace.RegisterStage(ctx, routing)
	modeltrace.RegisterStage(ctx, upstream)
}

func (p *liveCreatePorts) ModelTrace(ctx context.Context, groupID *int64, model, upstream string) (string, string) {
	requested := model
	if trace, ok := modeltrace.FromContext(ctx); ok && strings.TrimSpace(trace.ClientModel) != "" {
		requested = trace.ClientModel
	}
	plan := p.service.Routes.PlanRoute(ctx, nil, groupID, model)
	mapping := plan.Mapping()
	return requested, mapping.BuildModelMappingChain(model, upstream)
}
func (p *liveCreatePorts) NewLeaseID() string { return scheduler.GenerateRequestID() }
func (p *liveCreatePorts) ShouldFailover(err error) bool {
	return p.service.shouldFailoverLiveCreateError(err)
}

func (p *liveCreatePorts) Observe(record *session.LiveCallRecord) {
	p.service.Background("service/openai_live.go:CreateLiveCall", func() {
		p.service.observeLiveCall(record)
	})
}

func (t *liveCreateTarget) ResolveModel(ctx context.Context, model string) (string, string, error) {
	routing, err := t.service.Selection.ResolveOpenAIWSRoutingModelForProvider(ctx, t.groupID, t.provider, model, providercore.OpenAIEndpointCapabilityLive)
	if err != nil {
		return "", "", err
	}
	return routing, gatewayprovider.ExecutionModelPolicy(t.provider).OpenAIUpstream(routing, false), nil
}

func (t *liveCreateTarget) AllowsClient(ctx context.Context, identity session.LiveCallIdentity) bool {
	t.router = t.service.matchLiveTLSFingerprintRouter(t.provider, identity.UserAgent)
	result := t.service.liveClientPolicyResult(ctx, t.provider, identity, t.router)
	if result.Enabled && !result.Matched {
		logging.FromContext(ctx).Warn("OpenAI Live 客户端策略拒绝候选提供商", zap.Int64("provider_id", t.provider.Record.ID), zap.String("policy", result.Policy), zap.String("reason", result.Reason))
		return false
	}
	return true
}

func (t *liveCreateTarget) Create(ctx context.Context, request *session.LiveCallRequest, attestation string) (*gatewaylive.Created, error) {
	created, err := t.service.createUpstreamLiveCall(ctx, t.provider, request, attestation, t.router)
	if err != nil {
		return nil, err
	}
	return &gatewaylive.Created{SDP: created.SDP, CallID: created.CallID, Location: created.Location}, nil
}
