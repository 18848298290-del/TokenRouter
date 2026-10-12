package ws

import (
	"context"
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// RelayInput supplies synchronous frame connections and callbacks for the upstream relay adapter.
type RelayInput struct {
	Ctx                context.Context
	ClientConn         FrameConn
	UpstreamConn       FrameConn
	FirstClientMessage []byte
	Options            RelayOptions
}
type RelayResult struct {
	RequestModel string
	// ResponseServiceTier is the service tier declared by the terminal upstream response.
	ResponseServiceTier     string
	UpstreamResponseModel   string
	Usage                   wire.ForwardUsage
	RequestID               string
	TerminalEventType       string
	FirstTokenMs            *int
	Duration                time.Duration
	ClientToUpstreamFrames  int64
	UpstreamToClientFrames  int64
	DroppedDownstreamFrames int64
}

type RelayTurnResult struct {
	RequestModel          string
	ResponseServiceTier   string
	UpstreamResponseModel string
	Usage                 wire.ForwardUsage
	RequestID             string
	TerminalEventType     string
	StartedAt             time.Time
	Duration              time.Duration
	FirstTokenMs          *int
}

type RelayExit struct {
	Stage           string
	Err             error
	Graceful        bool
	WroteDownstream bool
}

type RelayOptions struct {
	WriteTimeout                    time.Duration
	WriteTimeoutForTurn             func() time.Duration
	IdleTimeout                     time.Duration
	UpstreamDrainTimeout            time.Duration
	FirstTurnStartedAt              time.Time
	TakeNextTurnStartedAt           func() time.Time
	FirstMessageType                int
	FirstMessageSent                bool
	StartClientAfterFirstDownstream bool
	OnUsageParseFailure             func(eventType string, usageRaw string)
	// OnUpstreamEvent runs after parsing each upstream text event type; the gateway selects warning events.
	OnUpstreamEvent   func(eventType string, payload []byte)
	OnTurnComplete    func(turn RelayTurnResult)
	BeforeWriteClient func(msgType int, payload []byte, wroteDownstream bool) error
	BeforeClientWrite func(msgType int, payload []byte)
	AfterClientWrite  func(msgType int, payload []byte, writeErr error)
	BeforeRelayCancel func(exit RelayExit)
	ReadClientFrame   func(ctx context.Context, clientConn FrameConn) (int, []byte, error)
	OnTrace           func(event RelayTraceEvent)
	Now               func() time.Time
}

type RelayTraceEvent struct {
	Stage           string
	Direction       string
	MessageType     string
	PayloadBytes    int
	Graceful        bool
	WroteDownstream bool
	Error           string
}
