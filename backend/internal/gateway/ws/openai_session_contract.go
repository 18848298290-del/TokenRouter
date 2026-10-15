package ws

import (
	"context"
	"time"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// openai_session_contract.go defines OpenAI turn hooks and completion captures
// shared by HTTP execution adapters.
// HTTP execution adapters retain per-turn request identity while exposing
// OpenAI-specific completion results to their callers. OpenAIIngressHooks
// carries the group policy and callback inputs shared by bridge, pooled, and
// dedicated forwarding; each adapter chooses callback timing and translates its
// captured result into this OpenAITurnCapture shape.

// OpenAIIngressHooks carries HTTP-adapter turn policy, logical request context,
// and lifecycle callbacks.
type OpenAIIngressHooks struct {
	// ContextForTurn optionally attaches the logical turn's telemetry and model
	// attributes to execution. Entry supplies it so provider retries share one
	// child request identity.
	ContextForTurn func(int) context.Context // logical request context resolver
	// ClientLifecycleContext optionally supplies pooled downstream writes with the
	// client lifetime before ingress-lease cancellation is attached. Those writes
	// retain client cancellation and their own timeout while lease-loss handling
	// chooses closure separately.
	ClientLifecycleContext context.Context // client lifetime before lease
	// InitialRequestModel is the entry model before group mapping. It can already
	// reflect API Key redirection and supplies model defaults when the first
	// forwarded frame omits model.
	InitialRequestModel string // initial entry model default
	// InitialTurnStartedAt supplies dedicated passthrough with a first-frame
	// acceptance time when nonzero. Zero lets that path capture its own clock;
	// sequential ingress determines its loop timing separately.
	InitialTurnStartedAt time.Time // first frame acceptance time
	// MaxReasoningEffort, MaxReasoningEffortOverLimit, and ReasoningEffortMappings
	// are passed together to frame reasoning policy. The cap action and mappings
	// are evaluated against each supplied or retained client model.
	MaxReasoningEffort          string                           // group requested effort ceiling
	MaxReasoningEffortOverLimit string                           // group excessive effort action
	ReasoningEffortMappings     []routing.ReasoningEffortMapping // group effort rewrite rules
	// ResolveRoutingModel maps the client identifier to eligible routing model
	// using this turn's payload intent. Its error prevents that frame from being
	// forwarded to the selected provider.
	ResolveRoutingModel func(turn int, requestedModel string, payload []byte) (string, error) // per-turn eligible model resolver
	// ResolveFastModePolicy optionally reads the current per-Key Fast policy for a
	// turn. Entry's binding keeps its last usable value after a refresh failure.
	ResolveFastModePolicy func(turn int) string // current API Key policy reader
	// TurnStarted, BeforeTurn, BeforeRequest, OnUpstreamError, and AfterTurn are
	// optional synchronous callbacks. Execution paths call them at their own
	// accepted-turn, admission, warning, and completion points; local pooled
	// retries skip repeated admission.
	TurnStarted     func(turn int, startedAt time.Time)                                                       // turn acceptance observation callback
	BeforeTurn      func(turn int) error                                                                      // per-turn admission validation callback
	BeforeRequest   func(turn int, payload []byte, originalModel, previousResponseID string) ([]byte, error)  // later payload validation callback
	OnUpstreamError func(turn int, originalModel string, statusCode int, responseBody []byte, message string) // upstream risk warning observer
	// AfterTurn supplies observed result and error with the captured request.
	// Consumers finish capture before mutating shared input or result references
	// used by completion processing.
	AfterTurn func(capture OpenAITurnCapture) // captured turn outcome callback
}

// OpenAITurnCapture associates an HTTP-adapter completion result with one
// accepted WebSocket turn.
type OpenAITurnCapture struct {
	Turn      int       // attempt-local turn sequence number
	StartedAt time.Time // turn pricing reference time
	// RequestBody can share the dedicated relay's queued snapshot. Completion
	// consumers keep those bytes immutable while capturing or retaining the
	// request for settlement.
	RequestBody        []byte // captured accepted request bytes
	OriginalModel      string // client requested model identifier
	PreviousResponseID string // captured continuation response identifier
	// Result and Err describe the observed outcome; a failure can have no result.
	// HTTP adapters translate from gateway capture without promising independent
	// copies of every nested reference.
	Result        *forwardcore.OpenAIResult // available OpenAI execution observations
	Err           error                     // final turn execution failure
	PayloadSource string                    // captured request source label
}
