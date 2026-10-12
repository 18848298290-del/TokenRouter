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
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream/ollama"
)

// ResolveModelAttributes reads the request's catalogue and group settings after the final model is known.
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

// ModelConversionOptions supplies resolved capabilities to protocol converters; unknown capabilities allow the parameter.
func ModelConversionOptions(result modelcatalog.Resolution) bridge.RequestOptions {
	attrs := result.Attributes
	max := attrs.EffortSupport("max")
	return bridge.RequestOptions{
		DropTemperature:   attrs.Temperature != nil && !*attrs.Temperature,
		DropTopP:          attrs.TopP != nil && !*attrs.TopP,
		SupportsMaxEffort: max == nil || *max,
	}
}

// GeminiThinking converts client controls using the final model's supported Gemini options.
func GeminiThinking(result modelcatalog.Resolution, body []byte) (*bridge.GeminiThinkingConfig, error) {
	attrs := result.Attributes
	minimum, maximum := attrs.ReasoningBudget()
	options := bridge.GeminiThinkingOptions{
		ControlsKnown: attrs.ReasoningOptions != nil,
		Reasoning:     attrs.Reasoning, Toggle: attrs.HasReasoningOption("toggle"),
		Efforts: attrs.Efforts(), Budget: attrs.HasReasoningOption("budget_tokens"),
		BudgetMin: minimum, BudgetMax: maximum,
		ForceBudget:    result.Target.Platform == "antigravity" && strings.HasPrefix(result.Target.Model, "claude-"),
		AdaptiveBudget: result.Rules.AdaptiveThinkingBudget, OutputLimit: attrs.OutputLimit, BudgetPadding: result.Rules.BudgetPadding,
	}
	return bridge.ResolveGeminiThinking(bridge.ParseGeminiThinkingInput(body), options)
}

// ApplyModelAttributeParameters applies the resolved route's parameter rules to the body.
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
	if strings.HasPrefix(strings.ToLower(result.Target.Model), "glm-") {
		options := thinkingRequestOptions(result.Target.Model)
		if result.Attributes.ReasoningOptions != nil {
			options.Efforts = append([]string{}, result.Attributes.Efforts()...)
		}
		if result.Target.UpstreamProtocol == "anthropic_messages" && options.GLM53 {
			body, _ = requeststate.NormalizeGLM53AnthropicThinking(body, options)
		} else if result.Attributes.HasReasoningOption("effort") {
			body, _ = requeststate.NormalizeGLMOpenAIReasoningEffort(body, options)
		}
	}
	if (result.Rules.ThinkingMode == "adaptive" || result.Rules.ThinkingMode == "adaptive_between_tools") && gjson.GetBytes(body, "thinking.type").String() == "enabled" {
		body, err = sjson.SetBytes(body, "thinking.type", "adaptive")
		if err == nil {
			body, err = sjson.DeleteBytes(body, "thinking.budget_tokens")
		}
	}
	if err == nil && (result.Target.Platform == "grok" || result.Target.ServiceID() == "xai") {
		codec := GrokBodyCodec()
		codec.Attributes = func(string) modelcatalog.Attributes { return result.Attributes }
		if result.Target.UpstreamProtocol == "openai_chat_completions" {
			return codec.NormalizeGrokChatReasoningEffort(body, result.Target.Model)
		}
		return codec.NormalizeGrokResponsesReasoningEffort(body, result.Target.Model)
	}
	return body, err
}

// ModelRequestState reads reasoning controls after group policies, including Gemini JSON field aliases.
func ModelRequestState(body []byte) modelcatalog.RequestState {
	var reasoning, effort, output, thinking, camelConfig, snakeConfig gjson.Result
	// Read the controls in one pass so large inputs are skipped once. Each
	// path keeps its first match, including null, as gjson.GetBytes does.
	wirejson.ParseView(body).ForEach(func(key, value gjson.Result) bool {
		var field *gjson.Result
		switch key.Str {
		case "reasoning":
			field, value = &reasoning, value.Get("effort")
		case "reasoning_effort":
			field = &effort
		case "output_config":
			field, value = &output, value.Get("effort")
		case "thinking":
			field, value = &thinking, value.Get("type")
		case "generationConfig":
			field = &camelConfig
		case "generation_config":
			field = &snakeConfig
		}
		if field != nil && !field.Exists() {
			*field = value
		}
		return true
	})
	state := modelcatalog.RequestState{}
	for _, value := range []gjson.Result{reasoning, effort, output} {
		if value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
			effort := strings.Clone(strings.ToLower(strings.TrimSpace(value.String())))
			state.Effort = &effort
			thinking := effort != "none"
			state.Thinking = &thinking
			break
		}
	}
	if thinking.Type == gjson.String {
		switch thinking.String() {
		case "enabled", "adaptive", "between_tools":
			thinking := true
			state.Thinking = &thinking
		case "disabled", "off", "none":
			thinking := false
			state.Thinking = &thinking
		}
	}
	for _, config := range []gjson.Result{camelConfig, snakeConfig} {
		for _, thinkingName := range []string{"thinkingConfig", "thinking_config"} {
			thinkingConfig := config.Get(thinkingName)
			for _, key := range []string{"thinkingLevel", "thinking_level"} {
				if level := thinkingConfig.Get(key); level.Type == gjson.String {
					effort := strings.Clone(strings.ToLower(level.String()))
					state.Effort = &effort
					thinking := effort != "none"
					state.Thinking = &thinking
				}
			}
			for _, key := range []string{"thinkingBudget", "thinking_budget"} {
				if budget := thinkingConfig.Get(key); budget.Type == gjson.Number {
					thinking := budget.Int() != 0
					state.Thinking = &thinking
				}
			}
		}
	}
	return state
}

// PrepareModelAttributeParameters applies the request's attribute snapshot to the final model and image tool models.
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

// PrepareGrokResponses applies final-route parameters and builds a body shared by cache identity and forwarding.
func PrepareGrokResponses(ctx context.Context, provider *ExecutionProvider, model string, body []byte, service *routing.ModelAttributeService) ([]byte, error) {
	resolution, err := ResolveModelAttributes(ctx, provider, model, service, "openai_responses")
	if err != nil {
		return nil, err
	}
	body, err = ApplyModelAttributeParameters(body, resolution)
	if err != nil {
		return nil, err
	}
	codec := GrokBodyCodec()
	codec.Attributes = func(string) modelcatalog.Attributes { return resolution.Attributes }
	return codec.PatchGrokResponsesBody(body, model)
}
