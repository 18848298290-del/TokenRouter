package ws

import (
	"context"
	"time"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// OpenAIIngressHooks defines per-turn lifecycle callbacks for inbound WebSocket sessions.
type OpenAIIngressHooks struct {
	ContextForTurn func(int) context.Context
	// ClientLifecycleContext is the client context before ingress lease cancellation is attached.
	// Writes inherit client and shutdown cancellation, letting lease loss close after the current frame finishes.
	ClientLifecycleContext context.Context
	// InitialRequestModel is the first frame's model before group mapping, used for session defaults and policy matching.
	InitialRequestModel string
	// InitialTurnStartedAt records when the first response.create was accepted.
	InitialTurnStartedAt time.Time
	// MaxReasoningEffort caps the reasoning effort requested within the session.
	MaxReasoningEffort string
	// MaxReasoningEffortOverLimit chooses downgrade or rejection when the requested effort exceeds the cap.
	MaxReasoningEffortOverLimit string
	// ReasoningEffortMappings rewrites requested effort within the session.
	ReasoningEffortMappings []routing.ReasoningEffortMapping
	// ResolveRoutingModel maps client model R to group model G before provider mapping each turn.
	// The payload supplies capability intent; an error prevents forwarding this frame upstream.
	ResolveRoutingModel func(turn int, requestedModel string, payload []byte) (string, error)
	// ResolveFastModePolicy reads the API Key's current Fast policy each turn.
	ResolveFastModePolicy func(turn int) string
	// TurnStarted reports acceptance time for each response.create, captured before policy processing.
	TurnStarted   func(turn int, startedAt time.Time)
	BeforeTurn    func(turn int) error
	BeforeRequest func(turn int, payload []byte, originalModel, previousResponseID string) ([]byte, error)
	// OnUpstreamError observes error and failed events for signals such as OpenAI cyber policy.
	OnUpstreamError func(turn int, originalModel string, statusCode int, responseBody []byte, message string)
	AfterTurn       func(capture OpenAITurnCapture)
}

// OpenAITurnCapture contains completed-turn data for usage settlement and error handling.
type OpenAITurnCapture struct {
	Turn               int
	StartedAt          time.Time
	RequestBody        []byte
	OriginalModel      string
	PreviousResponseID string
	Result             *forwardcore.OpenAIResult
	Err                error
	PayloadSource      string
}
