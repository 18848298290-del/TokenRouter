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

// openai_live_create_adapter.go binds Live creation to provider admission,
// model tracing and session observation.
// Live creation first selects an ordinary admitted provider, then qualifies
// its model and client policy before obtaining the separate session lease.
// The creation target retains its routed TLS match after client checks, so
// the subsequent upstream request uses the same creation-time identity
// policy.
//
// Model tracing keeps the source client name, group route and provider
// execution name available when Creator saves the call. Ordinary provider
// cleanup is handed to Creator with the candidate; saved session observation
// is requested only after persistence succeeds. The shared application
// background callback controls scheduling admission during shutdown. This
// adapter ignores its acceptance result, so a rejected initial observer leaves
// the already created response in place.

// liveCreatePorts binds provider admission, model tracing and observation for
// Live creation.
type liveCreatePorts struct {
	service *OpenAILiveExecutor // shared Live execution dependencies
}

// liveCreateTarget retains one admitted provider and client routing state for
// session creation.
type liveCreateTarget struct {
	service  *OpenAILiveExecutor                // shared Live execution dependencies
	provider *gatewayprovider.ExecutionProvider // retained admitted provider target
	// groupID retains the creation identity's selected-group pointer. Model
	// qualification reads it before upstream creation, so callers keep it stable.
	groupID *int64
	// router is installed by AllowsClient using creation-time User-Agent. Create
	// reuses it for the request identity and TLS profile after policy acceptance.
	router egress.TLSFingerprintRouterMatchResult
}

// PrepareAttestation returns the paired plaintext header and protected saved
// representation from the executor. Generation or encryption errors retain
// their typed unavailable classification, allowing Creator to stop before
// selecting an upstream provider.
func (p *liveCreatePorts) PrepareAttestation(ctx context.Context) (string, string, error) {
	return p.service.prepareLiveAttestation(ctx)
}

// Select requests ordinary provider admission with Live capability and a
// fresh selection hash. A selection error or nil result returns early;
// otherwise acquisition and cleanup are passed to Creator, with a target only
// when a provider is present.
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

// TraceModels registers group routing and provider upstream names in the
// request model trace.
func (p *liveCreatePorts) TraceModels(ctx context.Context, routing, upstream string) {
	modeltrace.RegisterStage(ctx, routing)
	modeltrace.RegisterStage(ctx, upstream)
}

// ModelTrace returns the source client name and group-derived mapping chain
// for saved metadata. A nonblank request trace client name overrides the
// supplied model, while current group planning contributes the route used
// with its upstream name.
func (p *liveCreatePorts) ModelTrace(ctx context.Context, groupID *int64, model, upstream string) (string, string) {
	requested := model
	if trace, ok := modeltrace.FromContext(ctx); ok && strings.TrimSpace(trace.ClientModel) != "" {
		requested = trace.ClientModel
	}
	plan := p.service.Routes.PlanRoute(ctx, nil, groupID, model)
	mapping := plan.Mapping()
	return requested, mapping.BuildModelMappingChain(model, upstream)
}

// NewLeaseID generates the concurrency ownership identifier for the upcoming
// Live session.
func (p *liveCreatePorts) NewLeaseID() string { return scheduler.GenerateRequestID() }

// ShouldFailover delegates failed creation recovery to the executor's OpenAI
// classification policy. Creator releases the failed Live lease before using
// this result, so permitted retry can select another candidate with fresh
// admission.
func (p *liveCreatePorts) ShouldFailover(err error) bool {
	return p.service.shouldFailoverLiveCreateError(err)
}

// Observe requests background monitoring after Creator has saved a successful
// call. The application callback's acceptance flag is ignored here; a
// rejected schedule leaves the created session response unchanged and starts
// no initial observer.
func (p *liveCreatePorts) Observe(record *session.LiveCallRecord) {
	// Persistence already succeeded before this callback. Background may reject
	// admission during shutdown; this adapter ignores that result for the created call.
	p.service.Background("service/openai_live.go:CreateLiveCall", func() {
		p.service.observeLiveCall(record)
	})
}

// ResolveModel qualifies the supplied model for this admitted provider and
// retained group. Errors stop creation before provider mapping; success
// returns the routed spelling beside the provider's separately resolved
// upstream model.
func (t *liveCreateTarget) ResolveModel(ctx context.Context, model string) (string, string, error) {
	routing, err := t.service.Selection.ResolveOpenAIWSRoutingModelForProvider(ctx, t.groupID, t.provider, model, providercore.OpenAIEndpointCapabilityLive)
	if err != nil {
		return "", "", err
	}
	return routing, gatewayprovider.ExecutionModelPolicy(t.provider).OpenAIUpstream(routing, false), nil
}

// AllowsClient stores the TLS match derived from captured client User-Agent
// and checks provider client policy. Enabled unmatched policy rejects the
// candidate and logs its reason; call this before Create so transport uses
// that retained match.
func (t *liveCreateTarget) AllowsClient(ctx context.Context, identity session.LiveCallIdentity) bool {
	t.router = t.service.matchLiveTLSFingerprintRouter(t.provider, identity.UserAgent)
	result := t.service.liveClientPolicyResult(ctx, t.provider, identity, t.router)
	if result.Enabled && !result.Matched {
		logging.FromContext(ctx).Warn("OpenAI Live client policy rejected candidate provider", zap.Int64("provider_id", t.provider.Record.ID), zap.String("policy", result.Policy), zap.String("reason", result.Reason))
		return false
	}
	return true
}

// Create opens one upstream call using the client-policy match retained by
// AllowsClient. Callers supply already resolved session settings and
// plaintext attestation; Creator adds the provider ID after receiving the
// public SDP and call metadata.
func (t *liveCreateTarget) Create(ctx context.Context, request *session.LiveCallRequest, attestation string) (*gatewaylive.Created, error) {
	created, err := t.service.createUpstreamLiveCall(ctx, t.provider, request, attestation, t.router)
	if err != nil {
		return nil, err
	}
	return &gatewaylive.Created{SDP: created.SDP, CallID: created.CallID, Location: created.Location}, nil
}
