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

// ClientPayload 保存本轮按入站规则规范化后的独立报文。
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

// ForwardResult 保存一次 WS turn 的可观测结果和恢复输入。
type ForwardResult struct {
	// LocalWarmup 表示该轮预热在网关本地完成。
	LocalWarmup bool `json:"-"`
	// UpstreamResponseModel 保存当前 turn 的原始上游模型声明。
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

// TurnCapture 固化当前 turn，完成处理只接收该快照。
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

// IngressHooks 由请求准入组件提供，入站流程决定回调时机。
type IngressHooks struct {
	TurnStarted   func(int, time.Time)
	BeforeTurn    func(int) error
	BeforeRequest func(int, []byte, string, string) ([]byte, error)
	AfterTurn     func(TurnCapture)
}

// IngressState 是一条入站会话的明确状态；技术 Adapter 仅同步握手字段。
type IngressState struct {
	OriginalModel   string
	TurnState       string
	SessionHash     string
	PreferredConnID string
	StoreDisabled   bool
}

// ConnLease 提供本次连接池租约的读写和释放操作。
type ConnLease interface {
	ConnID() string
	MarkBroken()
	Release()
	SupportsIdlePingWithoutReader() bool
	PingWithTimeout(time.Duration) error
}

// PreviousTurn 是纯协议严格续接比较器，具体编解码仍由供应商原语提供。
type PreviousTurn interface {
	Keep([]byte, string, bool) (bool, string, error)
}

// ReplayCodec 调用供应商协议处理函数，网关决定恢复方式和调用顺序。
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

// IngressPort 的方法执行单步操作或一次上游 turn，提供商切换和会话重试由入站流程控制。
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

// IngressOptions 配置入站会话的控制预算和提供商绑定策略。
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

// IngressSession 管理 bridge 和 ctx_pool 的逐轮循环及恢复状态。
type IngressSession struct {
	Refresh func() Parameters
	State   *IngressState
	Store   session.OpenAIWSStateStore
	Options IngressOptions
	Hooks   *IngressHooks
	Codec   ReplayCodec
	Port    IngressPort
}

// AcquireRecoveryError 仅标记允许进行一次提供商身份恢复的拨号失败。
type AcquireRecoveryError struct{ Err error }

func (e *AcquireRecoveryError) Error() string { return e.Err.Error() }
func (e *AcquireRecoveryError) Unwrap() error { return e.Err }
