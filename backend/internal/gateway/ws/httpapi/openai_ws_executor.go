package httpapi

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	openaiws "github.com/TokenFlux/TokenRouter/internal/upstream/openai/ws"
)

// OpenAIWSOptions contains static execution parameters; a nil Options pointer selects defaults.
type OpenAIWSOptions = gatewayws.Parameters

// OpenAIWSSelection resolves transport and session TTL for an already selected provider.
type OpenAIWSSelection interface {
	ResolveTransport(*gatewayadapter.ExecutionProvider) egress.OpenAIWSProtocolDecision
	SessionStickyTTL() time.Duration
}

// OpenAIWSDependencies supplies WebSocket execution dependencies.
type OpenAIWSDependencies struct {
	Options     *OpenAIWSOptions
	Runtime     *gatewayws.Runtime
	Connections *openaiws.OpenAIWSConnections
	Requests    *gatewayhttp.OpenAIRequests
	Output      *gatewayhttp.OpenAIResponseOutput
	Grok        *gatewayhttp.GrokExecutor
	FastPolicy  *gatewayadapter.ExecutionFastPolicy
	Prompts     *promptpolicy.Service
	Selection   OpenAIWSSelection
	State       session.OpenAIWSStateStore
	Lineage     *gatewayhttp.OpenAIEncryptedLineage
	ImageBridge *gatewayadapter.ResponseImagePolicy
	Cache       session.GatewayCache
}

// OpenAIWebSocketExecutor adapts frames, credentials, health, and HTTP bridging for the gateway/ws turn loop.
type OpenAIWebSocketExecutor struct {
	OpenAIWSDependencies
	openaiWSSessionPreemptions openAIWSSessionPreemptRegistry
}

// NewOpenAIWebSocketExecutor constructs an executor without starting connections or session tasks.
func NewOpenAIWebSocketExecutor(deps OpenAIWSDependencies) *OpenAIWebSocketExecutor {
	out := &OpenAIWebSocketExecutor{OpenAIWSDependencies: deps}

	return out
}

// EnsureSessionIsolation checks session ownership using the request's authenticated Key.
func (s *OpenAIWebSocketExecutor) EnsureSessionIsolation(ctx context.Context, key *apikey.APIKey, userID int64, source, hash string) error {
	if key == nil {
		return nil
	}
	var group int64
	if key.GroupID != nil {
		group = *key.GroupID
	}
	return session.EnsureIsolation(ctx, s.Cache, session.IsolationInput{UserID: userID, GroupID: group, Source: source, Hash: hash, TTL: time.Hour, Enabled: key.Group != nil && key.Group.SessionIsolationEnabled})
}

// forTurn freezes runtime parameters for this turn; later turns can read newer snapshots.
func (s *OpenAIWebSocketExecutor) forTurn() *OpenAIWebSocketExecutor {
	if s.Runtime == nil {
		return s
	}
	deps := s.OpenAIWSDependencies
	value := s.Runtime.Snapshot()
	deps.Options = &value
	return &OpenAIWebSocketExecutor{OpenAIWSDependencies: deps}
}
