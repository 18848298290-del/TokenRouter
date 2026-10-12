package googleforward

import (
	"context"
	"io"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// Options 包含 app 传入的启动配置，动态修复开关在请求处理中读取。
type Options struct {
	Configured           bool
	LogErrorBody         bool
	LogErrorBodyMaxBytes int
	DebugHeaders         bool
	ResponseReadLimit    int64
	MaxLineSize          int
	StreamInterval       int
	StreamKeepalive      int
	URLAllowlistEnabled  bool
	AllowInsecureHTTP    bool
	AllowedHosts         []string
	AllowPrivateHosts    bool
}

// Output 定义同步 HTTP 交换与错误观测操作。
type Output interface {
	RequestContext() context.Context
	GetHeader(string) string
	Header(string, string)
	WriteHeaders(http.Header, http.Header, *egress.CompiledHeaderFilter)
	Raw(int, string, []byte)
	Sink() upstream.OutputSink
	Commit()
	Count(int)
	GeminiErrorBody(int, string, []byte)
	ReadBody(io.Reader, int64) ([]byte, error)
	Observe(ops.OpsUpstreamErrorEvent)
	SetError(int, string, string)
	FeatureDenied()
	ClaudeError(int, string, string) error
	GoogleError(int, string) error
	ChatError(int, string, string) error
	GeminiCustomCodeSkippedError(*gatewayadapter.ExecutionProvider, int, string, []byte, func()) error
	GeminiNativeUpstreamError(*gatewayadapter.ExecutionProvider, *http.Response, []byte, string, bool) error
	GeminiMappedError(*gatewayadapter.ExecutionProvider, int, string, []byte) error
	GeminiOpenAICompatMappedError(*gatewayadapter.ExecutionProvider, int, string, []byte, gemini.OpenAICompatProtocol) error
	GeminiOpenAICompatError(gemini.OpenAICompatProtocol, int, string, string) error
	AntigravityCompatError(int, string, string) error
	MappedAntigravityCompatError(*gatewayadapter.ExecutionProvider, int, string, []byte) error
	MapAntigravityCollectionError(error) error
	MappedClaudeError(*gatewayadapter.ExecutionProvider, int, string, []byte) error
}

// Gemini 包含凭据、传输和错误观测依赖。
type Gemini struct {
	Attributes   *routing.ModelAttributeService
	Options      Options
	Tokens       *provider.GeminiTokenSource
	Health       *provideradapter.UpstreamHealth
	Errors       *provideradapter.GeminiErrorObserver
	Transport    httpclient.UpstreamTransport
	HeaderFilter *egress.CompiledHeaderFilter
	Enter        func() (func(), error)
}

// Antigravity 使用应用已有的健康与重试实例，每次调用独立构造平台输入。
type Antigravity struct {
	Attributes *routing.ModelAttributeService
	Options    Options
	Tokens     *provider.AntigravityTokenSource
	Retry      *provideradapter.AntigravityRetry
	Errors     *provideradapter.AntigravityErrorObserver
	Store      gatewayadapter.ExecutionProviderStore
	Transport  httpclient.UpstreamTransport
	Gateway    *gateway.RuntimeSettings
	Routing    *routing.RuntimeSettings
	Sticky     session.GatewayCache
	Enter      func() (func(), error)
}

// attempt 的图片计数与工具映射只属于一次平台调用，切换提供商时重新创建。
type attempt struct {
	Output
	ToolNames *anthropic.ToolNameRewrite
	Images    int
}

// LogConfig 保留错误日志的缺省截断长度。
func (o Options) LogConfig() (bool, int) {
	n := 2048
	if o.LogErrorBodyMaxBytes > 0 {
		n = o.LogErrorBodyMaxBytes
	}
	return o.Configured && o.LogErrorBody, n
}

func (o Options) ErrorDetail(body []byte) string {
	enabled, n := o.LogConfig()
	if !enabled {
		return ""
	}
	return logredact.TruncateUTF8(string(body), n)
}

func (a *attempt) reverseTools(body []byte) []byte {
	return anthropic.RestoreToolNamesInBytes(body, a.ToolNames)
}

func (a *attempt) observeImages(body []byte) {
	if n := gemini.CountGeminiInlineImageOutputs(body); n > a.Images {
		a.Images = n
	}
}

func (a *attempt) imageCount(original, mapped string) int {
	if a.Images > 0 {
		return a.Images
	}
	if antigravity.IsImageGenerationModel(original) || antigravity.IsImageGenerationModel(mapped) {
		return 1
	}
	return 0
}

func messagesResult(value *forward.Result) *forward.MessagesResult {
	if value == nil {
		return nil
	}
	copy := forward.MessagesResult(*value)
	return &copy
}
