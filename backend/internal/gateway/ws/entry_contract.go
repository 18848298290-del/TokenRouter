package ws

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// EntryKey 保存入站 WebSocket 所需的已验证访问数据。
type EntryKey struct {
	ID                int64
	UserID            int64
	GroupID           *int64
	ModelMapping      map[string]string
	FastModePolicy    string
	RefreshFastPolicy bool
	Group             *EntryGroup
}
type EntryGroup struct {
	Platform                    string
	MaxReasoningEffort          string
	MaxReasoningEffortOverLimit string
	ReasoningEffortMappings     []routing.ReasoningEffortMapping
	ImagesAllowed               bool
}
type EntrySubject struct {
	UserID      int64
	Concurrency int
}
type EntryInput struct {
	Key                    *EntryKey
	Subject                EntrySubject
	ClientLifecycleContext context.Context
	FirstTurnStartedAt     time.Time
	ClientIP               string
	UserAgent              string
	MaxProviderSwitches    int
}

// EntryProvider 保存提供商资格和展示字段。
type EntryProvider struct {
	provider.ProviderSnapshot
	Name   string
	Shadow bool
}
type EntrySelection struct {
	Provider    *EntryProvider
	Acquired    bool
	ReleaseFunc func()
	WaitPlan    *scheduler.ProviderWaitPlan
	Target      EntryTarget
}
type EntryDecision struct {
	Layer             string
	CandidateCount    int
	StickyPreviousHit bool
}
type EntryFailure struct {
	Err                   error
	StatusCode            int
	ReportScheduleFailure bool
	RetryNext             bool
}
type EntryClose struct {
	Status  int
	Reason  string
	Present bool
}
type EntryCyberSnapshot struct {
	Excerpt string
	Input   moderation.ContentModerationInput
}

// EntryHooks 在 turn 生命周期中接收入口回调。
type EntryHooks struct {
	ContextForTurn              func(int) context.Context
	ClientLifecycleContext      context.Context
	InitialRequestModel         string
	InitialTurnStartedAt        time.Time
	MaxReasoningEffort          string
	MaxReasoningEffortOverLimit string
	ReasoningEffortMappings     []routing.ReasoningEffortMapping
	ResolveFastModePolicy       func(int) string
	ResolveRoutingModel         func(int, string, []byte) (string, error)
	TurnStarted                 func(int, time.Time)
	BeforeTurn                  func(int) error
	BeforeRequest               func(int, []byte, string, string) ([]byte, error)
	OnUpstreamError             func(int, string, int, []byte, string)
	AfterTurn                   func(TurnCapture)
}

// EntryTarget 提供已选提供商的一次执行操作，执行时由目标处理凭据。
type EntryTarget interface {
	MappedModel(string) string
	Report(string, bool, *int)
	Switched()
	Stop429(int, int, *failover.OAuth429State) bool
	Credential(context.Context) error
	EnforceClient(context.Context, []byte) error
	ResolveRouting(context.Context, string, bool) (string, error)
	Warning(context.Context, string, int, []byte, string, EntryCyberSnapshot)
	RecordMarked(context.Context, string, bool, []byte, routing.PricingUsageFields, string) bool
	UpdateUsage(context.Context, map[string][]string)
	PrepareCompletion(context.Context, *ForwardResult, TurnCapture, string, routing.GroupMappingResult, []byte, bool) *completion.Input
	BeginPreemption(context.Context, []byte) (context.Context, func(), bool)
	Run(context.Context, ClientSocket, []byte, *EntryHooks) error
	LogFailure(error)
}

type EntryCompletion interface {
	Record(context.Context, *completion.Input, bool) error
}

// EntryPorts 提供单步能力，提供商循环、每轮资源及完成时序由 RunEntry 拥有。
type EntryPorts interface {
	Logger() EntryLogger
	SetLogger(EntryLogger)
	ClassifyPrevious(string) string
	Platform() string
	Prompt(context.Context, []byte) []byte
	Redirect(context.Context, string) (context.Context, string)
	BindContext(context.Context)
	ObserveFirst(string)
	CaptureCyber([]byte) EntryCyberSnapshot
	Moderate(context.Context, string, []byte) *moderation.Decision
	ModerationError(context.Context, *moderation.Decision)
	BlockedSession(context.Context, []byte) string
	BlockedError(context.Context)
	BlockedMessage() string
	BlockedOps(string, string)
	Plan(context.Context, string) (context.Context, routing.GroupMappingResult)
	ImageIntent(string, []byte, routing.GroupMappingResult) ([]byte, string, bool)
	ImageContext(context.Context) context.Context
	ExplicitImage(string, []byte) bool
	ImagesAllowed() bool
	ImageDeniedMessage() string
	PolicyDenied()
	FeatureDenied()
	AcquireKey(context.Context) (context.Context, func(), error)
	AcquireUser(context.Context) (func(), bool, error)
	AcquireProvider(context.Context, int64, int) (func(), bool, error)
	WrapRelease(context.Context, func()) func()
	Eligibility(context.Context) error
	AuthorizeTurn(context.Context) error
	LoadSubscription()
	SessionHash([]byte, string) string
	ExplicitHash([]byte) string
	Isolate(context.Context, string, string) error
	IsolationError(context.Context, error)
	IsolationReason(error) string
	Guardian(context.Context, []byte, string) context.Context
	Select(context.Context, string, string, string, map[int64]struct{}, bool, bool, string) (*EntrySelection, EntryDecision, error)
	SelectionLogError(error, string) error
	BindSticky(context.Context, string, int64) error
	RefreshFast(context.Context) (string, bool)
	Failover(error) (*EntryFailure, bool)
	CloseFailover(*EntryFailure)
	Close(int, string)
	CloseError(int, string, error) error
	CloseInfo(error) EntryClose
	SessionPreempted(error) bool
	RemovePrevious([]byte) []byte
	LocalPolicyError(error) bool
	ReportFailure(error) bool
	EndedByClient(error) bool
	ClearCyber()
	CompletionRecorder() EntryCompletion
	CompletionObserver() func(int64, string, error)
	SubmitCompletion(*ForwardResult, func(context.Context))
}

// EntryField 保存可写入日志的字段值。
type EntryField struct {
	Key     string
	Text    string
	Integer int64
	Flag    bool
	Err     error
	Kind    uint8
}

type EntryLogger interface {
	With(...EntryField) EntryLogger
	Info(string, ...EntryField)
	Warn(string, ...EntryField)
	Debug(string, ...EntryField)
	Error(string, ...EntryField)
}

func EntryString(key, value string) EntryField { return EntryField{Key: key, Text: value, Kind: 1} }
func EntryInt(key string, value int) EntryField {
	return EntryField{Key: key, Integer: int64(value), Kind: 2}
}

func EntryInt64(key string, value int64) EntryField {
	return EntryField{Key: key, Integer: value, Kind: 2}
}

func EntryBool(key string, value bool) EntryField { return EntryField{Key: key, Flag: value, Kind: 3} }

func EntryError(err error) EntryField { return EntryField{Key: "error", Err: err, Kind: 4} }
