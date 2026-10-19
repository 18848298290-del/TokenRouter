package modelcatalog

import (
	"slices"
	"strings"
)

// platform.go applies platform capability patches and protocol adapter
// restrictions to resolved routes.
// Model metadata and request encoding rules have different sources. Catalog
// attributes describe the complete model, while platform patches supply
// service-specific sampling conditions, authentication restrictions, thinking
// encodings, and registered media parameters. These patches run before
// administrator overrides so configured values can replace attribute fields.
//
// Adapter restrictions apply after those overrides because the chosen
// converter still determines which modalities and media parameters it can
// represent. Antigravity converted requests reserve output room for thinking,
// and Ollama cap settings select an output clamping rule. Execution rules
// remain separate from the attribute patches, allowing request encoders to
// interpret aliases, dropped fields, and signatures alongside the resolved
// capability values.

// Platform token defaults supply converted Antigravity thinking/output limits
// and the initial Ollama output parameter cap.
const (
	AntigravityAdaptiveBudget     = 24576 // tokens
	AntigravityDefaultOutputLimit = 64000 // tokens
	OllamaDefaultOutputLimit      = 65535 // tokens
)

// applyPlatform patches attributes and encoding rules for the detected
// service and route. It normalizes metadata lookup spellings locally, while
// retaining the target's outbound model and resolved protocol identifiers.
//
// Service authentication and endpoint variants select sampling, thinking,
// signature, and media behavior independently of catalog prices. Platform
// attribute sources are recorded when the patch is merged; later administrator
// overrides can replace them before adapter restrictions run. Qoder unknown
// routes retain a fixed input limit labeled execution_default, and nonpositive
// Ollama caps leave the output clamping rule disabled.
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
			// These registered identities declare verbosity unavailable. The
			// OpenAI forwarding transformer reads this flag when removing
			// text.verbosity from their request bodies.
			r.Rules.Verbosity = valuePointer(false)
		}
		if t.ServiceID() == "openai" && r.Attributes.Temperature != nil && !*r.Attributes.Temperature {
			// An official OpenAI route with unavailable temperature also
			// restricts top_p, allowing ApplyModelAttributeParameters to
			// remove both sampling parameters from the request.
			patch.TopP = valuePointer(false)
		}
		if t.ServiceID() == "openai" && slices.Contains([]string{"gpt-5.2", "gpt-5.4"}, model) {
			// These registered sampling controls depend on none effort. Keep
			// their request condition so ForRequest evaluates the same model
			// with each supplied reasoning state.
			conditions["temperature"] = ParameterCondition{Efforts: []string{"none"}}
			conditions["top_p"] = ParameterCondition{Efforts: []string{"none"}}
			patch.TopP = valuePointer(true)
		}
		if t.UpstreamProtocol == "openai_chat_completions" && t.ServiceID() == "openai" {
			// Tool capability can differ between Chat and Responses for one
			// model. Apply the registered Chat restriction here so route
			// attributes retain that protocol distinction.
			switch model {
			case "gpt-6-astra", "gpt-6.1-sol":
				patch.ToolCall = valuePointer(false)
			case "gpt-6-sol", "gpt-6-luna":
				conditions["tool_call"] = ParameterCondition{Efforts: []string{"none"}}
			}
		}
		if t.ProviderType == "oauth" || t.ProviderType == "setup-token" {
			// OAuth route encoders use their own output and sampling controls.
			// Mark unsupported attributes and dropped fields so forwarding
			// uses the selected authentication variant.
			patch.Temperature, patch.TopP = valuePointer(false), valuePointer(false)
			r.Rules.OutputParameter = valuePointer(false)
			r.Rules.DropFields = []string{"max_output_tokens", "max_completion_tokens", "frequency_penalty", "presence_penalty"}
			if model == "gpt-5.3-codex-spark" {
				patch.InputModalities = valuePointer([]string{"text"})
				patch.Attachment = valuePointer(false)
				r.Rules.ImageGenerationTool = valuePointer(false)
			}
			if t.Endpoint == "compact" && slices.Contains([]string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}, model) {
				// These registered Compact routes encode max effort as xhigh.
				// Remove max from display choices and retain its request
				// rewrite in EffortAliases.
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
		// Grok model-specific removals accompany its control declarations. The
		// forwarding parameter pass consumes DropFields even when the base
		// catalog contains other capability metadata.
		if slices.Contains([]string{"grok-composer", "grok-composer-2.5-fast", "composer-2.5"}, model) {
			patch.ReasoningOptions = valuePointer([]ReasoningOption{})
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
			// These sampling controls select opposite thinking states. Keep
			// both conditions so ForRequest disables whichever parameter
			// conflicts with the request's evaluated thinking choice.
			conditions["temperature"] = ParameterCondition{Thinking: valuePointer(false)}
			conditions["top_p"] = ParameterCondition{Thinking: valuePointer(true)}
			patch.TopP = valuePointer(true)
		}
	}
	// The Messages protocol selects legacy signature and thinking representations
	// even on a compatible service route. Catalog attributes continue to
	// describe that model's supported controls.
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
			// A positive provider cap narrows the catalog output limit used by
			// ApplyModelAttributeParameters for clamping. Administrator
			// attribute overrides can replace that limit afterward.
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

// antigravityPatch supplies limits and thinking encodings for converted
// Antigravity requests. The Gemini native client protocol leaves these
// conversion limits unset and continues with its catalog capabilities.
//
// Claude identities and Chat requests use the registered 64,000-token limit;
// other converted routes use 65,000 tokens, retaining any smaller known
// catalog limit. Budget padding reserves output room above thinking. Registered
// adaptive Claude models use their fixed thinking budget, and selected Gemini
// thinking identities also restrict sampling, tool configuration, and stop
// sequences for their request transformer.
func (r *Resolution) antigravityPatch(patch *Attributes) {
	model := normalize(r.Target.Model)
	if r.Target.ClientProtocol == "gemini_generate_content" {
		return
	}
	// Converted non-Claude Messages routes use their separate output allowance.
	limit := 65000
	if strings.HasPrefix(model, "claude-") || r.Target.ClientProtocol == "openai_chat_completions" {
		limit = AntigravityDefaultOutputLimit
	}
	patch.OutputLimit = minimum(r.Attributes.OutputLimit, &limit)
	// GeminiThinking subtracts this allowance when bounding positive thinking
	// budgets, retaining output space between the chosen budget and the
	// converted request's limit.
	r.Rules.BudgetPadding = 1000
	if slices.Contains([]string{"claude-opus-4-6", "claude-opus-4-6-thinking", "claude-opus-4-7", "claude-opus-4-7-thinking", "claude-opus-4-8", "claude-opus-4-8-thinking"}, model) {
		r.Rules.AdaptiveThinkingBudget = AntigravityAdaptiveBudget
	}
	if slices.Contains([]string{"gemini-2.5-flash-thinking", "gemini-3-pro-high", "gemini-3.1-pro-high", "gemini-3.6-flash-high", "gemini-3.6-flash-low", "gemini-3.6-flash-medium", "gemini-3.6-flash-tiered"}, model) {
		// The Antigravity request transformer consumes these thinking-route
		// options when preparing Gemini payloads, excluding empty tool
		// configuration and stop sequences from that representation.
		patch.Temperature, patch.TopP = valuePointer(false), valuePointer(false)
		r.Rules.DropFields = append(r.Rules.DropFields, "top_k")
		r.Rules.OmitEmptyToolConfig = true
		r.Rules.OmitStopSequences = true
	}
}

// applyAdapter restricts resolved attributes to the selected converter's
// representations after administrator overrides have been applied. Qoder
// retains text modalities and disables unsupported controls, while cross-text
// conversion intersects known modalities with text and image.
//
// Image and video entries impose their output modalities separately from
// ordinary text conversion. Batch images retain registered 1K sizes, and
// OpenAI OAuth image routes disable input fidelity even when configured true.
// Unknown modality lists remain unknown except for Qoder's fixed text choices;
// every changed attribute receives the adapter source label.
func (r *Resolution) applyAdapter() {
	t := r.Target
	patch := Attributes{}
	if t.Platform == "qoder" {
		// Qoder payloads use text for both directions. Intersect supplied
		// modalities with that fixed representation so an administrator clear
		// stays explicitly empty.
		patch.InputModalities, patch.OutputModalities = valuePointer([]string{"text"}), valuePointer([]string{"text"})
		if r.Attributes.InputModalities != nil {
			patch.InputModalities = intersection(r.Attributes.InputModalities, patch.InputModalities)
		}
		if r.Attributes.OutputModalities != nil {
			patch.OutputModalities = intersection(r.Attributes.OutputModalities, patch.OutputModalities)
		}
		patch.Attachment, patch.Temperature, patch.TopP, patch.StructuredOutput = valuePointer(false), valuePointer(false), valuePointer(false), valuePointer(false)
	} else if t.ClientProtocol != "" && t.UpstreamProtocol != "" && t.ClientProtocol != t.UpstreamProtocol && !strings.Contains(t.ClientProtocol, "images_") && !strings.Contains(t.ClientProtocol, "videos_") && t.ClientProtocol != "image_batches" {
		// Text protocol conversion has a narrower representation than a catalog
		// modality declaration. Restrict known lists after configuration so
		// unsupported audio, video, and PDF choices disappear.
		allowed := []string{"text", "image"}
		if r.Attributes.InputModalities != nil {
			patch.InputModalities = intersection(r.Attributes.InputModalities, &allowed)
		}
		if r.Attributes.OutputModalities != nil {
			patch.OutputModalities = intersection(r.Attributes.OutputModalities, &allowed)
		}
	}
	if t.ClientProtocol == "image_batches" && r.Attributes.Media != nil && r.Attributes.Media.ImageSizes != nil {
		// Batch image conversion registers the 1K size. Intersect the resolved
		// sizes so catalog or administrator choices cannot advertise another
		// batch size through this adapter.
		patch.Media = &MediaCapabilities{ImageSizes: commonStrings(r.Attributes.Media.ImageSizes, []string{"1K"})}
	}
	if strings.Contains(t.ClientProtocol, "images_") && t.Platform == "openai" && (t.ProviderType == "oauth" || t.ProviderType == "setup-token") {
		// The OAuth image path applies its own tool payload. Override
		// configured input fidelity support so PrepareModelAttributeParameters
		// removes the unsupported parameter for this route.
		patch.Media = &MediaCapabilities{InputFidelity: valuePointer(false)}
	}
	// Media client entries need their own output representation after ordinary
	// text conversion handling. Restrict known image outputs to image and
	// known video outputs to video.
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

// claudeThinkingMode selects a registered Claude thinking encoding from
// known reasoning controls and the full model name. Unknown reasoning
// declarations return no encoding; budget controls take precedence over
// adaptive identity selection.
//
// A budget option with effort choices selects adaptive_or_budget, while a
// budget option alone selects budget. When budget controls are absent, only
// registered adaptive identities select an encoding. Vertex date suffixes are
// removed for that encoding selection after their full identities have
// already determined catalog metadata.
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
	// Vertex date and default identities have already selected separate
	// metadata. Strip the date marker here to choose their shared message
	// encoding from the registered family.
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

// removeEffort copies the control list while omitting one effort string from
// effort options. Nil inputs produce an empty list; retained pointer values
// and budget endpoints remain shared until attribute merging clones them.
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

// thinkingSignatureMode selects signature handling for registered Messages
// model families. Passback families retain their thinking payloads, Claude
// names use Anthropic signatures, and unrecognized identities return no
// protocol-specific choice.
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
