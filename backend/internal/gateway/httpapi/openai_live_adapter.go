package httpapi

import (
	"context"

	coderws "github.com/coder/websocket"

	gatewaylive "github.com/TokenFlux/TokenRouter/internal/gateway/live"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Live runtime receives frame interfaces while provider credentials and
// encrypted attestation handling remain in the HTTP execution adapter. A
// selected saved-call target loads current provider capability and converts
// normal upstream close into the runtime's terminal-call sentinel. Downstream
// control transport retains its HTTP owner.
//
// Finalization writes a zero-cost usage row keyed separately from the
// upstream call ID. Key owner and funding user are kept distinct, with legacy
// actor absence using the funding user for the row's actor. Model trace and
// creation metadata come from the retained call record, while a positive
// subscription ID chooses its billing label. These records supplement usage
// analysis directly; this adapter does not invoke the ordinary funds
// settlement path.

// livePorts supplies shared Live services and final usage recording to
// runtime operations.
type livePorts struct {
	service *OpenAILiveExecutor // shared Live execution dependencies
}

// liveTarget retains one saved call and its currently qualified provider for
// control operations.
type liveTarget struct {
	service *OpenAILiveExecutor // shared Live execution dependencies
	// record and provider describe one looked-up call and its qualified target.
	// Dial uses saved identity/attestation, while Rewrite uses saved Key rules and
	// this provider's current model policy for the same control operation.
	record   *session.LiveCallRecord            // retained saved call metadata
	provider *gatewayprovider.ExecutionProvider // current qualified provider target
}

// liveUpstreamFrames adapts native frame types and terminal close errors for
// Live control.
type liveUpstreamFrames struct {
	// LiveFrameConn remains owned by the runtime that successfully dialed it.
	// Controller and observer paths close it after their respective frame loops return.
	openai.LiveFrameConn
}

// liveDownstreamFrames adapts the HTTP-owned client WebSocket to runtime
// frame operations.
type liveDownstreamFrames struct {
	// conn remains HTTP-owned through runtime proxying. The HTTP entry presents
	// any final close frame and runs immediate teardown as its request exits.
	conn *coderws.Conn
}

// liveModelResolver qualifies and maps models for one saved-call provider.
type liveModelResolver struct {
	service  *OpenAILiveExecutor                // shared Live execution dependencies
	provider *gatewayprovider.ExecutionProvider // retained qualified provider target
}

// Store returns the bound call store or the runtime's unavailable error. A
// missing executor/store prevents Live state operations, while a usable
// returned service remains owned by the application.
func (p livePorts) Store() (session.LiveCallStore, error) { return p.service.liveStore() }

// Leases returns shared persistent Live capacity accounting or the runtime's
// unavailable error. Creation needs that service before opening an upstream
// session, and the application retains its lifetime through controller
// completion.
func (p livePorts) Leases() (scheduler.LiveConcurrencyCache, error) {
	return p.service.liveConcurrencyCache()
}

// BeginObserver registers the supplied owner through shared observer state and
// returns its cancellation context with cleanup. Callers supply a unique active
// owner and call accepted cleanup exactly once; stopping rejects registration
// before another observer starts.
func (p livePorts) BeginObserver(owner string) (context.Context, func(), bool) {
	return p.service.beginLiveObserver(owner)
}

// Target loads and qualifies the provider saved in the call record before
// constructing control operations. A usable record and executor are required;
// repository and capability errors return before any WebSocket dialing.
func (p livePorts) Target(ctx context.Context, record *session.LiveCallRecord) (gatewaylive.Target, error) {
	provider, err := p.service.liveSidebandProvider(ctx, record)
	if err != nil {
		return nil, err
	}
	return liveTarget{service: p.service, record: record, provider: provider}, nil
}

// RecordZeroUsage writes a zero-cost Live analysis row with the supplied
// millisecond duration. A missing recorder skips writing; when bound, callers
// provide a usable saved record. Positive actor identity is retained, while
// absent legacy actor data uses the funding user.
//
// Subscription presence selects the billing label, and the hashed call
// identity supplies a stable financial key and separate request ID. Model and
// endpoint observations are copied from creation. WriteUsage runs with
// Background, which avoids inheriting controller cancellation and can retry
// rejected best-effort analysis insertion synchronously. Token and cost
// fields remain their zero values; this path records analysis without
// calculating or submitting funds.
func (p livePorts) RecordZeroUsage(ctx context.Context, record *session.LiveCallRecord, duration int) {
	if p.service.Usage == nil {
		return
	}
	inboundEndpoint := record.InboundEndpoint
	upstreamEndpoint := "/backend-api/codex/realtime/calls"
	userAgent := record.UserAgent
	ipAddress := record.IPAddress
	billingType := usage.BillingTypeBalance
	if record.SubscriptionID > 0 {
		billingType = usage.BillingTypeSubscription
	}
	actorUserID := record.ActorUserID
	if actorUserID <= 0 {
		actorUserID = record.UserID
	}
	// Record analysis under a detached context so controller cancellation does not
	// discard the row. WriteUsage can retry best-effort insertion with synchronous
	// Create; zero-cost fields bypass the normal funds calculation and settlement.
	// TODO(billing): Add duration or token pricing through shared settlement with
	// balance and subscription coverage when Live charging is implemented.
	p.service.Usage.WriteUsage(context.Background(), &usage.UsageLog{
		UserID:            actorUserID,
		BillingUserID:     record.UserID,
		TeamID:            liveOptionalID(record.TeamID),
		APIKeyID:          record.APIKeyID,
		ProviderID:        record.ProviderID,
		RequestID:         telemetry.StableRequestID("live/" + record.CallHash),
		BillingKey:        record.CallHash,
		Model:             record.Model,
		RequestedModel:    requeststate.FirstNonEmpty(record.RequestedModel, record.Model),
		UpstreamModel:     liveOptionalString(record.UpstreamModel),
		ModelMappingChain: liveOptionalString(record.ModelMappingChain),
		GroupID:           liveOptionalID(record.GroupID),
		SubscriptionID:    liveOptionalID(record.SubscriptionID),
		RateMultiplier:    1,
		BillingType:       billingType,
		RequestType:       usage.RequestTypeLive,
		DurationMs:        &duration,
		UserAgent:         &userAgent,
		IPAddress:         &ipAddress,
		InboundEndpoint:   &inboundEndpoint,
		UpstreamEndpoint:  &upstreamEndpoint,
		CreatedAt:         record.CreatedAt,
	}, "service.openai_live")
}

// Dial opens the qualified saved provider's control connection and wraps its
// native frames. The runtime closes successful upstream connections; errors
// retain the native credential, transport or capability failure for
// controller recovery.
func (t liveTarget) Dial(ctx context.Context) (gatewaylive.FrameConn, error) {
	conn, err := t.service.dialLiveSidebandForProvider(ctx, t.record, t.provider)
	if err != nil {
		return nil, err
	}
	return liveUpstreamFrames{conn}, nil
}

// Rewrite applies saved Key redirects with current group/provider
// qualification to a client payload. The returned client name and internal
// names support response restoration; mapping errors retain the original
// input through the Live rewriter.
func (t liveTarget) Rewrite(ctx context.Context, payload []byte) ([]byte, string, []string, error) {
	return t.service.rewriteLiveSidebandClientPayload(ctx, t.record, t.provider, payload)
}

// ReadFrame returns a native upstream frame with its integer runtime kind. A
// normal close becomes the terminal-call sentinel, while other errors and
// payload observations retain their supplied values for controller handling.
func (c liveUpstreamFrames) ReadFrame(ctx context.Context) (int, []byte, error) {
	typ, body, err := c.LiveFrameConn.ReadFrame(ctx)
	return int(typ), body, liveSidebandReadError(err)
}

// WriteFrame sends the runtime's integer kind through native Live frame
// output.
func (c liveUpstreamFrames) WriteFrame(ctx context.Context, typ int, body []byte) error {
	return c.LiveFrameConn.WriteFrame(ctx, coderws.MessageType(typ), body)
}

// SetReadLimit leaves upstream transport settings unchanged because native
// LiveFrameConn exposes frame read, write and close methods. The runtime
// applies its configured client limit through the separate downstream adapter
// instead.
func (c liveUpstreamFrames) SetReadLimit(limit int64) {}

// ReadFrame returns the client WebSocket payload with its integer runtime
// message kind.
func (c liveDownstreamFrames) ReadFrame(ctx context.Context) (int, []byte, error) {
	typ, body, err := c.conn.Read(ctx)
	return int(typ), body, err
}

// WriteFrame sends a runtime kind and payload through the accepted client
// WebSocket.
func (c liveDownstreamFrames) WriteFrame(ctx context.Context, typ int, body []byte) error {
	return c.conn.Write(ctx, coderws.MessageType(typ), body)
}

// SetReadLimit applies the runtime's supplied byte limit to the client
// WebSocket. The HTTP owner keeps transport cleanup responsibility; this
// method configures reads before the control relay starts exchanging
// payloads.
func (c liveDownstreamFrames) SetReadLimit(limit int64) { c.conn.SetReadLimit(limit) }

// Close immediately tears down the HTTP-owned client WebSocket transport.
func (c liveDownstreamFrames) Close() error { return c.conn.CloseNow() }

// ResolveModel qualifies a request model for the supplied group and retained
// Live provider. A qualification error returns before provider mapping;
// successful routing is returned beside the selected provider's separately
// resolved upstream name.
func (r liveModelResolver) ResolveModel(ctx context.Context, groupID *int64, model string) (string, string, error) {
	routing, err := r.service.Selection.ResolveOpenAIWSRoutingModelForProvider(ctx, groupID, r.provider, model, providercore.OpenAIEndpointCapabilityLive)
	if err != nil {
		return "", "", err
	}
	return routing, gatewayprovider.ExecutionModelPolicy(r.provider).OpenAIUpstream(routing, false), nil
}
