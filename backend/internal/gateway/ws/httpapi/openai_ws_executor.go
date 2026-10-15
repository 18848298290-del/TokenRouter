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

// openai_ws_executor.go constructs the Responses WebSocket executor and
// captures per-turn parameter snapshots. Runtime updates affect later turns;
// the executor retains the injected services so HTTP bridging and pooled or
// dedicated WS execution observe the same provider and session data.

// OpenAIWSOptions names the turn parameters consumed by WebSocket execution.
// A dependency with nil Options uses each adapter's documented defaults unless
// Runtime supplies a snapshot before the turn begins.
type OpenAIWSOptions = gatewayws.Parameters

// OpenAIWSSelection resolves transport and session TTL for an already selected provider.
type OpenAIWSSelection interface {
	// ResolveTransport reads the selected provider's configured upstream transport.
	// A nil provider produces the egress HTTP default; callers still apply group
	// conversion rules before executing a WS turn through HTTP.
	ResolveTransport(*gatewayadapter.ExecutionProvider) egress.OpenAIWSProtocolDecision
	// SessionStickyTTL returns the lifetime used when writing session affinity.
	// The bound selector supplies a positive configured duration or its one-hour
	// default; response-ID affinity has a separate executor setting.
	SessionStickyTTL() time.Duration
}

// OpenAIWSDependencies supplies shared service references and optional settings
// for WebSocket turn execution. The constructor retains these references; app
// owns service initialization and connection-resource shutdown. forTurn copies
// this container while replacing only Options with a value snapshot.
type OpenAIWSDependencies struct {
	// Options supplies static turn settings when Runtime is absent. Adapter
	// accessors choose their own defaults for a nil pointer or unsupported values.
	Options *OpenAIWSOptions
	// Runtime publishes immutable settings snapshots. forTurn captures Options
	// from it while retaining the other dependency references for the current turn.
	Runtime     *gatewayws.Runtime
	Connections *openaiws.OpenAIWSConnections       // shared upstream connection resources
	Requests    *gatewayhttp.OpenAIRequests         // shared authenticated request builder
	Output      *gatewayhttp.OpenAIResponseOutput   // shared response policy services
	Grok        *gatewayhttp.GrokExecutor           // Grok HTTP bridge executor
	FastPolicy  *gatewayadapter.ExecutionFastPolicy // service tier policy evaluator
	Prompts     *promptpolicy.Service               // user prompt replacement service
	Selection   OpenAIWSSelection                   // transport and session selector
	State       session.OpenAIWSStateStore          // optional response affinity storage
	Lineage     *gatewayhttp.OpenAIEncryptedLineage // rejected ciphertext session tracking
	ImageBridge *gatewayadapter.ResponseImagePolicy // Codex image injection policy
	Cache       session.GatewayCache                // optional session isolation cache
}

// OpenAIWebSocketExecutor adapts frames, credentials, health, and HTTP bridging
// for the gateway/ws turn loop.
type OpenAIWebSocketExecutor struct {
	OpenAIWSDependencies // injected shared execution services
	// openaiWSSessionPreemptions belongs to this executor instance. forTurn makes
	// a fresh executor whose registry is unused by the already armed session.
	openaiWSSessionPreemptions openAIWSSessionPreemptRegistry
}

// NewOpenAIWebSocketExecutor retains the supplied service references in a new
// executor. Construction starts no connection workers or preemption watches;
// the owner supplies required dependencies before invoking session execution.
func NewOpenAIWebSocketExecutor(deps OpenAIWSDependencies) *OpenAIWebSocketExecutor {
	out := &OpenAIWebSocketExecutor{OpenAIWSDependencies: deps}

	return out
}

// EnsureSessionIsolation records or checks the user's first group for the
// source and session hash. A nil Key skips the check; otherwise group absence
// uses zero and the target group's setting controls rejection. Cache
// operations use a one-hour ownership lifetime and propagate their errors.
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

// forTurn captures current Runtime parameters for one execution turn. With no
// Runtime it returns the receiver; otherwise it returns a new executor sharing
// injected services and using an independent Options value. The session's
// existing preemption registration remains on its original owner.
func (s *OpenAIWebSocketExecutor) forTurn() *OpenAIWebSocketExecutor {
	if s.Runtime == nil {
		return s
	}
	deps := s.OpenAIWSDependencies
	value := s.Runtime.Snapshot()
	deps.Options = &value
	return &OpenAIWebSocketExecutor{OpenAIWSDependencies: deps}
}
