package forward

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// GeminiInput 保存当前请求以及提供商的展示和资格字段。
type GeminiInput struct {
	StartedAt                                   time.Time
	Model, Action                               string
	Stream, Sticky, TokenAvailable              bool
	Body                                        []byte
	GroupID, ProviderID                         int64
	SessionHash, ProviderName, Platform, Prefix string
}
type GeminiRecovery struct {
	ErrorBody   []byte
	ContentType string
}
type GeminiExecution struct {
	Model, OriginalModel                   string
	StartedAt                              time.Time
	Stream, Sticky                         bool
	Body, InjectedBody                     []byte
	ProjectID, UpstreamAction, SessionHash string
	GroupID                                int64
}
type GeminiHooks struct {
	Exchange    func() error
	Before      func(context.Context, *ExchangeResponse) (bool, error)
	OutputError func(error)
}

// GeminiPorts 通过平台提供的恢复操作执行供应商重试。
type GeminiPorts interface {
	GoogleError(int, string) error
	ImageInputSize([]byte) string
	ImageTier(string) string
	ZeroCount()
	MappedModel(string) string
	ModelParameters(context.Context, string, []byte) ([]byte, error)
	FeatureDenied()
	Credential(context.Context) error
	ProjectID() (string, error)
	Transport()
	InjectIdentity([]byte) ([]byte, error)
	CleanSchema([]byte) ([]byte, error)
	Wrap(string, string, []byte) ([]byte, error)
	ProjectRequired(error) bool
	Log(string)
	StdLog(string)
	Retry(context.Context, GeminiExecution) error
	SwitchError(error) (bool, bool)
	Failover(int, []byte, bool, bool) error
	ClientCanceled() bool
	Recover(context.Context, GeminiExecution) (GeminiRecovery, error)
	RequestID(string)
	Unwrap([]byte) ([]byte, error)
	Health(context.Context, int, map[string][]string, []byte, GeminiExecution)
	ErrorMessage([]byte) string
	Sanitize(string) string
	Detail([]byte) string
	SetError(int, string, string)
	GoogleConfigError(string) bool
	Observe(Notice)
	ShouldFailover(int) bool
	TruncateBytes([]byte, int) string
	ErrorBody(int, string, []byte)
	Execute(context.Context, GeminiExecution, GeminiHooks) (upstream.AttemptResult, error)
	IsImageModel(string) bool
}
