package ws

import (
	"context"
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// relay_contract.go defines relay callbacks and results used to connect
// terminal observation with client delivery.
// Responses terminal observation and client delivery can finish at different
// points. Immediately settled terminals report their turn before client
// writing, while a bare error can be forwarded before later evidence settles
// its usage. Passthrough combines those observations with separate write
// callbacks to govern admission after a response terminal.

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
	RequestModel string // relay retained request model
	// terminal upstream service tier
	ResponseServiceTier string
	// observed upstream response model
	UpstreamResponseModel string
	// aggregate observed relay usage
	Usage wire.ForwardUsage
	// relay selected response identifier
	RequestID         string
	TerminalEventType string // relay retained terminal label
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
	RequestModel string // initial relay request model
	// current terminal response tier
	ResponseServiceTier string
	// current observed response model
	UpstreamResponseModel string
	Usage                 wire.ForwardUsage // current turn usage snapshot
	// current response correlation identifier
	RequestID         string
	TerminalEventType string // current terminal event label
	// StartedAt, Duration, and FirstTokenMs come from the settled response timing
	// record. Accepted timestamps seed that record when available; otherwise the
	// relay clock supplies its start. An unassociated bare error can leave timing
	// at zero or nil.
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
	// WriteTimeout supplies the fallback budget for synchronous frame sends.
	// Nonpositive values use two minutes; a positive WriteTimeoutForTurn result
	// takes precedence for the current send.
	WriteTimeout time.Duration // fallback frame write budget
	// WriteTimeoutForTurn optionally selects the current turn's updated write
	// budget. When absent, the upstream relay uses WriteTimeout for each
	// synchronous frame send. A nonpositive callback result also uses that
	// fallback budget.
	// current accepted turn write reader
	WriteTimeoutForTurn func() time.Duration
	// IdleTimeout applies a connection-wide activity watchdog when positive.
	// PassthroughSession sets zero and uses separate client inter-turn and
	// upstream active-read timing instead.
	IdleTimeout time.Duration // relay-wide idle watchdog budget
	// UpstreamDrainTimeout bounds the sibling-exit wait after graceful client
	// read closure. Nonpositive values use 1200 milliseconds; final upstream
	// reader joining remains a separate wait.
	UpstreamDrainTimeout time.Duration // disconnected client drain budget
	// FirstTurnStartedAt and TakeNextTurnStartedAt supply captured client
	// acceptance times. Zero or unavailable later values use the relay clock; the
	// callback consumes each accepted timestamp once.
	FirstTurnStartedAt time.Time // first frame acceptance time
	// consume later acceptance timestamp
	TakeNextTurnStartedAt func() time.Time
	FirstMessageType      int // first client message type
	// FirstMessageSent tells the relay whether the initial request already reached
	// upstream. Passthrough writes it through DeadlineConn first so its
	// first-output budget is armed before relay begins.
	FirstMessageSent bool // first request already forwarded
	// StartClientAfterFirstDownstream starts later-frame reading only after the
	// first successful downstream write. This keeps the first turn's response
	// setup ahead of further client requests.
	// delayed client reader enabled
	StartClientAfterFirstDownstream bool
	// upstream usage decoding observer
	OnUsageParseFailure func(eventType string, usageRaw string)
	// OnUpstreamEvent receives a copied text payload after its event type is
	// parsed. Risk observation runs before client model and tool restoration.
	// upstream event observation callback
	OnUpstreamEvent func(eventType string, payload []byte)
	// OnTurnComplete receives a settled terminal observation. Immediate terminals
	// notify before attempted client writing; draining can suppress that write. A
	// bare error may already have been forwarded when a later event, read
	// termination, or final controller settlement after reader join invokes this
	// callback. Passthrough dequeues a turn snapshot here, while paired client
	// write callbacks separately govern response-terminal admission.
	// per-turn completion observation callback
	OnTurnComplete func(turn RelayTurnResult)
	// BeforeWriteClient can return a provider-policy failure before output is
	// delivered. Its wroteDownstream input reports prior successful session
	// delivery for pre-output failover decisions.
	// pre-delivery provider policy callback
	BeforeWriteClient func(msgType int, payload []byte, wroteDownstream bool) error
	// BeforeClientWrite and AfterClientWrite bracket the synchronous client send,
	// including a failed send. Passthrough pairs terminal calls to hold its
	// admission mutex until delivery outcome is known.
	// synchronous write preparation callback
	BeforeClientWrite func(msgType int, payload []byte)
	// synchronous write outcome callback
	AfterClientWrite func(msgType int, payload []byte, writeErr error)
	// BeforeRelayCancel observes the first exit before internal relay
	// cancellation. The gateway can attempt a close handshake while its client
	// reader is still available.
	// first-exit close preparation callback
	BeforeRelayCancel func(exit RelayExit)
	// ReadClientFrame optionally replaces ordinary client reading. Passthrough
	// uses it to forward non-response.create events while waiting for the next
	// accepted turn request.
	// custom next client frame reader
	ReadClientFrame func(ctx context.Context, clientConn FrameConn) (int, []byte, error)
	// OnTrace can run from transport workers and the relay controller. Supplied
	// diagnostic callbacks must support concurrent invocation and avoid retaining
	// mutable per-worker payload storage.
	OnTrace func(event RelayTraceEvent) // relay diagnostic event callback
	// Now supplies the relay clock or defaults to time.Now. Transport workers,
	// watchdog and controller can call it concurrently, so custom clocks support
	// concurrent reads.
	Now func() time.Time // optional relay clock source
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
