package modelcatalog

import (
	"slices"
	"strings"
)

const (
	AntigravityAdaptiveBudget     = 24576
	AntigravityDefaultOutputLimit = 64000
	OllamaDefaultOutputLimit      = 65535
)

// applyPlatform applies service constraints absent from the catalog. The linked document records model
// coverage and sources.
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#model_attributes
func (r *Resolution) applyPlatform() {
	t := r.Target
	model := normalize(t.Model)
	if t.Platform == "gemini" || t.Platform == "antigravity" {
		model = googleResourceModel(model)
	}
	if t.ServiceID() != "" {
		model = strings.TrimPrefix(model, t.ServiceID()+"/")
	}
	patch := Attributes{}
	conditions := ParameterConditions{}
	switch t.servicePlatform() {
	case "openai":
		switch model {
		case "gpt-3.5-turbo", "gpt-4", "gpt-4-turbo", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o", "gpt-4o-2024-05-13", "gpt-4o-2024-08-06", "gpt-4o-2024-11-20", "gpt-4o-mini", "gpt-5.1", "gpt-5.1-codex", "gpt-5.1-codex-mini", "gpt-5.1-codex-max", "gpt-5.2", "gpt-5.2-chat-latest", "gpt-5.2-pro", "gpt-5.2-codex":
			r.Rules.Verbosity = valuePointer(false)
		}
		if t.ServiceID() == "openai" && r.Attributes.Temperature != nil && !*r.Attributes.Temperature {
			patch.TopP = valuePointer(false)
		}
		if t.ServiceID() == "openai" && slices.Contains([]string{"gpt-5.2", "gpt-5.4"}, model) {
			conditions["temperature"] = ParameterCondition{Efforts: []string{"none"}}
			conditions["top_p"] = ParameterCondition{Efforts: []string{"none"}}
			patch.TopP = valuePointer(true)
		}
		if t.UpstreamProtocol == "openai_chat_completions" && t.ServiceID() == "openai" {
			switch model {
			case "gpt-6-astra", "gpt-6.1-sol":
				patch.ToolCall = valuePointer(false)
			case "gpt-6-sol", "gpt-6-luna":
				conditions["tool_call"] = ParameterCondition{Efforts: []string{"none"}}
			}
		}
		if t.ProviderType == "oauth" || t.ProviderType == "setup-token" {
			patch.Temperature, patch.TopP = valuePointer(false), valuePointer(false)
			r.Rules.OutputParameter = valuePointer(false)
			r.Rules.DropFields = []string{"max_output_tokens", "max_completion_tokens", "frequency_penalty", "presence_penalty"}
			if model == "gpt-5.3-codex-spark" {
				patch.InputModalities = valuePointer([]string{"text"})
				patch.Attachment = valuePointer(false)
				r.Rules.ImageGenerationTool = valuePointer(false)
			}
			if t.Endpoint == "compact" && slices.Contains([]string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}, model) {
				if r.Attributes.ReasoningOptions != nil {
					patch.ReasoningOptions = valuePointer(removeEffort(r.Attributes.ReasoningOptions, "max"))
				}
				r.Rules.EffortAliases = map[string]string{"max": "xhigh"}
			}
		}
	case "anthropic":
		if t.ProviderType == "bedrock" {
			r.Rules = BedrockExecutionRules(model)
		}
		if strings.HasPrefix(model, "claude-") || t.ProviderType == "bedrock" || t.ProviderType == "service_account" {
			conditions["temperature"] = ParameterCondition{Thinking: valuePointer(false)}
			r.Rules.SignatureMode = "anthropic"
			if mode := claudeThinkingMode(model, r.Attributes); mode != "" {
				r.Rules.ThinkingMode = mode
			}
		}
	case "antigravity":
		r.antigravityPatch(&patch)
	case "grok":
		if slices.Contains([]string{"grok-composer", "grok-composer-2.5-fast", "composer-2.5"}, model) {
			patch.ReasoningOptions = valuePointer([]ReasoningOption{})
			r.Rules.DropFields = append(r.Rules.DropFields, "reasoning", "reasoning_effort", "reasoningEffort")
		}
		if strings.HasPrefix(model, "grok-4.20") {
			r.Rules.DropFields = append(r.Rules.DropFields, "logprobs", "top_logprobs")
		}
		if model == "grok-4.5" {
			r.Rules.DropFields = append(r.Rules.DropFields, "presence_penalty", "presencePenalty", "frequency_penalty", "frequencyPenalty", "stop")
		}
	case "qoder":
		limit, selectable := QoderInputLimit(t.Site, model)
		patch.InputLimit = valuePointer(limit)
		if _, known := findQoderRoute(t.Site, model); known {
			patch.ReasoningOptions = valuePointer(QoderReasoningOptions(t.Site, model))
		}
		r.Rules.RuntimeContext = selectable
		r.Rules.ThinkingMode = "qoder"
	case "kimi":
		r.Rules.SignatureMode = "passback"
	case "zhipu":
		r.Rules.SignatureMode = "passback"
		if model == "glm-5.3" {
			r.Rules.ThinkingMode = "glm53"
		}
	case "deepseek":
		r.Rules.SignatureMode = "passback"
		if slices.Contains([]string{"deepseek-v4-pro", "deepseek-v4-flash", "deepseek-flash", "deepseek-v4-flash-vision-exp"}, model) {
			conditions["temperature"] = ParameterCondition{Thinking: valuePointer(false)}
			conditions["top_p"] = ParameterCondition{Thinking: valuePointer(true)}
			patch.TopP = valuePointer(true)
		}
	}
	// Use the implemented protocol family for legacy thinking encodings; model capabilities still come from the
	// catalog.
	if t.UpstreamProtocol == "anthropic_messages" {
		if signature := thinkingSignatureMode(model); signature != "" {
			r.Rules.SignatureMode = signature
		}
		if strings.HasPrefix(model, "minimax-m") {
			r.Rules.ThinkingMode = "adaptive"
		}
	}
	if len(conditions) > 0 {
		patch.Conditions = &conditions
	}
	if t.Ollama {
		cap := OllamaDefaultOutputLimit
		if t.OllamaOutputCap != nil {
			cap = *t.OllamaOutputCap
		}
		if cap > 0 {
			r.Rules.ClampOutput = true
			patch.OutputLimit = minimum(r.Attributes.OutputLimit, &cap)
		}
	}
	patch.Media = MediaForModel(t.servicePlatform(), model)
	r.patch(patch, "platform")
	if t.Platform == "qoder" {
		if _, found := findQoderRoute(t.Site, t.Model); !found {
			r.Sources["input_limit"] = "execution_default"
		}
	}
}

func (r *Resolution) antigravityPatch(patch *Attributes) {
	model := normalize(r.Target.Model)
	if r.Target.ClientProtocol == "gemini_generate_content" {
		return
	}
	limit := 65000
	if strings.HasPrefix(model, "claude-") || r.Target.ClientProtocol == "openai_chat_completions" {
		limit = AntigravityDefaultOutputLimit
	}
	patch.OutputLimit = minimum(r.Attributes.OutputLimit, &limit)
	r.Rules.BudgetPadding = 1000
	if slices.Contains([]string{"claude-opus-4-6", "claude-opus-4-6-thinking", "claude-opus-4-7", "claude-opus-4-7-thinking", "claude-opus-4-8", "claude-opus-4-8-thinking"}, model) {
		r.Rules.AdaptiveThinkingBudget = AntigravityAdaptiveBudget
	}
	if slices.Contains([]string{"gemini-2.5-flash-thinking", "gemini-3-pro-high", "gemini-3.1-pro-high", "gemini-3.6-flash-high", "gemini-3.6-flash-low", "gemini-3.6-flash-medium", "gemini-3.6-flash-tiered"}, model) {
		patch.Temperature, patch.TopP = valuePointer(false), valuePointer(false)
		r.Rules.DropFields = append(r.Rules.DropFields, "top_k")
		r.Rules.OmitEmptyToolConfig = true
		r.Rules.OmitStopSequences = true
	}
}

// applyAdapter limits attributes to formats the current converter can encode, including administrator overrides.
func (r *Resolution) applyAdapter() {
	t := r.Target
	patch := Attributes{}
	if t.Platform == "qoder" {
		patch.InputModalities, patch.OutputModalities = valuePointer([]string{"text"}), valuePointer([]string{"text"})
		if r.Attributes.InputModalities != nil {
			patch.InputModalities = intersection(r.Attributes.InputModalities, patch.InputModalities)
		}
		if r.Attributes.OutputModalities != nil {
			patch.OutputModalities = intersection(r.Attributes.OutputModalities, patch.OutputModalities)
		}
		patch.Attachment, patch.Temperature, patch.TopP, patch.StructuredOutput = valuePointer(false), valuePointer(false), valuePointer(false), valuePointer(false)
	} else if t.ClientProtocol != "" && t.UpstreamProtocol != "" && t.ClientProtocol != t.UpstreamProtocol && !strings.Contains(t.ClientProtocol, "images_") && !strings.Contains(t.ClientProtocol, "videos_") && t.ClientProtocol != "image_batches" {
		allowed := []string{"text", "image"}
		if r.Attributes.InputModalities != nil {
			patch.InputModalities = intersection(r.Attributes.InputModalities, &allowed)
		}
		if r.Attributes.OutputModalities != nil {
			patch.OutputModalities = intersection(r.Attributes.OutputModalities, &allowed)
		}
	}
	if t.ClientProtocol == "image_batches" && r.Attributes.Media != nil && r.Attributes.Media.ImageSizes != nil {
		patch.Media = &MediaCapabilities{ImageSizes: commonStrings(r.Attributes.Media.ImageSizes, []string{"1K"})}
	}
	if strings.Contains(t.ClientProtocol, "images_") && t.Platform == "openai" && (t.ProviderType == "oauth" || t.ProviderType == "setup-token") {
		patch.Media = &MediaCapabilities{InputFidelity: valuePointer(false)}
	}
	if strings.Contains(t.ClientProtocol, "images_") || t.ClientProtocol == "image_batches" {
		if r.Attributes.InputModalities != nil {
			patch.InputModalities = intersection(r.Attributes.InputModalities, valuePointer([]string{"text", "image"}))
		}
		if r.Attributes.OutputModalities != nil {
			patch.OutputModalities = intersection(r.Attributes.OutputModalities, valuePointer([]string{"image"}))
		}
	} else if strings.Contains(t.ClientProtocol, "videos_") {
		if r.Attributes.OutputModalities != nil {
			patch.OutputModalities = intersection(r.Attributes.OutputModalities, valuePointer([]string{"video"}))
		}
	}
	r.patch(patch, "adapter")
}

func claudeThinkingMode(model string, attrs Attributes) string {
	if attrs.Reasoning == nil && attrs.ReasoningOptions == nil {
		return ""
	}
	if attrs.HasReasoningOption("budget_tokens") {
		if attrs.HasReasoningOption("effort") {
			return "adaptive_or_budget"
		}
		return "budget"
	}
	// Vertex date and default IDs have already been looked up separately; this selects their shared message encoding.
	identity, _, _ := strings.Cut(model, "@")
	switch identity {
	case "claude-sonnet-5-5":
		return "adaptive_between_tools"
	case "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-opus-5-5", "claude-sonnet-5", "claude-haiku-5-5":
		return "adaptive"
	default:
		return ""
	}
}

func removeEffort(options *[]ReasoningOption, effort string) []ReasoningOption {
	result := []ReasoningOption{}
	if options == nil {
		return result
	}
	for _, option := range *options {
		if option.Type == "effort" {
			option.Values = slices.DeleteFunc(slices.Clone(option.Values), func(value *string) bool { return value != nil && *value == effort })
		}
		result = append(result, option)
	}
	return result
}

func thinkingSignatureMode(model string) string {
	switch {
	case strings.HasPrefix(model, "deepseek-"), strings.HasPrefix(model, "kimi-"), strings.HasPrefix(model, "moonshot-"), strings.HasPrefix(model, "glm-"), model == "k3", model == "k3-256k", strings.HasPrefix(model, "minimax-m"):
		return "passback"
	case strings.Contains(model, "-thinking") && (strings.HasPrefix(model, "qwen-") || strings.HasPrefix(model, "qwen2-") || strings.HasPrefix(model, "qwen3-") || strings.HasPrefix(model, "qwen4-")):
		return "passback"
	case strings.HasPrefix(model, "claude-"), strings.HasPrefix(model, "opus-"), strings.HasPrefix(model, "sonnet-"), strings.HasPrefix(model, "haiku-"):
		return "anthropic"
	default:
		return ""
	}
}
