package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	openaiwire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// UnifiedTextExecutor executes one exchange on the selected provider; the caller owns selection and failover.
type UnifiedTextExecutor struct {
	Pricing          *admission.ModelPricing
	OpenAI           *OpenAIResponsesExecutor
	Anthropic        *MessagesExecutor
	Gemini           *GeminiExecutor
	Antigravity      *AntigravityExecutor
	Qoder            *gatewayadapter.QoderRuntime
	QoderRefresh     *provideradapter.QoderRequestRefresh
	MessageQueue     NativeMessageQueue
	MessageQueueMode string
	MessageQueueWait time.Duration
}

// EnforceClient applies client policy to OpenAI-compatible providers.
func (e *UnifiedTextExecutor) EnforceClient(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte, match egress.TLSFingerprintRouterMatchResult) error {
	c.Request = c.Request.WithContext(ctx)
	if target.View().IsOpenAICompatible() {
		return e.OpenAI.Requests.EnforceClient(ctx, c, target, body, match)
	}
	return nil
}

func (e *UnifiedTextExecutor) Responses(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte) (*forward.OpenAIResult, error) {
	c.Request = c.Request.WithContext(ctx)
	var err error
	body, err = e.prepare(c, target, body, protocol.ProtocolOpenAIResponses)
	if err != nil {
		return nil, err
	}
	ctx = c.Request.Context()
	if target.View().IsOpenAICompatible() {
		return e.OpenAI.Forward(ctx, c, target, body)
	}
	return e.native(ctx, c, target, body, protocol.ProtocolOpenAIResponses)
}

func (e *UnifiedTextExecutor) Messages(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte, cacheKey, model string, tls ...egress.TLSFingerprintRouterMatchResult) (*forward.OpenAIResult, error) {
	c.Request = c.Request.WithContext(ctx)
	c.Set(nativeMessageInterceptedKey, false)
	var err error
	body, err = e.prepare(c, target, body, protocol.ProtocolAnthropicMessages)
	if err != nil {
		return nil, err
	}
	ctx = c.Request.Context()
	if target.View().IsOpenAICompatible() {
		return e.OpenAI.Text.Messages(ctx, c, target, body, cacheKey, model, tls...)
	}
	return e.native(ctx, c, target, body, protocol.ProtocolAnthropicMessages)
}

func (e *UnifiedTextExecutor) Chat(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte, cacheKey, model string, tls ...egress.TLSFingerprintRouterMatchResult) (*forward.OpenAIResult, error) {
	c.Request = c.Request.WithContext(ctx)
	var err error
	body, err = e.prepare(c, target, body, protocol.ProtocolOpenAIChatCompletions)
	if err != nil {
		return nil, err
	}
	ctx = c.Request.Context()
	if target.View().IsOpenAICompatible() {
		return e.OpenAI.Text.Chat(ctx, c, target, body, cacheKey, model, tls...)
	}
	return e.native(ctx, c, target, body, protocol.ProtocolOpenAIChatCompletions)
}

// prepare checks model pricing and reasoning policy for each selected provider attempt.
func (e *UnifiedTextExecutor) prepare(c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte, source protocol.ProtocolID) ([]byte, error) {
	SetOpsSelectedProvider(c, target.Record.ID, target.Record.Platform)
	if err := e.checkPricing(c, target, body, source); err != nil {
		return nil, err
	}
	key, _ := EffectiveAPIKey(c)
	var result []byte
	var err error
	switch target.Record.Platform {
	case provider.PlatformAnthropic:
		result, _, err = ApplyAnthropicReasoningEffortPolicyForRequest(c, key, body)
	case provider.PlatformOpenAI:
		result, _, err = ApplyOpenAIReasoningEffortPolicyForRequest(c, key, body)
	default:
		return body, nil
	}
	if err != nil {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
		if source == protocol.ProtocolAnthropicMessages {
			c.JSON(http.StatusForbidden, gin.H{"type": "error", "error": gin.H{"type": "permission_error", "message": err.Error()}})
		} else {
			c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"type": "permission_error", "message": err.Error(), "code": "reasoning_effort_limit"}})
		}
	}
	return result, err
}

// native dispatches to each platform's request and credential implementation.
func (e *UnifiedTextExecutor) native(ctx context.Context, c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte, source protocol.ProtocolID) (*forward.OpenAIResult, error) {
	var result *forward.MessagesResult
	var err error
	SetActualUpstreamEndpoint(c, "")
	var parsed *requeststate.ParsedRequest
	if source == protocol.ProtocolAnthropicMessages {
		var release func()
		var intercepted bool
		parsed, release, intercepted, err = e.prepareNativeMessages(c, target, body)
		if release != nil {
			defer release()
		}
		if err != nil || intercepted {
			return nil, err
		}
		body = parsed.Body.Bytes()
		ctx = c.Request.Context()
	}
	switch target.Record.Platform {
	case provider.PlatformAnthropic:
		switch source {
		case protocol.ProtocolAnthropicMessages:
			result, err = e.Anthropic.Forward(ctx, c, target, parsed)
		case protocol.ProtocolOpenAIResponses:
			result, err = e.Anthropic.ForwardAsResponses(ctx, c, target, body, nil)
		default:
			result, err = e.Anthropic.ForwardAsChatCompletions(ctx, c, target, body, nil)
		}
	case provider.PlatformGemini:
		SetActualUpstreamEndpoint(c, EndpointGeminiModels)
		switch source {
		case protocol.ProtocolAnthropicMessages:
			result, err = e.Gemini.Forward(ctx, c, target, body)
		case protocol.ProtocolOpenAIResponses:
			result, err = e.Gemini.ForwardAsResponses(ctx, c, target, body, nil)
		default:
			result, err = e.Gemini.ForwardAsChatCompletions(ctx, c, target, body)
		}
	case provider.PlatformAntigravity:
		SetActualUpstreamEndpoint(c, EndpointAntigravityGenerateContent)
		switch source {
		case protocol.ProtocolAnthropicMessages:
			sticky, ok := requeststate.PrefetchedStickyProviderIDFromContext(ctx)
			result, err = e.Antigravity.Forward(ctx, c, target, body, ok && sticky == target.Record.ID)
		case protocol.ProtocolOpenAIResponses:
			result, err = e.Antigravity.ForwardAsResponses(ctx, c, target, body, nil)
		default:
			result, err = e.Antigravity.ForwardAsChatCompletions(ctx, c, target, body, nil)
		}
	case provider.PlatformQoder:
		before := c.Writer.Size()
		result, err = ForwardQoderAttempt(ctx, c, e.Qoder, &target.Record, body, source)
		// Credential recovery retries the same provider once, before any result or output is delivered.
		if err != nil && result == nil && c.Writer.Size() == before && qoder.MayRefreshAttempt(err) && e.QoderRefresh != nil {
			fresh, refreshErr := e.QoderRefresh.RefreshProviderSession(ctx, &target.Record)
			if refreshErr == nil && fresh != nil {
				target.Record = *fresh
				result, err = ForwardQoderAttempt(ctx, c, e.Qoder, &target.Record, body, source)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported provider platform %q", target.Record.Platform)
	}
	return nativeTextResult(result, GetUpstreamEndpoint(c, target.Record.Platform)), err
}

// nativeTextResult retains native token categories and cache durations for completion billing.
func nativeTextResult(value *forward.MessagesResult, endpoint string) *forward.OpenAIResult {
	if value == nil {
		return nil
	}
	usage := value.Usage
	return &forward.OpenAIResult{
		RequestID: value.RequestID, UpstreamHeaders: value.UpstreamHeaders,
		Model: value.Model, UpstreamModel: value.UpstreamModel, UpstreamEndpoint: endpoint,
		NativeUsage: &usage,
		Usage:       openaiwire.ForwardUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheCreationInputTokens: usage.CacheCreationInputTokens, CacheReadInputTokens: usage.CacheReadInputTokens, ImageOutputTokens: usage.ImageOutputTokens},
		Stream:      value.Stream, Duration: value.Duration, FirstTokenMs: value.FirstTokenMs, ClientDisconnect: value.ClientDisconnect,
		ReasoningEffort: value.ReasoningEffort, RequestedReasoningEffort: value.RequestedReasoningEffort,
		ServiceTier:                 value.ServiceTier,
		UpstreamResponseServiceTier: value.UpstreamResponseServiceTier,
		UpstreamResponseModel:       value.UpstreamResponseModel,
		ImageCount:                  value.ImageCount, ImageSize: value.ImageSize, ImageInputSize: value.ImageInputSize, ImageOutputSize: value.ImageOutputSize,
		ImageOutputSizes: value.ImageOutputSizes, ImageSizeSource: value.ImageSizeSource, ImageSizeBreakdown: value.ImageSizeBreakdown,
		SearchCount: value.SearchCount, AudioUsage: value.AudioUsage,
	}
}
