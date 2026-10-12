package ws

import (
	"context"
	"encoding/json"
	"time"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// ClientPayload contains this turn's normalized ingress payload and routing metadata.
type ClientPayload struct {
	PayloadRaw                []byte
	ProviderIdentitySourceRaw []byte
	RawForHash                []byte
	PromptCacheKey            string
	PreviousResponseID        string
	OriginalModel             string
	RoutingModel              string
	ImageBillingModel         string
	ImageSizeTier             string
	ImageInputSize            string
	PayloadBytes              int
	RequestedReasoningEffort  *string
}

// ForwardResult contains one turn's observed result and recovery input.
type ForwardResult struct {
	// LocalWarmup reports that the gateway completed the warmup locally.
	LocalWarmup bool `json:"-"`
	// UpstreamResponseModel is the original model declaration observed in this turn's upstream response.
	UpstreamResponseModel string
	VideoCount            int
	VideoResolution       string
	VideoDurationSeconds  int
	WebSearchCalls        int
	SearchCount           int
	AudioUsage            *protocol.AudioUsage
	UpstreamWarning       *forwardcore.UpstreamWarning

	RequestID                     string
	ResponseID                    string
	UpstreamHeaders               map[string][]string
	Usage                         wire.ForwardUsage
	Model                         string
	BillingModel                  string
	UpstreamModel                 string
	UpstreamResponseServiceTier   string
	UpstreamEndpoint              string
	ServiceTier                   *string
	ReasoningEffort               *string
	RequestedReasoningEffort      *string
	Stream                        bool
	OpenAIWSMode                  bool
	UpstreamTerminalEvent         string
	ResponseHeaders               map[string][]string
	ResponseTurnState             string
	Duration                      time.Duration
	FirstTokenMs                  *int
	ClientDisconnect              bool
	ImageCount                    int
	ImageSize                     string
	ImageInputSize                string
	ImageOutputSize               string
	ImageOutputSizes              []string
	ImageSizeSource               string
	ImageSizeBreakdown            map[string]int
	WSReplayInput                 []json.RawMessage
	WSReplayInputExists           bool
	WSProviderFailoverReplayInput []json.RawMessage
}

// TurnCapture supplies the current turn's snapshot to completion processing.
type TurnCapture struct {
	Turn               int
	StartedAt          time.Time
	RequestBody        []byte
	OriginalModel      string
	PreviousResponseID string
	Result             *ForwardResult
	Err                error
	PayloadSource      string
}

// IngressHooks supplies admission callbacks whose timing is controlled by the ingress flow.
type IngressHooks struct {
	TurnStarted   func(int, time.Time)
	BeforeTurn    func(int) error
	BeforeRequest func(int, []byte, string, string) ([]byte, error)
	AfterTurn     func(TurnCapture)
}

// IngressState holds one ingress session's model, handshake, and connection state.
type IngressState struct {
	OriginalModel   string
	TurnState       string
	SessionHash     string
	PreferredConnID string
	StoreDisabled   bool
}

// ConnLease exposes connection identity, health, ping, and release operations for a pool lease.
type ConnLease interface {
	ConnID() string
	MarkBroken()
	Release()
	SupportsIdlePingWithoutReader() bool
	PingWithTimeout(time.Duration) error
}

// PreviousTurn compares protocol state for strict continuation.
type PreviousTurn interface {
	Keep([]byte, string, bool) (bool, string, error)
}

// ReplayCodec supplies upstream payload operations; the gateway chooses recovery and call order.
type ReplayCodec interface {
	Extract([]byte) ([]json.RawMessage, bool, error)
	BuildFromItems([]json.RawMessage, bool, []json.RawMessage, bool, bool) ([]json.RawMessage, bool)
	Build([]json.RawMessage, bool, []byte, bool) ([]json.RawMessage, bool, error)
	SetInput([]byte, []json.RawMessage, bool) ([]byte, error)
	RetryPayload([]byte, []json.RawMessage, bool, string) ([]byte, bool, error)
	Combine([]json.RawMessage, []json.RawMessage) []json.RawMessage
	HasOutput([]byte) bool
	ItemsHaveOutput([]json.RawMessage) bool
	ItemsCoverOutput([]json.RawMessage) bool
	DropPrevious([]byte) ([]byte, bool, error)
	SetPrevious([]byte, string) ([]byte, error)
	BuildStrict([]byte) (PreviousTurn, error)
	KeepPrevious([]byte, []byte, string, bool) (bool, string, error)
	StripItems([]json.RawMessage, map[string]struct{}) ([]json.RawMessage, int)
	ShouldInfer(bool, int, wire.ToolContinuationSignals, string, string) bool
	ClassifyPrevious(string) string
}

// IngressPort supplies individual operations and upstream turns; ingress controls session retries.
type IngressPort interface {
	Parse([]byte, bool, int) (ClientPayload, error)
	ShouldBridge(ClientPayload) bool
	ReadClient() ([]byte, error)
	GenerateHash([]byte) string
	StoreDisabled([]byte) bool
	InvalidDigests(int64, string) map[string]struct{}
	StripInvalid([]byte, map[string]struct{}, string, int64, int) ([]byte, int)
	BridgeIdentity([]byte, string, int) (string, error)
	Bridge(context.Context, ClientPayload, []byte, string, int) (*ForwardResult, error)
	SetRequestState(string, string)
	OpenPool(ClientPayload) error
	Acquire(int, string, bool, bool) (ConnLease, error)
	RecoverAcquire(context.Context) error
	Relay(int, ConnLease, ClientPayload) (*ForwardResult, error)
	PinConn(int64, string) bool
	UnpinConn(int64, string)
	Header(string) string
	UpdateHeaders(ClientPayload, string)
	BindOwner(context.Context, string)
	IsDisconnect(error) bool
	IsFailover(error) bool
	CloseError(int, string, error) error
	Log(string)
	Debug(string)
	NormalizeLog(string) string
	TruncateLog(string, int) string
	SummarizeClose(error) (string, string)
	BindWarning(int64, int64, string, error)
}

// IngressOptions configures session budgets and provider affinity.
type IngressOptions struct {
	ProviderType       string
	BridgeThreshold    int64
	ProviderID         int64
	Platform           string
	GroupID            int64
	UseBridge          bool
	Debug              bool
	PreviousRecovery   bool
	StoreDisabledMode  string
	PreflightPingIdle  time.Duration
	HealthCheckTimeout time.Duration
	ResponseStickyTTL  time.Duration
	SessionStickyTTL   time.Duration
}

// IngressSession owns HTTP bridge and pooled-connection turn loops and recovery state.
type IngressSession struct {
	Refresh func() Parameters
	State   *IngressState
	Store   session.OpenAIWSStateStore
	Options IngressOptions
	Hooks   *IngressHooks
	Codec   ReplayCodec
	Port    IngressPort
}

// AcquireRecoveryError marks a dial failure eligible for one provider-identity recovery attempt.
type AcquireRecoveryError struct{ Err error }

func (e *AcquireRecoveryError) Error() string { return e.Err.Error() }
func (e *AcquireRecoveryError) Unwrap() error { return e.Err }
