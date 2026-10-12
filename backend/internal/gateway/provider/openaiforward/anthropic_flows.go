package openaiforward

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

const (
	NativeMessages NativeAnthropicKind = iota
	NativeResponses
	NativeChat
)

type NativeAnthropicKind uint8

func ForwardNativeMessages(ctx context.Context, body []byte, defaultMappedModel string, p NativeAnthropicPorts) (*Result, error) {
	profile := p.Profile()
	startTime := time.Now()

	originalModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if originalModel == "" {
		p.Error(http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := gjson.GetBytes(body, "stream").Bool()

	billingModel := p.BillingModel(originalModel, defaultMappedModel)
	upstreamModel := p.UpstreamModel(billingModel)
	if upstreamModel != originalModel {
		rewritten, err := sjson.SetBytes(body, "model", upstreamModel)
		if err != nil {
			return nil, fmt.Errorf("rewrite model: %w", err)
		}
		body = rewritten
	}
	body, err := p.PrepareModel(ctx, body, upstreamModel)
	if err != nil {
		return nil, err
	}

	// Record output_config.effort when present. For passback-required CN models,
	// enabled thinking without an effort records the default high value.
	// The Anthropic gateway uses the same recording rule.
	requestedReasoningEffort := protocol.NormalizeClaudeOutputEffort(gjson.GetBytes(body, "output_config.effort").String())
	reasoningEffort := p.ThinkingFallback(
		requestedReasoningEffort,
		body,
		billingModel,
	)

	// Apply the Messages passthrough filters: empty text and unsupported search history
	// can cause compatible upstreams to reject the request.
	body = p.StripEmpty(body)
	body = p.FilterSearch(body, upstreamModel)

	p.Log("[CN Anthropic 直通] provider=%d(%s) platform=%s model=%s upstream=%s stream=%v",
		profile.ID, profile.Name, profile.Platform, originalModel, upstreamModel, clientStream)

	apiKey := strings.TrimSpace(p.ProtocolAPIKey())
	if apiKey == "" {
		return nil, fmt.Errorf("provider %d missing api_key", profile.ID)
	}
	targetURL, err := p.TargetURL()
	if err != nil {
		return nil, err
	}

	p.PrepareTransport()

	upstreamCtx, releaseUpstreamCtx := p.StreamContext(ctx, clientStream)
	upstreamReq, err := p.BuildNative(upstreamCtx, body, apiKey, targetURL)
	releaseUpstreamCtx()
	if err != nil {
		return nil, err
	}

	resp, err := p.SendNative(upstreamReq)
	if err != nil {
		return nil, p.TransportErrorNative(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := p.ReadUpstreamError(resp)
		if foErr := p.FailoverHTTP(ctx, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		// Use the shared Anthropic error formatter, passthrough rules, Ops observations,
		// and cyber-policy detection when failover is not requested.
		return p.ErrorResponse(resp, billingModel)
	}

	if clientStream {
		result, err := NativeAnthropicStreaming(ctx, resp, upstream.NewDeferredOutputContext(p.Sink()), p.DirectOptions(), originalModel, billingModel, upstreamModel, reasoningEffort, startTime)
		if result != nil {
			result.RequestedReasoningEffort = requestedReasoningEffort
		}
		return result, err
	}
	result, err := NativeAnthropicBuffered(ctx, resp, upstream.NewDeferredOutputContext(p.Sink()), p.DirectOptions(), originalModel, billingModel, upstreamModel, reasoningEffort, startTime)
	if result != nil {
		result.RequestedReasoningEffort = requestedReasoningEffort
	}
	return result, err
}

func ForwardNativeResponses(ctx context.Context, body []byte, defaultMappedModel string, p NativeAnthropicPorts) (*Result, error) {
	profile := p.Profile()
	startTime := time.Now()

	// Convert Codex client tools into function tools understood by Anthropic.
	adaptedBody, clientToolMapping, err := p.AdaptResponsesTools(body)
	if err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", "Failed to adapt request tools")
		return nil, fmt.Errorf("adapt responses client tools: %w", err)
	}

	var responsesReq protocolopenai.ResponsesRequest
	if err := json.Unmarshal(adaptedBody, &responsesReq); err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := responsesReq.Model
	if strings.TrimSpace(originalModel) == "" {
		p.Error(http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := responsesReq.Stream

	anthropicReq, err := p.ResponsesToAnthropic(&responsesReq)
	if err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", "Failed to convert request")
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}

	// Apply model mapping from the OpenAI gateway entry point.
	billingModel := p.BillingModel(originalModel, defaultMappedModel)
	upstreamModel := p.UpstreamModel(billingModel)
	anthropicReq.Model = upstreamModel

	reasoningEffort := p.ResponsesEffort(body, upstreamModel, billingModel, originalModel)
	reasoningEffort = p.ThinkingFallback(reasoningEffort, body, billingModel)

	// Read a streaming upstream response for both client modes.
	// The buffered output path assembles a non-streaming response when requested.
	anthropicReq.Stream = true
	reqStream := true

	p.Debug("openai responses: forwarding via native anthropic endpoint",
		zap.Int64("provider_id", profile.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("client_stream", clientStream),
	)

	anthropicBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	// Apply the same body filters as the direct Messages path.
	anthropicBody = p.StripEmpty(anthropicBody)
	anthropicBody = p.FilterSearch(anthropicBody, upstreamModel)
	anthropicBody = p.CacheLimit(anthropicBody)

	apiKey := strings.TrimSpace(p.ProtocolAPIKey())
	if apiKey == "" {
		return nil, fmt.Errorf("provider %d missing api_key", profile.ID)
	}
	targetURL, err := p.TargetURL()
	if err != nil {
		return nil, err
	}

	p.PrepareTransport()

	upstreamCtx, releaseUpstreamCtx := p.StreamContext(ctx, reqStream)
	upstreamReq, err := p.BuildNative(upstreamCtx, anthropicBody, apiKey, targetURL)
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	resp, err := p.SendNative(upstreamReq)
	if err != nil {
		return nil, p.TransportErrorNative(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := p.ReadUpstreamError(resp)
		if foErr := p.FailoverHTTP(ctx, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		p.Error(p.MapStatus(resp.StatusCode), "server_error", upstreamMsg)
		return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
	}

	if clientStream {
		return ResponsesFromAnthropicStreaming(resp, upstream.NewDeferredOutputContext(p.Sink()), p.OutputOptions(), originalModel, billingModel, upstreamModel, reasoningEffort, startTime, clientToolMapping)
	}
	return ResponsesFromAnthropicBuffered(resp, upstream.NewDeferredOutputContext(p.Sink()), p.OutputOptions(), originalModel, billingModel, upstreamModel, reasoningEffort, startTime, clientToolMapping)
}

func ForwardNativeChat(ctx context.Context, body []byte, defaultMappedModel string, p NativeAnthropicPorts) (*Result, error) {
	profile := p.Profile()
	startTime := time.Now()

	var ccReq protocolopenai.ChatCompletionsRequest
	if err := json.Unmarshal(body, &ccReq); err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse chat completions request: %w", err)
	}
	originalModel := ccReq.Model
	if strings.TrimSpace(originalModel) == "" {
		p.Error(http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := ccReq.Stream
	includeUsage := ccReq.StreamOptions != nil && ccReq.StreamOptions.IncludeUsage

	// Convert Chat through Responses before producing the Anthropic request.
	responsesReq, err := p.ChatToResponses(&ccReq)
	if err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", "Failed to convert request")
		return nil, fmt.Errorf("convert chat completions to responses: %w", err)
	}
	anthropicReq, err := p.ResponsesToAnthropic(responsesReq)
	if err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", "Failed to convert request")
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}

	// Apply model mapping from the OpenAI gateway entry point.
	billingModel := p.BillingModel(originalModel, defaultMappedModel)
	upstreamModel := p.UpstreamModel(billingModel)
	anthropicReq.Model = upstreamModel

	// Read a streaming upstream response for both client modes.
	// The buffered output path assembles a non-streaming response when requested.
	anthropicReq.Stream = true
	reqStream := true

	p.Debug("openai chat_completions: forwarding via native anthropic endpoint",
		zap.Int64("provider_id", profile.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("client_stream", clientStream),
	)

	anthropicBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	// Apply the same body filters as the direct Messages path.
	anthropicBody = p.StripEmpty(anthropicBody)
	anthropicBody = p.FilterSearch(anthropicBody, upstreamModel)
	anthropicBody = p.CacheLimit(anthropicBody)

	apiKey := strings.TrimSpace(p.ProtocolAPIKey())
	if apiKey == "" {
		return nil, fmt.Errorf("provider %d missing api_key", profile.ID)
	}
	targetURL, err := p.TargetURL()
	if err != nil {
		return nil, err
	}

	p.PrepareTransport()

	upstreamCtx, releaseUpstreamCtx := p.StreamContext(ctx, reqStream)
	upstreamReq, err := p.BuildNative(upstreamCtx, anthropicBody, apiKey, targetURL)
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	resp, err := p.SendNative(upstreamReq)
	if err != nil {
		return nil, p.TransportErrorNative(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := p.ReadUpstreamError(resp)
		if foErr := p.FailoverHTTP(ctx, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		p.Error(p.MapStatus(resp.StatusCode), "server_error", upstreamMsg)
		return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
	}

	reasoningEffort := p.ChatEffort(body, upstreamModel, billingModel, originalModel)
	reasoningEffort = p.ThinkingFallback(reasoningEffort, body, billingModel)

	if clientStream {
		return ChatFromAnthropicStreaming(resp, upstream.NewDeferredOutputContext(p.Sink()), p.OutputOptions(), originalModel, billingModel, upstreamModel, reasoningEffort, startTime, includeUsage)
	}
	return ChatFromAnthropicBuffered(resp, upstream.NewDeferredOutputContext(p.Sink()), p.OutputOptions(), originalModel, billingModel, upstreamModel, reasoningEffort, startTime)
}
