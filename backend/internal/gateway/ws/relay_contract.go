package ws

import (
	"context"
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Bidirectional relay reports per-turn output before a terminal client write
// finishes, while connection exit can include an unfinished turn. Relay data
// carries accepted timing, completion observations, and paired write callbacks
// separately so passthrough can hold admission closed until terminal delivery
// succeeds.

// RelayInput supplies connection transports, initial request bytes, and
// callbacks to upstream relay.
type RelayInput struct {
	Ctx context.Context // connection execution control context
	// ClientConn and UpstreamConn support concurrent opposite-direction reads and
	// writes. RunRelay owns relay cancellation; the gateway session retains final
	// client close presentation and upstream cleanup.
	ClientConn   FrameConn // filtered client frame connection
	UpstreamConn FrameConn // deadline-controlled upstream frame connection
	// FirstClientMessage may already have been sent by the session.
	// Options.FirstMessageSent prevents the relay from sending those bytes a
	// second time.
	FirstClientMessage []byte       // normalized first request bytes
	Options            RelayOptions // relay callback and timeout settings
}

// RelayResult retains aggregate observations returned by the relay controller.
// Any started upstream reader is joined before final settlement.
type RelayResult struct {
	RequestModel          string            // relay retained request model
	ResponseServiceTier   string            // terminal upstream service tier
	UpstreamResponseModel string            // observed upstream response model
	Usage                 wire.ForwardUsage // aggregate observed relay usage
	RequestID             string            // relay selected response identifier
	TerminalEventType     string            // relay retained terminal label
	// FirstTokenMs follows the upstream relay's token-label predicate and remains
	// nil when no eligible nonnegative timing is recorded. It is independent of
	// pooled StreamPort.IsToken.
	FirstTokenMs *int          // optional first-token elapsed milliseconds
	Duration     time.Duration // elapsed connection relay duration
	// ClientToUpstreamFrames and UpstreamToClientFrames count forwarded frames by
	// direction. DroppedDownstreamFrames counts upstream frames drained after
	// downstream delivery is suppressed; those frames still contribute
	// observations.
	ClientToUpstreamFrames  int64 // forwarded client frame count
	UpstreamToClientFrames  int64 // forwarded upstream frame count
	DroppedDownstreamFrames int64 // drained undispatched output count
}

// RelayTurnResult retains one response turn's usage, terminal metadata, and
// observed timing.
type RelayTurnResult struct {
	RequestModel          string            // current relay request model
	ResponseServiceTier   string            // current terminal response tier
	UpstreamResponseModel string            // current observed response model
	Usage                 wire.ForwardUsage // current turn usage snapshot
	RequestID             string            // current response correlation identifier
	TerminalEventType     string            // current terminal event label
	// StartedAt, Duration, and FirstTokenMs describe the same accepted turn. The
	// gateway can supply acceptance time before filtering; the relay uses its
	// clock when no captured time is available.
	StartedAt    time.Time     // accepted turn start time
	Duration     time.Duration // elapsed current turn duration
	FirstTokenMs *int          // optional first-token elapsed milliseconds
}

// RelayExit identifies the stage, cause, and downstream state of a connection
// exit.
type RelayExit struct {
	Stage    string // connection exit operation label
	Err      error  // underlying relay exit cause
	Graceful bool   // relay classified clean exit
	// WroteDownstream reports successful downstream output across this relay
	// session. Passthrough permits first-turn failover only when that flag is
	// false and completed-turn count is zero.
	WroteDownstream bool // session downstream output observed
}

// RelayOptions supplies bidirectional timing, observation, and client-write
// lifecycle callbacks.
type RelayOptions struct {
	WriteTimeout time.Duration // initial frame write budget
	// WriteTimeoutForTurn optionally selects the current turn's updated write
	// budget. When absent, the upstream relay uses WriteTimeout for each
	// synchronous frame send.
	WriteTimeoutForTurn func() time.Duration // current accepted turn write reader
	// IdleTimeout applies a connection-wide activity watchdog when positive.
	// PassthroughSession sets zero and uses separate client inter-turn and
	// upstream active-read timing instead.
	IdleTimeout          time.Duration // relay-wide idle watchdog budget
	UpstreamDrainTimeout time.Duration // disconnected client drain budget
	// FirstTurnStartedAt and TakeNextTurnStartedAt supply captured client
	// acceptance times. Zero or unavailable later values use the relay clock; the
	// callback consumes each accepted timestamp once.
	FirstTurnStartedAt    time.Time        // first frame acceptance time
	TakeNextTurnStartedAt func() time.Time // consume later acceptance timestamp
	FirstMessageType      int              // first client message type
	// FirstMessageSent tells the relay whether the initial request already reached
	// upstream. Passthrough writes it through DeadlineConn first so its
	// first-output budget is armed before relay begins.
	FirstMessageSent bool // first request already forwarded
	// StartClientAfterFirstDownstream starts later-frame reading only after the
	// first successful downstream write. This keeps the first turn's response
	// setup ahead of further client requests.
	StartClientAfterFirstDownstream bool                                    // delayed client reader enabled
	OnUsageParseFailure             func(eventType string, usageRaw string) // upstream usage decoding observer
	// OnUpstreamEvent receives a copied text payload after its event type is
	// parsed. Risk observation runs before client model and tool restoration.
	OnUpstreamEvent func(eventType string, payload []byte) // upstream event observation callback
	// OnTurnComplete reports observed terminal completion before terminal client
	// delivery. Passthrough saves its dequeued TurnPayload for that write and
	// opens later admission only after the paired write succeeds.
	OnTurnComplete func(turn RelayTurnResult) // per-turn completion observation callback
	// BeforeWriteClient can return a provider-policy failure before output is
	// delivered. Its wroteDownstream input reports prior successful session
	// delivery for pre-output failover decisions.
	BeforeWriteClient func(msgType int, payload []byte, wroteDownstream bool) error // pre-delivery provider policy callback
	// BeforeClientWrite and AfterClientWrite bracket the synchronous client send,
	// including a failed send. Passthrough pairs terminal calls to hold its
	// admission mutex until delivery outcome is known.
	BeforeClientWrite func(msgType int, payload []byte)                 // synchronous write preparation callback
	AfterClientWrite  func(msgType int, payload []byte, writeErr error) // synchronous write outcome callback
	// BeforeRelayCancel observes the first exit before internal relay
	// cancellation. The gateway can attempt a close handshake while its client
	// reader is still available.
	BeforeRelayCancel func(exit RelayExit) // first-exit close preparation callback
	// ReadClientFrame optionally replaces ordinary client reading. Passthrough
	// uses it to forward non-response.create events while waiting for the next
	// accepted turn request.
	ReadClientFrame func(ctx context.Context, clientConn FrameConn) (int, []byte, error) // custom next client frame reader
	// OnTrace can run from transport workers and the relay controller. Supplied
	// diagnostic callbacks must support concurrent invocation and avoid retaining
	// mutable per-worker payload storage.
	OnTrace func(event RelayTraceEvent) // relay diagnostic event callback
	Now     func() time.Time            // optional relay clock source
}

// RelayTraceEvent records one transport or controller stage for relay
// diagnostics.
type RelayTraceEvent struct {
	Stage           string // observed relay operation label
	Direction       string // observed relay traffic direction
	MessageType     string // WebSocket frame type label
	PayloadBytes    int    // observed frame byte count
	Graceful        bool   // observed clean exit classification
	WroteDownstream bool   // successful session delivery observed
	Error           string // diagnostic error message text
}
