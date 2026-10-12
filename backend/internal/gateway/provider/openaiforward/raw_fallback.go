package openaiforward

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func MessagesViaRawChat(ctx context.Context, body []byte, defaultMappedModel string, p RawFallbackPorts) (*Result, error) {
	profile := p.Profile()
	startTime := time.Now()

	var anthropicReq protocolanthropic.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse anthropic request: %w", err)
	}
	originalModel := anthropicReq.Model
	if strings.TrimSpace(originalModel) == "" {
		p.Error(http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	if err := p.ValidateEffort(body, originalModel); err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	clientStream := anthropicReq.Stream
	billingModel := p.BillingModel(originalModel, defaultMappedModel)
	upstreamModel := p.UpstreamModel(billingModel)
	anthropicReq.Model = upstreamModel

	// Convert Messages directly to Chat Completions.
	chatReq, err := p.AnthropicToChat(&anthropicReq)
	if err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("convert anthropic to chat completions: %w", err)
	}

	chatReq.Model = upstreamModel
	chatReq.ReasoningEffort = p.MessagesEffort(&anthropicReq, upstreamModel, chatReq.ReasoningEffort)
	chatReq.Stream = clientStream
	if clientStream {
		chatReq.StreamOptions = &protocolopenai.ChatStreamOptions{IncludeUsage: true}
	}

	convertedEffort := chatReq.ReasoningEffort
	reasoningEffort := &convertedEffort
	reasoningEffort = p.ThinkingFallback(reasoningEffort, body, billingModel)
	serviceTier := p.ServiceTier(body)

	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions request: %w", err)
	}
	chatBody, err = p.PrepareModel(ctx, chatBody)
	if err != nil {
		return nil, err
	}
	// The Messages bridge applies group effort limits to client-specified reasoning controls.
	if profile.OpenAI {
		policyBody, changed, policyErr := p.ApplyEffort(ctx, chatBody)
		if policyErr != nil {
			return nil, policyErr
		}
		if changed {
			chatBody = policyBody
			if effectiveEffort := strings.TrimSpace(gjson.GetBytes(chatBody, "reasoning_effort").String()); effectiveEffort != "" {
				reasoningEffort = &effectiveEffort
			}
		}
	}
	chatBody, err = p.FastFallback(ctx, upstreamModel, chatBody)
	if err != nil {
		return nil, err
	}
	if serviceTier == nil {
		serviceTier = p.ServiceTier(chatBody)
	}

	p.Debug("openai messages: forwarding via raw chat completions",
		zap.Int64("provider_id", profile.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	// Use the shared Chat transport to build and send the request.
	apiKey, targetURL, err := p.Target(ctx)
	if err != nil {
		return nil, err
	}
	p.Endpoint("/v1/chat/completions")
	resp, err := p.SendCC(ctx, targetURL, chatBody, clientStream, apiKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := p.ReadUpstreamError(resp)
		if foErr := p.FailoverHTTP(ctx, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		// The shared Anthropic error handler applies passthrough rules, Ops observations,
		// and cyber-policy detection after failover checks.
		return p.AnthropicError(resp, billingModel)
	}

	output := upstream.NewDeferredOutputContext(p.Sink())
	options := p.RawOptions(resp, billingModel, upstreamModel, serviceTier)
	var result *openai.CompatResponseResult
	if clientStream {
		result, err = openai.ReadCCAsMessagesStreaming(output, resp, options, originalModel, upstreamModel, reasoningEffort, startTime)
	} else {
		result, err = openai.ReadCCAsMessagesBuffered(output, resp, options, originalModel, upstreamModel, reasoningEffort, startTime)
	}
	return FromCompatResult(result, billingModel), err
}

func ResponsesViaRawChat(ctx context.Context, body []byte, p RawFallbackPorts) (*Result, error) {
	profile := p.Profile()
	startTime := time.Now()

	var responsesReq protocolopenai.ResponsesRequest
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := strings.TrimSpace(responsesReq.Model)
	if originalModel == "" {
		p.Error(http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}

	clientStream := responsesReq.Stream
	serviceTier := p.ServiceTier(body)
	// Save tool identities before converting them to Chat function tools. Response conversion
	// restores custom_tool_call and tool_search_call items by these names and flags.
	// Flattened namespace tools use the saved map to regain function_call.namespace.
	effectiveTools, err := bridge.EffectiveResponsesTools(&responsesReq)
	if err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("resolve responses tools: %w", err)
	}
	customTools := bridge.CustomToolNames(effectiveTools)
	functionTools := bridge.FunctionToolNames(effectiveTools)
	toolSearch := bridge.HasToolSearchTool(effectiveTools)
	namespaceTools := bridge.NamespaceToolNames(effectiveTools)

	// Refresh cached reasoning from plaintext summaries so later encrypted-only copies can be resolved.
	p.RecacheInput(responsesReq.Input)
	chatReq, err := bridge.ResponsesToChatCompletionsRequestWithOptions(&responsesReq, &bridge.ResponsesToChatOptions{
		ReasoningContentByID: p.ReasoningContent,
	})
	if err != nil {
		p.Error(http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("convert responses to chat completions: %w", err)
	}

	billingModel := p.BillingModel(originalModel, "")
	upstreamModel := p.UpstreamModel(billingModel)
	chatReq.Model = upstreamModel
	if clientStream {
		chatReq.StreamOptions = &protocolopenai.ChatStreamOptions{IncludeUsage: true}
	}

	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions fallback request: %w", err)
	}
	chatBody, err = p.FastFallback(ctx, upstreamModel, chatBody)
	if err != nil {
		return nil, err
	}
	// Usage records use effort from the final Chat request after conversion and policy processing.
	reasoningEffort := p.EffectiveEffort(chatBody, body, upstreamModel, billingModel, originalModel)
	// Record a default display effort for supported CN models with thinking enabled and no explicit effort.
	reasoningEffort = p.ThinkingFallback(reasoningEffort, chatBody, billingModel)
	if serviceTier == nil {
		serviceTier = p.ServiceTier(chatBody)
	}

	p.Debug("openai responses: forwarding via raw chat completions",
		zap.Int64("provider_id", profile.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)
	p.ObserveModel(upstreamModel)

	// Use the shared Chat transport to build and send the request.
	apiKey, targetURL, err := p.Target(ctx)
	if err != nil {
		return nil, err
	}
	p.Endpoint("/v1/chat/completions")
	resp, err := p.SendCC(ctx, targetURL, chatBody, clientStream, apiKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := p.ReadUpstreamError(resp)
		if foErr := p.FailoverHTTP(ctx, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		return p.ResponsesError(ctx, resp, chatBody, billingModel)
	}

	output := upstream.NewDeferredOutputContext(p.Sink())
	options := p.RawOptions(resp, billingModel, upstreamModel, serviceTier)
	var result *openai.CompatResponseResult
	if clientStream {
		result, err = openai.ReadCCAsResponsesStreaming(output, resp, options, originalModel, upstreamModel, reasoningEffort, startTime, customTools, functionTools, toolSearch, namespaceTools)
	} else {
		result, err = openai.ReadCCAsResponsesBuffered(output, resp, options, originalModel, upstreamModel, reasoningEffort, startTime, customTools, functionTools, toolSearch, namespaceTools)
	}
	return FromCompatResult(result, billingModel), err
}
