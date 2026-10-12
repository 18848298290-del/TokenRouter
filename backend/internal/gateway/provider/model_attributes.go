package provider

import (
	"context"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/ollama"
)

// ResolveModelAttributes 在最终模型确定后读取本次请求固定的目录与分组配置。
func ResolveModelAttributes(ctx context.Context, value *ExecutionProvider, model string, service *routing.ModelAttributeService, protocols ...string) (modelcatalog.Resolution, error) {
	upstreamProtocol := ""
	if value != nil {
		upstreamProtocol = string(value.Route.Protocol())
	}
	if len(protocols) > 0 {
		upstreamProtocol = protocols[0]
	}
	target := ExecutionProtocolRecord(value).ModelAttributeTarget(model, upstreamProtocol)
	if source, ok := requeststate.ClientProtocolFromContext(ctx); ok {
		target.ClientProtocol = string(source)
	}

	var groupID int64
	if group, ok := requeststate.GroupFromContext(ctx); ok && group != nil {
		groupID = group.ID
	}
	session := routing.ModelAttributesFromContext(ctx)
	if session == nil {
		session = service.NewSession()
	}
	return session.Resolve(ctx, groupID, target)
}

func ExecutionModelAttributes(value *ExecutionProvider, model string) modelcatalog.Attributes {
	if value != nil && value.Capabilities != nil && value.Capabilities.Target.Model == model {
		return value.Capabilities.Attributes
	}
	return modelcatalog.Attributes{}
}

// ModelConversionOptions 将已解析能力交给纯协议转换器，未知参数保持原样。
func ModelConversionOptions(result modelcatalog.Resolution) bridge.RequestOptions {
	attrs := result.Attributes
	max := attrs.EffortSupport("max")
	return bridge.RequestOptions{
		DropTemperature:   attrs.Temperature != nil && !*attrs.Temperature,
		DropTopP:          attrs.TopP != nil && !*attrs.TopP,
		SupportsMaxEffort: max == nil || *max,
	}
}

// GeminiThinking 使用本次最终模型的控制集合，将客户端选项转换为 Gemini 参数。
func GeminiThinking(result modelcatalog.Resolution, body []byte) (*bridge.GeminiThinkingConfig, error) {
	attrs := result.Attributes
	minimum, maximum := attrs.ReasoningBudget()
	options := bridge.GeminiThinkingOptions{
		Known:     attrs.ReasoningOptions != nil || attrs.Reasoning != nil,
		Reasoning: attrs.Reasoning, Toggle: attrs.HasReasoningOption("toggle"),
		Efforts: attrs.Efforts(), Budget: attrs.HasReasoningOption("budget_tokens"),
		BudgetMin: minimum, BudgetMax: maximum,
		ForceBudget:    result.Target.Platform == "antigravity" && strings.HasPrefix(result.Target.Model, "claude-"),
		AdaptiveBudget: result.Rules.AdaptiveThinkingBudget, OutputLimit: attrs.OutputLimit, BudgetPadding: result.Rules.BudgetPadding,
	}
	return bridge.ResolveGeminiThinking(bridge.ParseGeminiThinkingInput(body), options)
}

// ApplyModelAttributeParameters 在已有参数准备阶段应用本次路线的能力值。
func ApplyModelAttributeParameters(body []byte, result modelcatalog.Resolution) ([]byte, error) {
	result = result.ForRequest(ModelRequestState(body))
	remove := append([]string(nil), result.Rules.DropFields...)
	if value := result.Attributes.Temperature; value != nil && !*value {
		remove = append(remove, "temperature")
	}
	if value := result.Attributes.TopP; value != nil && !*value {
		remove = append(remove, "top_p")
	}
	if result.Rules.ClampOutput && result.Attributes.OutputLimit != nil {
		body, _ = ollama.ClampMaxTokens(body, int64(*result.Attributes.OutputLimit))
	}
	var err error
	for _, field := range remove {
		if gjson.GetBytes(body, field).Exists() {
			body, err = sjson.DeleteBytes(body, field)
			if err != nil {
				return nil, err
			}
		}
	}
	for _, path := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
		effort := gjson.GetBytes(body, path)
		if mapped := result.Rules.EffortAliases[effort.String()]; effort.Exists() && mapped != "" {
			body, err = sjson.SetBytes(body, path, mapped)
			if err != nil {
				return nil, err
			}
		}
	}
	if result.Target.Platform == "openai" && result.Target.ServiceID() == "" && !result.Attributes.HasReasoningOption("toggle") {
		if supported := result.Attributes.EffortSupport("none"); supported != nil && !*supported {
			for _, path := range []string{"reasoning.effort", "reasoning_effort"} {
				if gjson.GetBytes(body, path).String() == "none" {
					body, err = sjson.DeleteBytes(body, path)
					if err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(result.Target.Model), "glm-") && result.Attributes.HasReasoningOption("effort") {
		options := thinkingRequestOptions(result.Target.Model)
		if result.Target.UpstreamProtocol == "anthropic_messages" && options.GLM53 {
			body, _ = requeststate.NormalizeGLM53AnthropicThinking(body, true)
		} else {
			body, _ = requeststate.NormalizeGLMOpenAIReasoningEffort(body, options)
		}
	}
	if (result.Rules.ThinkingMode == "adaptive" || result.Rules.ThinkingMode == "adaptive_between_tools") && gjson.GetBytes(body, "thinking.type").String() == "enabled" {
		body, err = sjson.SetBytes(body, "thinking.type", "adaptive")
		if err == nil {
			body, err = sjson.DeleteBytes(body, "thinking.budget_tokens")
		}
	}
	return body, err
}

// ModelRequestState 从实际准备发送的请求中读取已应用分组策略的推理状态。
func ModelRequestState(body []byte) modelcatalog.RequestState {
	state := modelcatalog.RequestState{}
	for _, path := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
		value := gjson.GetBytes(body, path)
		if value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
			effort := strings.ToLower(strings.TrimSpace(value.String()))
			state.Effort = &effort
			thinking := effort != "none"
			state.Thinking = &thinking
			break
		}
	}
	if value := gjson.GetBytes(body, "thinking.type"); value.Type == gjson.String {
		switch value.String() {
		case "enabled", "adaptive", "between_tools":
			thinking := true
			state.Thinking = &thinking
		case "disabled", "off", "none":
			thinking := false
			state.Thinking = &thinking
		}
	}
	for _, prefix := range []string{"generationConfig.thinkingConfig.", "generation_config.thinking_config."} {
		levelKey, budgetKey := "thinkingLevel", "thinkingBudget"
		if strings.HasPrefix(prefix, "generation_config") {
			levelKey, budgetKey = "thinking_level", "thinking_budget"
		}
		if level := gjson.GetBytes(body, prefix+levelKey); level.Type == gjson.String {
			effort := strings.ToLower(level.String())
			state.Effort = &effort
			thinking := effort != "none"
			state.Thinking = &thinking
		}
		if budget := gjson.GetBytes(body, prefix+budgetKey); budget.Type == gjson.Number {
			thinking := budget.Int() != 0
			state.Thinking = &thinking
		}
	}
	return state
}

// PrepareModelAttributeParameters 在模型与工具模型确定后应用本次请求的属性快照。
func PrepareModelAttributeParameters(ctx context.Context, value *ExecutionProvider, model string, body []byte, service *routing.ModelAttributeService, protocols ...string) ([]byte, error) {
	if routing.ModelAttributesFromContext(ctx) == nil {
		ctx = routing.WithModelAttributes(ctx, service)
	}
	result, err := ResolveModelAttributes(ctx, value, model, service, protocols...)
	if err != nil {
		return nil, err
	}
	body, err = ApplyModelAttributeParameters(body, result)
	if err != nil || result.Target.Platform != "openai" {
		return body, err
	}
	for index, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() != "image_generation" || !tool.Get("input_fidelity").Exists() {
			continue
		}
		toolModel := tool.Get("model").String()
		if toolModel == "" {
			continue
		}
		image, err := ResolveModelAttributes(ctx, value, toolModel, service, protocols...)
		if err != nil {
			return nil, err
		}
		media := image.Attributes.Media
		if media != nil && media.InputFidelity != nil && !*media.InputFidelity {
			body, err = sjson.DeleteBytes(body, "tools."+strconv.Itoa(index)+".input_fidelity")
			if err != nil {
				return nil, err
			}
		}
	}
	return body, nil
}
