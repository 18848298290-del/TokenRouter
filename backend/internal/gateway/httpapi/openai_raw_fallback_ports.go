package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	openaiexecution "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type openAIRawFallbackAdapter struct {
	*openAIMessagesExecutionAdapter
	kind openaiexecution.NativeAnthropicKind
}

func (p *openAIRawFallbackAdapter) Profile() openaiexecution.MessagesProfile {
	return openaiexecution.MessagesProfile{Profile: openAIForwardProfile(p.provider), ID: p.provider.Record.ID}
}

func (p *openAIRawFallbackAdapter) errorWriter() func(*gin.Context, int, string, string) {
	if p.kind == openaiexecution.NativeMessages {
		return WriteForwardAnthropicError
	}
	return WriteForwardResponsesFallbackError
}

func (p *openAIRawFallbackAdapter) Error(status int, kind, message string) {
	p.errorWriter()(p.c, status, kind, message)
}

func (p *openAIRawFallbackAdapter) ThinkingFallback(e *string, b []byte, m string) *string {
	return gatewayprovider.ApplyThinkingEnabledFallback(e, b, m)
}

func (p *openAIRawFallbackAdapter) PrepareModel(ctx context.Context, body []byte) ([]byte, error) {
	return p.s.Requests.applyModelAttributes(ctx, p.provider, body, "openai_chat_completions")
}

func (p *openAIRawFallbackAdapter) FastFallback(ctx context.Context, m string, b []byte) ([]byte, error) {
	updated, err := tierpolicy.ApplyBody(b, p.s.FastPolicy.Input(ctx, p.provider, m))
	if blocked, ok := errors.AsType[*tierpolicy.BlockedError](err); ok {
		WriteFastPolicyBlockedResponse(p.c, blocked)
	}
	return updated, err
}

func (p *openAIRawFallbackAdapter) RecacheInput(b json.RawMessage) {
	p.s.Output.Reasoning.FromInput(b)
}

func (p *openAIRawFallbackAdapter) ReasoningContent(id string) string {
	return p.s.Output.Reasoning.Lookup(id)
}

func (p *openAIRawFallbackAdapter) EffectiveEffort(b, original []byte, models ...string) *string {
	return requeststate.ExtractOpenAIReasoningEffortFromBody(b)
}
func (p *openAIRawFallbackAdapter) ObserveModel(m string) { SetOpsUpstreamModel(p.c, m) }
func (p *openAIRawFallbackAdapter) Target(ctx context.Context) (string, string, error) {
	return p.s.Requests.ChatFallbackTarget(ctx, p.provider)
}

func (p *openAIRawFallbackAdapter) Endpoint(v string) {
	SetActualOpenAIUpstreamEndpoint(p.c, v)
}

func (p *openAIRawFallbackAdapter) SendCC(ctx context.Context, url string, b []byte, stream bool, key string) (*http.Response, error) {
	resetResponseModel(p.c)
	return p.s.Requests.SendChat(ctx, p.c, p.provider, url, b, stream, key, p.provider.View().GetOpenAIUserAgent(), "", p.tls...)
}

func (p *openAIRawFallbackAdapter) AnthropicError(r *http.Response, m string) (*openaiexecution.Result, error) {
	v, e := p.s.messagesError(r, p.c, p.provider, m)
	return openaiexecution.FromForwardResult(v), e
}

func (p *openAIRawFallbackAdapter) ResponsesError(ctx context.Context, r *http.Response, b []byte, m string) (*openaiexecution.Result, error) {
	v, e := p.s.Output.ResponseError(ctx, r, p.c, p.provider, b, m)
	return openaiexecution.FromForwardResult(v), e
}

func (p *openAIRawFallbackAdapter) RawOptions(r *http.Response, billing, model string, tier *string) openai.RawResponseOptions {
	return p.s.Output.RawOptions(p.c, r, nil, billing, model, tier, p.errorWriter())
}

func (p *openAIRawFallbackAdapter) AnthropicToChat(r *protocolanthropic.AnthropicRequest) (*protocolopenai.ChatCompletionsRequest, error) {
	result, err := p.modelResolution(r.Model, r, "openai_chat_completions")
	if err != nil {
		return nil, err
	}
	return protocolbridge.AnthropicToChatCompletionsRequest(r, gatewayprovider.ModelConversionOptions(result))
}
