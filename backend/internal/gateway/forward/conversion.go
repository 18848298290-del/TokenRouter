package forward

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// AsResponses 依次转换请求、取得凭据、调用上游并输出 Responses 响应。
func AsResponses(ctx context.Context, p ConversionPorts, in ConversionInput, body []byte) (*Result, error) {
	startTime := time.Now()

	normalizedBody, normalized, err := p.NormalizeResponses(body)
	if err != nil {
		return nil, err
	}
	if normalized {
		body = normalizedBody
	}

	adaptedBody, clientToolMapping, err := AdaptResponsesClientToolsForAnthropic(body)
	if err != nil {
		return nil, fmt.Errorf("adapt responses client tools: %w", err)
	}

	var responsesReq protocolopenai.ResponsesRequest
	if err := json.Unmarshal(adaptedBody, &responsesReq); err != nil {
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := responsesReq.Model
	clientStream := responsesReq.Stream

	anthropicReq, err := bridge.ResponsesToAnthropicRequest(&responsesReq)
	if err != nil {
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}

	anthropicReq.Stream = true
	reqStream := true

	// 4. 模型映射：分组映射已由 handler 写入 body，此处继续执行提供商映射和平台规范化。
	mappedModel := p.ResolveModel(ctx, originalModel)
	if mappedModel == "" {
		mappedModel = originalModel
	}
	reasoningEffort := p.Effort(body, false, mappedModel, originalModel)
	// 按 Anthropic 出站档位记录，不能用 OpenAI 模型能力过滤掉 Claude 的 max。
	if anthropicReq.OutputConfig != nil {
		reasoningEffort = protocolcore.NormalizeClaudeOutputEffort(anthropicReq.OutputConfig.Effort)
	}
	// 国产模型缺少请求 effort 时，thinking 开启后补入默认展示档位。
	reasoningEffort = p.ThinkingFallback(reasoningEffort, body, mappedModel)
	anthropicReq.Model = mappedModel

	p.ModelNotice("gateway forward_as_responses: model mapping applied", originalModel, mappedModel, clientStream)

	anthropicBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	// Responses 入口的 OAuth 转发执行与 Messages 相同的 system 改写、规范化和 metadata 注入。
	// Anthropic 按客户端身份区分订阅额度和 extra usage，身份处理见 applyClaudeCodeOAuthMimicryToBody。
	shouldMimicClaudeCode := in.OAuth

	if shouldMimicClaudeCode {
		anthropicBody = p.Mimic(ctx, anthropicBody, anthropicReq.System, mappedModel)
	}

	anthropicBody = p.CacheLimit(anthropicBody)

	if err := p.Credential(ctx); err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}
	wireBody, err := p.Build(ctx, anthropicBody, mappedModel, reqStream, shouldMimicClaudeCode)
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	resp, err := p.Send(ctx)
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := p.ReadErrorBody()
		upstreamMsg := p.ErrorMessage(respBody)
		decision := p.Health(ctx, resp.StatusCode, respBody, mappedModel)

		if decision.Generic {
			p.Output().Error(500, "server_error", "Upstream gateway error")
			return nil, fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}

		if decision.Failover {
			p.FailoverNotice(resp.StatusCode, upstreamMsg)
			return nil, p.FailoverError(resp.StatusCode, respBody, decision.RetrySameProvider)
		}

		p.Output().Error(MapStatus(resp.StatusCode), "server_error", upstreamMsg)
		return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
	}

	var result *Result
	var handleErr error
	if clientStream {
		result, handleErr = ResponsesStreaming(resp, p.Output(), originalModel, mappedModel, reasoningEffort, startTime, clientToolMapping)
	} else {
		result, handleErr = ResponsesBuffered(resp, p.Output(), originalModel, mappedModel, reasoningEffort, startTime, clientToolMapping)
	}

	if result != nil && strings.TrimSpace(result.Usage.Speed) == "" &&
		strings.EqualFold(strings.TrimSpace(gjson.GetBytes(wireBody, "speed").String()), "fast") {
		result.Usage.Speed = "fast"
	}
	return result, handleErr
}

// AsChat 依次转换请求、取得凭据、调用上游并输出 Chat 响应。
func AsChat(ctx context.Context, p ConversionPorts, in ConversionInput, body []byte) (*Result, error) {
	startTime := time.Now()

	var ccReq protocolopenai.ChatCompletionsRequest
	if err := json.Unmarshal(body, &ccReq); err != nil {
		return nil, fmt.Errorf("parse chat completions request: %w", err)
	}
	originalModel := ccReq.Model
	clientStream := ccReq.Stream
	includeUsage := ccReq.StreamOptions != nil && ccReq.StreamOptions.IncludeUsage

	responsesReq, err := bridge.ChatCompletionsToResponses(&ccReq, bridge.RequestOptions{SupportsMaxEffort: true})
	if err != nil {
		return nil, fmt.Errorf("convert chat completions to responses: %w", err)
	}

	anthropicReq, err := bridge.ResponsesToAnthropicRequest(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}

	anthropicReq.Stream = true
	reqStream := true

	// 4. 模型映射：分组映射已由 handler 写入 body，此处继续执行提供商映射和平台规范化。
	mappedModel := p.ResolveModel(ctx, originalModel)
	if mappedModel == "" {
		mappedModel = originalModel
	}
	anthropicReq.Model = mappedModel

	p.ModelNotice("gateway forward_as_chat_completions: model mapping applied", originalModel, mappedModel, clientStream)

	anthropicBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	// Chat Completions 入口的 OAuth 转发执行与 Messages 相同的 system 改写、规范化和 metadata 注入。
	// Anthropic 按客户端身份区分订阅额度和 extra usage，身份处理见 applyClaudeCodeOAuthMimicryToBody。
	shouldMimicClaudeCode := in.OAuth

	if shouldMimicClaudeCode {
		anthropicBody = p.Mimic(ctx, anthropicBody, anthropicReq.System, mappedModel)
	}

	anthropicBody = p.CacheLimit(anthropicBody)

	if err := p.Credential(ctx); err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}
	wireBody, err := p.Build(ctx, anthropicBody, mappedModel, reqStream, shouldMimicClaudeCode)
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	resp, err := p.Send(ctx)
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := p.ReadErrorBody()
		upstreamMsg := p.ErrorMessage(respBody)
		decision := p.Health(ctx, resp.StatusCode, respBody, mappedModel)

		if decision.Generic {
			p.Output().Error(500, "server_error", "Upstream gateway error")
			return nil, fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}

		if decision.Failover {
			p.FailoverNotice(resp.StatusCode, upstreamMsg)
			return nil, p.FailoverError(resp.StatusCode, respBody, decision.RetrySameProvider)
		}

		p.Output().Error(MapStatus(resp.StatusCode), "server_error", upstreamMsg)
		return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
	}

	reasoningEffort := p.Effort(body, true, mappedModel, originalModel)
	// 按 Anthropic 出站档位记录，不能用 OpenAI 模型能力过滤掉 Claude 的 max。
	if anthropicReq.OutputConfig != nil {
		reasoningEffort = protocolcore.NormalizeClaudeOutputEffort(anthropicReq.OutputConfig.Effort)
	}
	// 国产模型缺少请求 effort 时，thinking 开启后补入默认展示档位。
	reasoningEffort = p.ThinkingFallback(reasoningEffort, body, mappedModel)

	var result *Result
	var handleErr error
	if clientStream {
		result, handleErr = ChatStreaming(resp, p.Output(), originalModel, mappedModel, reasoningEffort, startTime, includeUsage)
	} else {
		result, handleErr = ChatBuffered(resp, p.Output(), originalModel, mappedModel, reasoningEffort, startTime)
	}

	if result != nil && strings.TrimSpace(result.Usage.Speed) == "" &&
		strings.EqualFold(strings.TrimSpace(gjson.GetBytes(wireBody, "speed").String()), "fast") {
		result.Usage.Speed = "fast"
	}
	return result, handleErr
}

func AdaptResponsesClientToolsForAnthropic(body []byte) ([]byte, bridge.ResponsesClientToolMapping, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var requestBody map[string]any
	if err := decoder.Decode(&requestBody); err != nil {
		return body, bridge.ResponsesClientToolMapping{}, err
	}

	additionalToolsChanged, err := LiftResponsesAdditionalTools(requestBody)
	if err != nil {
		return body, bridge.ResponsesClientToolMapping{}, err
	}
	mapping, changed, err := bridge.AdaptResponsesClientTools(requestBody)
	if err != nil {
		return body, bridge.ResponsesClientToolMapping{}, err
	}
	changed = changed || additionalToolsChanged
	if !changed {
		return body, mapping, nil
	}

	rebuilt, err := json.Marshal(requestBody)
	if err != nil {
		return body, bridge.ResponsesClientToolMapping{}, err
	}
	return rebuilt, mapping, nil
}

func LiftResponsesAdditionalTools(requestBody map[string]any) (bool, error) {
	input, ok := requestBody["input"].([]any)
	if !ok {
		return false, nil
	}

	tools, _ := requestBody["tools"].([]any)
	kept := make([]any, 0, len(input))
	changed := false
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(fmt.Sprint(item["type"])) != "additional_tools" {
			kept = append(kept, raw)
			continue
		}
		additional, ok := item["tools"].([]any)
		if !ok {
			return false, fmt.Errorf("additional_tools.tools must be an array")
		}
		tools = append(tools, additional...)
		changed = true
	}
	if !changed {
		return false, nil
	}
	requestBody["tools"] = tools
	requestBody["input"] = kept
	return true, nil
}

// ConversionOptionsForModel 在目标尚未确定时保存请求参数，最终能力由执行适配器解析。
func ConversionOptionsForModel(_ string) bridge.RequestOptions {
	return bridge.RequestOptions{
		SupportsMaxEffort: true,
	}
}

// ExtractEffort 优先读取 reasoning.effort，Chat 请求缺少该字段时读取 reasoning_effort。
// 模型和档位由调用方提供的 normalize 函数规范化。
func ExtractEffort(body []byte, chat bool, normalize func(string, string) string, models ...string) *string {
	raw := strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())
	if raw == "" && chat {
		raw = strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
	}
	if raw == "" {
		return nil
	}
	model := ""
	for _, candidate := range models {
		if value := strings.TrimSpace(candidate); value != "" {
			model = value
			break
		}
	}
	if model == "" {
		model = strings.TrimSpace(gjson.GetBytes(body, "model").String())
	}
	value := normalize(raw, model)
	if value == "" {
		return nil
	}
	return &value
}
