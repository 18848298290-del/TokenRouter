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

// livePorts supplies dependencies and usage recording for Live requests.
type livePorts struct{ service *OpenAILiveExecutor }

// liveTarget resolves provider credentials and dials a frame connection for Live.
type liveTarget struct {
	service  *OpenAILiveExecutor
	record   *session.LiveCallRecord
	provider *gatewayprovider.ExecutionProvider
}

// liveUpstreamFrames converts frame kinds and normal-close errors. The Live service closes the connection.
type liveUpstreamFrames struct{ openai.LiveFrameConn }

// liveDownstreamFrames adapts an upgraded WebSocket; the HTTP layer owns upgrade and close-frame handling.
type liveDownstreamFrames struct{ conn *coderws.Conn }

// liveModelResolver resolves provider capabilities and routing models for the Live payload rewriter.
type liveModelResolver struct {
	service  *OpenAILiveExecutor
	provider *gatewayprovider.ExecutionProvider
}

func (p livePorts) Store() (session.LiveCallStore, error) { return p.service.liveStore() }
func (p livePorts) Leases() (scheduler.LiveConcurrencyCache, error) {
	return p.service.liveConcurrencyCache()
}

func (p livePorts) BeginObserver(owner string) (context.Context, func(), bool) {
	return p.service.beginLiveObserver(owner)
}

func (p livePorts) Target(ctx context.Context, record *session.LiveCallRecord) (gatewaylive.Target, error) {
	provider, err := p.service.liveSidebandProvider(ctx, record)
	if err != nil {
		return nil, err
	}
	return liveTarget{service: p.service, record: record, provider: provider}, nil
}

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
	// TODO(billing): Live records zero-cost usage. Duration or token charging requires integration
	// with shared settlement and regression coverage for balance and subscription funding.
	// If best-effort usage insertion fails, WriteUsage retries through synchronous Create.
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

func (t liveTarget) Dial(ctx context.Context) (gatewaylive.FrameConn, error) {
	conn, err := t.service.dialLiveSidebandForProvider(ctx, t.record, t.provider)
	if err != nil {
		return nil, err
	}
	return liveUpstreamFrames{conn}, nil
}

func (t liveTarget) Rewrite(ctx context.Context, payload []byte) ([]byte, string, []string, error) {
	return t.service.rewriteLiveSidebandClientPayload(ctx, t.record, t.provider, payload)
}

func (c liveUpstreamFrames) ReadFrame(ctx context.Context) (int, []byte, error) {
	typ, body, err := c.LiveFrameConn.ReadFrame(ctx)
	return int(typ), body, liveSidebandReadError(err)
}

func (c liveUpstreamFrames) WriteFrame(ctx context.Context, typ int, body []byte) error {
	return c.LiveFrameConn.WriteFrame(ctx, coderws.MessageType(typ), body)
}
func (c liveUpstreamFrames) SetReadLimit(limit int64) {}

func (c liveDownstreamFrames) ReadFrame(ctx context.Context) (int, []byte, error) {
	typ, body, err := c.conn.Read(ctx)
	return int(typ), body, err
}

func (c liveDownstreamFrames) WriteFrame(ctx context.Context, typ int, body []byte) error {
	return c.conn.Write(ctx, coderws.MessageType(typ), body)
}
func (c liveDownstreamFrames) SetReadLimit(limit int64) { c.conn.SetReadLimit(limit) }
func (c liveDownstreamFrames) Close() error             { return c.conn.CloseNow() }

func (r liveModelResolver) ResolveModel(ctx context.Context, groupID *int64, model string) (string, string, error) {
	routing, err := r.service.Selection.ResolveOpenAIWSRoutingModelForProvider(ctx, groupID, r.provider, model, providercore.OpenAIEndpointCapabilityLive)
	if err != nil {
		return "", "", err
	}
	return routing, gatewayprovider.ExecutionModelPolicy(r.provider).OpenAIUpstream(routing, false), nil
}
