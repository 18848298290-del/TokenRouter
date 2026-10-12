package forward

import (
	"context"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// MessageInput captures provider eligibility and static forwarding options.
type MessageInput struct {
	ProviderPresent, HTTPPresent, OAuth, Passthrough, Bedrock bool
	ProviderID                                                int64
	ProviderName, ProviderType, Platform                      string
	LogErrorBody, FailoverOn400                               bool
	LogErrorBodyMaxBytes                                      int
}
type PassthroughInput struct {
	Body                        []byte
	Parsed                      *requeststate.ParsedRequest
	RequestModel, OriginalModel string
	Stream                      bool
	StartedAt                   time.Time
}
type NormalizeOptions struct {
	StripSystemCacheControl, InjectMetadata bool
	MetadataUserID                          string
}
type Notice struct {
	UpstreamURL                                                      string
	Passthrough                                                      bool
	Platform, ProviderName, UpstreamRequestID, Kind, Message, Detail string
	ProviderID                                                       int64
	UpstreamStatusCode                                               int
}
type ExchangeResponse struct {
	StatusCode int
	Headers    map[string][]string
	RequestID  string
}
type StreamOutcome struct {
	Usage            *upstream.TokenUsage
	FirstTokenMs     *int
	ClientDisconnect bool
}
type MessageHooks struct {
	Before                 func(context.Context, *ExchangeResponse, []byte) (bool, error)
	Wire                   func([]byte)
	Accepted, BeforeStream func()
	Stream                 func(*StreamOutcome)
}
type MessageExecution struct {
	Model, OriginalModel string
	Stream, Mimic        bool
	StartedAt            time.Time
	Body                 []byte
	ReplaceBody          func([]byte) error
	Hooks                MessageHooks
}

// MessagePorts supplies settings, provider operations, and transport steps used by Messages.
type MessagePorts interface {
	ShouldEmulate(context.Context, *int64, []byte) bool
	Emulate(context.Context, *requeststate.ParsedRequest) (*Result, error)
	MappedModel(string) string
	ReplaceModel([]byte, string) []byte
	Passthrough(context.Context, PassthroughInput) (*Result, error)
	Bedrock(context.Context, *requeststate.ParsedRequest, time.Time) (*Result, error)
	Begin() (func(), error)
	Beta(context.Context, string) error
	DebugOriginal([]byte, string, bool)
	ProviderMappedModel(string) string
	IsClaudeCode(context.Context, []byte, string) bool
	SystemSettings(context.Context) (bool, string, string)
	RewriteSystem([]byte, *requeststate.ParsedRequest, string, string) []byte
	Metadata(context.Context, *requeststate.ParsedRequest) string
	NormalizeOAuth([]byte, NormalizeOptions) []byte
	RewriteCache(context.Context, []byte) []byte
	RewriteTools([]byte) ([]byte, bool)
	BindTools()
	ToolsLast([]byte) []byte
	NormalizeDateline(context.Context, []byte) ([]byte, bool)
	CacheLimit([]byte) []byte
	PlatformModel(string) string
	InjectTTL(context.Context) bool
	CacheTTL([]byte) []byte
	Credential(context.Context) error
	Transport()
	FilterSearchHistory([]byte, string) []byte
	FilterThinking([]byte, string) []byte
	PassbackThinking(string) bool
	NormalizeThinking([]byte, string) ([]byte, bool)
	ReadErrorBody() ([]byte, error)
	ResetErrorBody([]byte)
	Health(context.Context, string, int, map[string][]string, []byte, string) ErrorDecision
	HandleError(context.Context, string, bool) (*Result, error)
	Observe(Notice)
	Failover400([]byte) bool
	FailoverError(int, []byte, bool) error
	IsFailover(error) bool
	Truncate(string, int) string
	TruncateBytes([]byte, int) string
	Sanitize(string) string
	Log(string)
	Execute(context.Context, MessageExecution) (upstream.AttemptResult, error)
	StreamError(error) (string, bool)
	Size() int
	Written() bool
	GenericError()
	ServiceTier() string
}

// ShouldRetry permits HTTP 403 retries for OAuth or Setup Token requests.
func ShouldRetry(oauth bool, status int) bool { return oauth && status == 403 }

// ShouldFailover identifies eligible response statuses; the request loop decides whether to switch.
func ShouldFailover(status int) bool {
	switch status {
	case 401, 403, 429, 529:
		return true
	default:
		return status >= 500
	}
}

// RetryDelay computes a 300 ms exponential delay capped at three seconds.
func RetryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 300 * time.Millisecond
	}
	delay := 300 * time.Millisecond * time.Duration(1<<(attempt-1))
	if delay > 3*time.Second {
		return 3 * time.Second
	}
	return delay
}

// PartialUsage returns observed stream usage when failover is not requested. A result requires a nonnil response.
func PartialUsage(resp *ExchangeResponse, stream *StreamOutcome, model, upstreamModel string, start time.Time, speed string, failover bool) *Result {
	if stream == nil || !stream.Usage.HasObservedTokens() || failover {
		return nil
	}
	usage := *stream.Usage
	if strings.TrimSpace(usage.Speed) == "" && strings.EqualFold(strings.TrimSpace(speed), "fast") {
		usage.Speed = "fast"
	}
	requestID := ""
	if resp != nil {
		requestID = resp.RequestID
	}
	return &Result{
		RequestID:        requestID,
		UpstreamHeaders:  resp.Headers,
		Usage:            usage,
		Model:            model,
		UpstreamModel:    upstreamModel,
		Stream:           true,
		Duration:         time.Since(start),
		FirstTokenMs:     stream.FirstTokenMs,
		ClientDisconnect: stream.ClientDisconnect,
	}
}
