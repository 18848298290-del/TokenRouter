package openaiforward

import (
	"context"
	"encoding/json"
	"net/http"

	"go.uber.org/zap"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type RawFallbackPorts interface {
	AnthropicToChat(*protocolanthropic.AnthropicRequest) (*protocolopenai.ChatCompletionsRequest, error)
	Profile() MessagesProfile
	Error(int, string, string)
	ValidateEffort([]byte, string) error
	BillingModel(string, string) string
	UpstreamModel(string) string
	MessagesEffort(*protocolanthropic.AnthropicRequest, string, string) string
	ThinkingFallback(*string, []byte, string) *string
	ServiceTier([]byte) *string
	PrepareModel(context.Context, []byte) ([]byte, error)
	ApplyEffort(context.Context, []byte) ([]byte, bool, error)
	FastFallback(context.Context, string, []byte) ([]byte, error)
	Debug(string, ...zap.Field)
	RecacheInput(json.RawMessage)
	ReasoningContent(string) string
	EffectiveEffort([]byte, []byte, ...string) *string
	ObserveModel(string)
	Target(context.Context) (string, string, error)
	Endpoint(string)
	SendCC(context.Context, string, []byte, bool, string) (*http.Response, error)
	ReadUpstreamError(*http.Response) ([]byte, string)
	FailoverHTTP(context.Context, *http.Response, []byte, string, string) error
	AnthropicError(*http.Response, string) (*Result, error)
	ResponsesError(context.Context, *http.Response, []byte, string) (*Result, error)
	Sink() upstream.OutputSink
	RawOptions(*http.Response, string, string, *string) openai.RawResponseOptions
}
