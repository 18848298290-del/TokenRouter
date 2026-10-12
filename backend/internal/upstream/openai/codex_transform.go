package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

const (
	ImagesResponsesMainModel = "gpt-5.4-mini"

	CodexCallIDMaxLength = 64
	CodexCallIDPrefix    = "fc_"

	CodexImageGenerationFunctionToolName = "image_gen.imagegen"

	CodexImageGenerationBridgeMarker = "<tokenrouter-codex-image-generation>"
	CodexImageGenerationBridgeText   = CodexImageGenerationBridgeMarker + "\nWhen the user asks for raster image generation or editing, use the OpenAI Responses native `image_generation` tool attached to this request. The local Codex client may not expose an `image_gen` namespace, but that does not mean image generation is unavailable. Do not ask the user to switch to CLI fallback solely because `image_gen` is absent.\n</tokenrouter-codex-image-generation>"
	CodexSparkImageUnsupportedMarker = "<tokenrouter-codex-spark-image-unsupported>"
	CodexSparkImageUnsupportedText   = CodexSparkImageUnsupportedMarker + "\nThe current model is gpt-5.3-codex-spark, which does not support image generation, image editing, image input, the `image_generation` tool, or Codex `image_gen`/`$imagegen` workflows. If the user asks for image generation or image editing, clearly explain this model limitation and ask them to switch to a non-Spark Codex model such as gpt-5.3-codex or gpt-5.4. Do not claim that the local environment merely lacks image_gen tooling, and do not suggest CLI fallback as the primary fix while the model remains Spark.\n</tokenrouter-codex-spark-image-unsupported>"
)

var (
	OpenAIChatGPTInternalUnsupportedFields = []string{
		"chat_template_kwargs",
		"user",
		"metadata",
		"prompt_cache_retention",
		"safety_identifier",
		"stream_options",
		"truncation",
		"stop_sequences",
	}

	OpenAICodexOAuthUnsupportedFields = append([]string{
		"max_output_tokens",
		"max_completion_tokens",
		"temperature",
		"top_p",
		"frequency_penalty",
		"presence_penalty",
	}, OpenAIChatGPTInternalUnsupportedFields...)
)

type CodexTransformResult struct {
	Modified        bool
	NormalizedModel string
	PromptCacheKey  string
	ToolNameReverse map[string]string
	Error           error
}

type CodexOAuthTransformOptions struct {
	Attributes                          *modelcatalog.Attributes
	Rules                               *modelcatalog.ExecutionRules
	IsMessagesBridge                    func(map[string]any) bool
	IsCodexCLI                          bool
	IsCompact                           bool
	SkipDefaultInstructions             bool
	PreserveToolCallIDs                 bool
	OmitPromotedSystemMessagesFromInput bool
}

type CodexInputFilterOptions struct {
	PreserveReferences bool
	PreserveCallIDs    bool
}

func NormalizeCodexCallID(id string) string {
	return NormalizeCodexCallIDForItemType("function_call", id)
}

func NormalizeCodexCallIDForItemType(itemType, id string) string {
	prefix := protocolopenai.OpenAIResponsesToolCallIDPrefix(itemType) + "_"
	candidate := id
	switch {
	case id == "":
		return ""
	case strings.HasPrefix(id, strings.TrimSuffix(prefix, "_")):
	case strings.HasPrefix(id, "call_"):
		candidate = prefix + strings.TrimPrefix(id, "call_")
	default:
		candidate = prefix + TrimOpenAIResponsesKnownCallIDPrefix(id)
	}
	if len(candidate) <= CodexCallIDMaxLength {
		return candidate
	}
	return CompactCodexCallIDForItemType(itemType, candidate)
}

func CompactCodexCallIDForItemType(itemType, id string) string {
	prefix := protocolopenai.OpenAIResponsesToolCallIDPrefix(itemType) + "_"
	// Keep the hash domain stable so existing calls still match their results.
	digest := sha256.Sum256([]byte("sub2api:codex-call-id:v1:" + id))
	encoded := hex.EncodeToString(digest[:])
	return prefix + encoded[:CodexCallIDMaxLength-len(prefix)]
}

func TrimOpenAIResponsesKnownCallIDPrefix(id string) string {
	for _, prefix := range []string{"fc_", "ctc_", "tsc_"} {
		if after, ok := strings.CutPrefix(id, prefix); ok {
			return after
		}
	}
	return id
}

func ApplyCodexOAuthTransformWithOptions(reqBody map[string]any, opts CodexOAuthTransformOptions) CodexTransformResult {
	result := CodexTransformResult{}
	if protocolopenai.NormalizeOpenAIOAuthResponsesCompatibilityFields(reqBody) {
		result.Modified = true
	}
	// Tool continuation determines which input references survive filtering.
	needsToolContinuation := protocolopenai.NeedsToolContinuation(reqBody)

	model := ""
	if v, ok := reqBody["model"].(string); ok {
		model = v
	}
	normalizedModel := strings.TrimSpace(model)
	if normalizedModel != "" {
		if model != normalizedModel {
			reqBody["model"] = normalizedModel
			result.Modified = true
		}
		result.NormalizedModel = normalizedModel
	}

	if opts.IsCompact {
		if _, ok := reqBody["store"]; ok {
			delete(reqBody, "store")
			result.Modified = true
		}
		if _, ok := reqBody["stream"]; ok {
			delete(reqBody, "stream")
			result.Modified = true
		}
	} else {
		// The ChatGPT internal API requires store=false, including when the caller requests true.
		if v, ok := reqBody["store"].(bool); !ok || v {
			reqBody["store"] = false
			result.Modified = true
		}
		if v, ok := reqBody["stream"].(bool); !ok || !v {
			reqBody["stream"] = true
			result.Modified = true
		}
	}

	// Strip parameters unsupported by ChatGPT internal Codex endpoint.
	for _, key := range OpenAICodexOAuthUnsupportedFields {
		if opts.Attributes != nil {
			if key == "temperature" && opts.Attributes.Temperature != nil && *opts.Attributes.Temperature {
				continue
			}
			if key == "top_p" && opts.Attributes.TopP != nil && *opts.Attributes.TopP {
				continue
			}
		}
		if _, ok := reqBody[key]; ok {
			delete(reqBody, key)
			result.Modified = true
		}
	}

	// Request encrypted reasoning for replay; Compact has a separate request shape.
	if !opts.IsCompact && EnsureCodexReasoningInclude(reqBody) {
		result.Modified = true
	}

	// Convert legacy functions and function_call fields to tools and tool_choice.
	if functionsRaw, ok := reqBody["functions"]; ok {
		if functions, k := functionsRaw.([]any); k {
			tools := make([]any, 0, len(functions))
			for _, f := range functions {
				tools = append(tools, map[string]any{
					"type":     "function",
					"function": f,
				})
			}
			reqBody["tools"] = tools
		}
		delete(reqBody, "functions")
		result.Modified = true
	}

	if fcRaw, ok := reqBody["function_call"]; ok {
		if fcStr, ok := fcRaw.(string); ok {
			// e.g. "auto", "none"
			reqBody["tool_choice"] = fcStr
		} else if fcObj, ok := fcRaw.(map[string]any); ok {
			// e.g. {"name": "my_func"}
			if name, ok := fcObj["name"].(string); ok && strings.TrimSpace(name) != "" {
				reqBody["tool_choice"] = map[string]any{
					"type": "function",
					"name": name,
				}
			}
		}
		delete(reqBody, "function_call")
		result.Modified = true
	}

	if NormalizeCodexTools(reqBody) {
		result.Modified = true
	}
	// Collect aliases only after prompt/functions/function_call compatibility
	// has produced the final Responses protocol nodes. Otherwise references
	// introduced by those migrations can retain the reserved name.
	toolNameReverse, toolNamesChanged, err := AliasOpenAIOAuthReservedToolNames(reqBody)
	if err != nil {
		result.Error = err
		return result
	}
	result.ToolNameReverse = toolNameReverse
	if toolNamesChanged {
		result.Modified = true
	}
	if NormalizeCodexToolChoice(reqBody) {
		result.Modified = true
	}

	if v, ok := reqBody["prompt_cache_key"].(string); ok {
		result.PromptCacheKey = strings.TrimSpace(v)
		if opts.IsMessagesBridge(reqBody) {
			delete(reqBody, "prompt_cache_key")
			result.Modified = true
		}
	}

	// ChatGPT rejects the system role, so copy its text into instructions.
	// Responses JSON mode still needs developer guidance in input; the Chat bridge
	// may omit pure-text messages after promoting them losslessly.
	if ExtractSystemMessagesFromInput(reqBody, opts.OmitPromotedSystemMessagesFromInput) {
		result.Modified = true
	}

	// Fill missing instructions unless the caller disables default injection.
	if !opts.SkipDefaultInstructions && ApplyInstructions(reqBody, opts.IsCodexCLI) {
		result.Modified = true
	}
	rules := (modelcatalog.View{}).Resolve(modelcatalog.Target{Platform: "openai", ProviderType: "oauth", Model: normalizedModel}, modelcatalog.Attributes{}).Rules
	if opts.Rules != nil {
		rules = *opts.Rules
	}
	imageToolsDisabled := rules.ImageGenerationTool != nil && !*rules.ImageGenerationTool
	if imageToolsDisabled && ApplyCodexSparkImageUnsupportedInstructions(reqBody) {
		result.Modified = true
	}
	// Remove client-supplied image tools when the resolved model rules disable them.
	if imageToolsDisabled && StripCodexSparkImageGenerationTools(reqBody) {
		result.Modified = true
	}

	// Retain valid item references needed to pair tool calls with their results.
	if input, ok := reqBody["input"].([]any); ok {
		if normalizedInput, modified := NormalizeCodexToolRoleMessages(input); modified {
			input = normalizedInput
			result.Modified = true
		}
		if normalizedInput, modified := NormalizeCodexMessageContentText(input); modified {
			input = normalizedInput
			result.Modified = true
		}
		input = FilterCodexInputWithOptions(input, CodexInputFilterOptions{
			PreserveReferences: needsToolContinuation,
			PreserveCallIDs:    opts.PreserveToolCallIDs,
		})
		reqBody["input"] = input
		result.Modified = true
	} else if inputStr, ok := reqBody["input"].(string); ok {
		// ChatGPT codex endpoint requires input to be a list, not a string.
		// Convert string input to the expected message array format.
		trimmed := strings.TrimSpace(inputStr)
		if trimmed != "" {
			reqBody["input"] = []any{
				map[string]any{
					"type":    "message",
					"role":    "user",
					"content": inputStr,
				},
			}
		} else {
			reqBody["input"] = []any{}
		}
		result.Modified = true
	}

	return result
}

func NormalizeCodexToolChoice(reqBody map[string]any) bool {
	choice, ok := reqBody["tool_choice"]
	if !ok || choice == nil {
		return false
	}
	choiceMap, ok := choice.(map[string]any)
	if !ok {
		return false
	}
	choiceType := strings.TrimSpace(FirstNonEmptyString(choiceMap["type"]))
	if choiceType == "" {
		return false
	}
	modified := false
	if choiceType == "function" {
		name := strings.TrimSpace(FirstNonEmptyString(choiceMap["name"]))
		if name == "" {
			if function, ok := choiceMap["function"].(map[string]any); ok {
				name = strings.TrimSpace(FirstNonEmptyString(function["name"]))
			}
		}
		if name == "" {
			reqBody["tool_choice"] = "auto"
			return true
		}
		if strings.TrimSpace(FirstNonEmptyString(choiceMap["name"])) != name {
			choiceMap["name"] = name
			modified = true
		}
		if _, ok := choiceMap["function"]; ok {
			delete(choiceMap, "function")
			modified = true
		}
		if !CodexToolsContainFunctionName(reqBody["tools"], name) {
			reqBody["tool_choice"] = "auto"
			return true
		}
		return modified
	}
	if CodexToolsContainType(reqBody["tools"], choiceType) || CodexInputAdditionalToolsContainType(reqBody["input"], choiceType) {
		return modified
	}
	reqBody["tool_choice"] = "auto"
	return true
}

func CodexInputAdditionalToolsContainType(rawInput any, toolType string) bool {
	input, ok := rawInput.([]any)
	if !ok || strings.TrimSpace(toolType) == "" {
		return false
	}
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(FirstNonEmptyString(item["type"])) != "additional_tools" {
			continue
		}
		if CodexToolsContainType(item["tools"], toolType) {
			return true
		}
	}
	return false
}

func CodexToolsContainType(rawTools any, toolType string) bool {
	tools, ok := rawTools.([]any)
	if !ok || strings.TrimSpace(toolType) == "" {
		return false
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(FirstNonEmptyString(tool["type"])) == toolType {
			return true
		}
	}
	return false
}

func CodexToolsContainFunctionName(rawTools any, name string) bool {
	tools, ok := rawTools.([]any)
	if !ok || strings.TrimSpace(name) == "" {
		return false
	}
	normalizedName := strings.TrimSpace(name)
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(FirstNonEmptyString(tool["type"])) != "function" {
			continue
		}
		toolName := strings.TrimSpace(FirstNonEmptyString(tool["name"]))
		if toolName == "" {
			if function, ok := tool["function"].(map[string]any); ok {
				toolName = strings.TrimSpace(FirstNonEmptyString(function["name"]))
			}
		}
		if toolName == normalizedName {
			return true
		}
	}
	return false
}

func NormalizeCodexToolRoleMessages(input []any) ([]any, bool) {
	if len(input) == 0 {
		return input, false
	}

	modified := false
	normalized := make([]any, 0, len(input))
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok {
			normalized = append(normalized, item)
			continue
		}
		role, _ := m["role"].(string)
		if strings.TrimSpace(role) != "tool" {
			normalized = append(normalized, item)
			continue
		}

		callID := FirstNonEmptyString(m["call_id"], m["tool_call_id"], m["id"])
		callID = strings.TrimSpace(callID)
		if callID == "" {
			// Responses does not accept role:"tool". If no call id is available,
			// preserve the text as a user message instead of sending invalid input.
			fallback := make(map[string]any, len(m))
			maps.Copy(fallback, m)
			fallback["role"] = "user"
			delete(fallback, "tool_call_id")
			normalized = append(normalized, fallback)
			modified = true
			continue
		}

		output := ExtractTextFromContent(m["content"])
		if output == "" {
			if value, ok := m["output"].(string); ok {
				output = value
			}
		}
		if output == "" && m["content"] != nil {
			if b, err := json.Marshal(m["content"]); err == nil {
				output = string(b)
			}
		}

		normalized = append(normalized, map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  output,
		})
		modified = true
	}
	if !modified {
		return input, false
	}
	return normalized, true
}

func NormalizeCodexMessageContentText(input []any) ([]any, bool) {
	if len(input) == 0 {
		return input, false
	}

	modified := false
	normalized := make([]any, 0, len(input))
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok || strings.TrimSpace(FirstNonEmptyString(m["type"])) != "message" {
			normalized = append(normalized, item)
			continue
		}
		parts, ok := m["content"].([]any)
		if !ok {
			normalized = append(normalized, item)
			continue
		}

		var newItem map[string]any
		var newParts []any
		ensureItemCopy := func() {
			if newItem != nil {
				return
			}
			newItem = make(map[string]any, len(m))
			maps.Copy(newItem, m)
			newParts = make([]any, len(parts))
			copy(newParts, parts)
		}

		for i, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			text, hasText := part["text"]
			if !hasText {
				continue
			}
			if _, ok := text.(string); ok {
				continue
			}

			ensureItemCopy()
			newPart := make(map[string]any, len(part))
			maps.Copy(newPart, part)
			newPart["text"] = StringifyCodexContentText(text)
			newParts[i] = newPart
			modified = true
		}

		if newItem != nil {
			newItem["content"] = newParts
			normalized = append(normalized, newItem)
			continue
		}
		normalized = append(normalized, item)
	}
	if !modified {
		return input, false
	}
	return normalized, true
}

func StringifyCodexContentText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprint(v)
	}
}

func IsCodexSparkModel(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), "gpt-5.3-codex-spark")
}

func HasOpenAIImageGenerationTool(reqBody map[string]any) bool {
	if ToolsContainImageGeneration(reqBody["tools"]) {
		return true
	}
	return InputContainsImageGenerationTool(reqBody["input"])
}

func HasCodexImageGenerationFunctionTool(reqBody map[string]any) bool {
	return len(reqBody) > 0 &&
		CodexToolsContainFunctionName(reqBody["tools"], CodexImageGenerationFunctionToolName)
}

func ToolsContainImageGeneration(rawTools any) bool {
	if rawTools == nil {
		return false
	}
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}
	for _, rawTool := range tools {
		toolMap, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if IsOpenAIImageGenerationToolMap(toolMap) {
			return true
		}
	}
	return false
}

// IsOpenAIImageGenerationToolMap recognizes flat and namespace image tool declarations.
func IsOpenAIImageGenerationToolMap(tool map[string]any) bool {
	return IsOpenAIImageGenerationType(FirstNonEmptyString(tool["type"])) ||
		IsImageGenNamespaceToolMap(tool)
}

// IsImageGenNamespaceToolMap recognizes the Codex image generation namespace.
func IsImageGenNamespaceToolMap(tool map[string]any) bool {
	return strings.TrimSpace(FirstNonEmptyString(tool["type"])) == "namespace" &&
		IsOpenAIImageGenNamespaceName(FirstNonEmptyString(tool["name"]))
}

// InputContainsImageGenerationTool finds image tools in Responses Lite additional_tools items.
func InputContainsImageGenerationTool(rawInput any) bool {
	input, ok := rawInput.([]any)
	if !ok {
		return false
	}
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(FirstNonEmptyString(item["type"])) != "additional_tools" {
			continue
		}
		if ToolsContainImageGeneration(item["tools"]) {
			return true
		}
	}
	return false
}

// StripOpenAIImageGenerationTools removes image declarations from tools, Lite additional_tools
// and tool_choice. It reports whether the request changed.
func StripOpenAIImageGenerationTools(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	modified := StripOpenAIImageGenerationToolList(reqBody, "tools")
	if StripOpenAIImageGenerationToolsFromInput(reqBody) {
		modified = true
	}
	if OpenAIAnyToolChoiceSelectsImageGeneration(reqBody["tool_choice"]) {
		delete(reqBody, "tool_choice")
		modified = true
	}
	return modified
}

// StripOpenAIImageGenerationToolList removes image declarations from the named tool array.
func StripOpenAIImageGenerationToolList(container map[string]any, key string) bool {
	rawTools, ok := container[key]
	if !ok || rawTools == nil {
		return false
	}
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}
	filtered := make([]any, 0, len(tools))
	removed := false
	for _, rawTool := range tools {
		if toolMap, ok := rawTool.(map[string]any); ok && IsOpenAIImageGenerationToolMap(toolMap) {
			removed = true
			continue
		}
		filtered = append(filtered, rawTool)
	}
	if !removed {
		return false
	}
	if len(filtered) == 0 {
		delete(container, key)
	} else {
		container[key] = filtered
	}
	return true
}

// StripOpenAIImageGenerationToolsFromInput removes image tools from Responses Lite input.
func StripOpenAIImageGenerationToolsFromInput(reqBody map[string]any) bool {
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}

	filteredInput := make([]any, 0, len(input))
	modified := false
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(FirstNonEmptyString(item["type"])) != "additional_tools" {
			filteredInput = append(filteredInput, rawItem)
			continue
		}
		if !StripOpenAIImageGenerationToolList(item, "tools") {
			filteredInput = append(filteredInput, rawItem)
			continue
		}
		modified = true
		if _, hasTools := item["tools"]; hasTools {
			filteredInput = append(filteredInput, rawItem)
		}
		// Drop additional_tools items whose last declaration was removed.
	}
	if modified {
		reqBody["input"] = filteredInput
	}
	return modified
}

// StripOpenAIImageGenerationToolsFromRawPayload removes image tools from raw HTTP or WS JSON.
func StripOpenAIImageGenerationToolsFromRawPayload(payload []byte, hasDeclaration bool) ([]byte, bool, error) {
	if !hasDeclaration {
		if json.Valid(payload) {
			return payload, false, nil
		}
		var invalidPayload map[string]any
		return payload, false, json.Unmarshal(payload, &invalidPayload)
	}
	payloadMap := make(map[string]any)
	if err := json.Unmarshal(payload, &payloadMap); err != nil {
		return payload, false, err
	}
	if !StripOpenAIImageGenerationTools(payloadMap) {
		return payload, false, nil
	}
	rebuilt, err := json.Marshal(payloadMap)
	if err != nil {
		return payload, false, err
	}
	return rebuilt, true, nil
}

// StripCodexSparkImageGenerationTools removes image declarations and choices.
// Spark rejects these capabilities even when a Codex client includes them by default.
func StripCodexSparkImageGenerationTools(reqBody map[string]any) bool {
	return StripOpenAIImageGenerationTools(reqBody)
}

func HasOpenAIInputImage(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	return HasOpenAIInputImageValue(reqBody["input"]) || HasOpenAIInputImageValue(reqBody["messages"])
}

func HasOpenAIInputImageValue(value any) bool {
	switch v := value.(type) {
	case []any:
		if slices.ContainsFunc(v, HasOpenAIInputImageValue) {
			return true
		}
	case map[string]any:
		if strings.TrimSpace(FirstNonEmptyString(v["type"])) == "input_image" {
			return true
		}
		if _, ok := v["image_url"]; ok {
			return true
		}
		return HasOpenAIInputImageValue(v["content"])
	}
	return false
}

func ValidateCodexSparkInput(reqBody map[string]any, model string, spark bool) error {
	if !spark || !HasOpenAIInputImage(reqBody) {
		return nil
	}
	return fmt.Errorf("model %q does not support image input", strings.TrimSpace(model))
}

func NormalizeOpenAIResponsesImageGenerationTools(reqBody map[string]any) bool {
	rawTools, ok := reqBody["tools"]
	if !ok || rawTools == nil {
		return false
	}
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}

	modified := false
	for _, rawTool := range tools {
		toolMap, ok := rawTool.(map[string]any)
		if !ok || strings.TrimSpace(FirstNonEmptyString(toolMap["type"])) != "image_generation" {
			continue
		}
		if _, ok := toolMap["output_format"]; !ok {
			if value := strings.TrimSpace(FirstNonEmptyString(toolMap["format"])); value != "" {
				toolMap["output_format"] = value
				modified = true
			}
		}
		if _, ok := toolMap["output_compression"]; !ok {
			if value, exists := toolMap["compression"]; exists && value != nil {
				toolMap["output_compression"] = value
				modified = true
			}
		}
		if _, ok := toolMap["format"]; ok {
			delete(toolMap, "format")
			modified = true
		}
		if _, ok := toolMap["compression"]; ok {
			delete(toolMap, "compression")
			modified = true
		}
	}
	return modified
}

func NormalizeOpenAIResponseFormatSchemas(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	modified := false
	normalizeFormat := func(format map[string]any) {
		if format == nil || strings.TrimSpace(FirstNonEmptyString(format["type"])) != "json_schema" {
			return
		}
		if schema, ok := format["schema"].(map[string]any); ok && NormalizeOpenAIResponseJSONSchema(schema) {
			modified = true
		}
		if jsonSchema, ok := format["json_schema"].(map[string]any); ok {
			if schema, ok := jsonSchema["schema"].(map[string]any); ok && NormalizeOpenAIResponseJSONSchema(schema) {
				modified = true
			}
		}
	}
	if text, ok := reqBody["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok {
			normalizeFormat(format)
		}
	}
	if responseFormat, ok := reqBody["response_format"].(map[string]any); ok {
		normalizeFormat(responseFormat)
	}
	return modified
}

func NormalizeOpenAIResponseJSONSchema(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	modified := false
	for _, key := range []string{"uniqueItems", "minProperties"} {
		if _, exists := schema[key]; exists {
			delete(schema, key)
			modified = true
		}
	}
	if rawType, exists := schema["type"]; !exists || rawType == nil {
		switch {
		case schema["properties"] != nil:
			schema["type"] = "object"
			modified = true
		case schema["items"] != nil:
			schema["type"] = "array"
			modified = true
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for _, raw := range properties {
			if child, ok := raw.(map[string]any); ok && NormalizeOpenAIResponseJSONSchema(child) {
				modified = true
			}
		}
	}
	switch items := schema["items"].(type) {
	case map[string]any:
		if NormalizeOpenAIResponseJSONSchema(items) {
			modified = true
		}
	case []any:
		for _, raw := range items {
			if child, ok := raw.(map[string]any); ok && NormalizeOpenAIResponseJSONSchema(child) {
				modified = true
			}
		}
	}
	for _, key := range []string{
		"additionalProperties",
		"additionalItems",
		"contains",
		"not",
		"if",
		"then",
		"else",
		"propertyNames",
		"unevaluatedProperties",
		"unevaluatedItems",
	} {
		if child, ok := schema[key].(map[string]any); ok && NormalizeOpenAIResponseJSONSchema(child) {
			modified = true
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		children, _ := schema[key].([]any)
		for _, raw := range children {
			if child, ok := raw.(map[string]any); ok && NormalizeOpenAIResponseJSONSchema(child) {
				modified = true
			}
		}
	}
	for _, key := range []string{"$defs", "definitions", "patternProperties", "dependentSchemas"} {
		children, _ := schema[key].(map[string]any)
		for _, raw := range children {
			if child, ok := raw.(map[string]any); ok && NormalizeOpenAIResponseJSONSchema(child) {
				modified = true
			}
		}
	}
	if dependencies, ok := schema["dependencies"].(map[string]any); ok {
		for _, raw := range dependencies {
			if child, ok := raw.(map[string]any); ok && NormalizeOpenAIResponseJSONSchema(child) {
				modified = true
			}
		}
	}
	return modified
}

func EnsureOpenAIResponsesImageGenerationTool(reqBody map[string]any, spark bool) bool {
	if len(reqBody) == 0 {
		return false
	}
	if spark {
		return false
	}
	// Recognize the Codex image function, namespace and hosted tool forms before
	// injection so a request cannot receive duplicate image capabilities.
	if HasCodexImageGenerationFunctionTool(reqBody) {
		return false
	}
	if HasOpenAIImageGenerationTool(reqBody) {
		return false
	}

	tool := map[string]any{
		"type":          "image_generation",
		"output_format": "png",
	}

	rawTools, ok := reqBody["tools"]
	if !ok || rawTools == nil {
		reqBody["tools"] = []any{tool}
		return true
	}

	tools, ok := rawTools.([]any)
	if !ok {
		reqBody["tools"] = []any{tool}
		return true
	}
	reqBody["tools"] = append(tools, tool)
	return true
}

func EnsureOpenAIResponsesImageGenerationToolChoiceAuto(reqBody map[string]any, spark bool) bool {
	if len(reqBody) == 0 || HasCodexImageGenerationFunctionTool(reqBody) || !HasOpenAIImageGenerationTool(reqBody) {
		return false
	}
	if spark {
		return false
	}
	if _, ok := reqBody["tool_choice"]; ok {
		return false
	}
	reqBody["tool_choice"] = "auto"
	return true
}

func ApplyCodexImageGenerationBridgeInstructions(reqBody map[string]any, spark bool) bool {
	if len(reqBody) == 0 || HasCodexImageGenerationFunctionTool(reqBody) || !HasOpenAIImageGenerationTool(reqBody) {
		return false
	}
	if spark {
		return false
	}

	existing, _ := reqBody["instructions"].(string)
	if strings.Contains(existing, CodexImageGenerationBridgeMarker) || strings.Contains(existing, "<sub2api-codex-image-generation>") {
		return false
	}

	existing = strings.TrimRight(existing, " \t\r\n")
	if strings.TrimSpace(existing) == "" {
		reqBody["instructions"] = CodexImageGenerationBridgeText
		return true
	}

	reqBody["instructions"] = existing + "\n\n" + CodexImageGenerationBridgeText
	return true
}

func ApplyCodexSparkImageUnsupportedInstructions(reqBody map[string]any) bool {
	if len(reqBody) == 0 {
		return false
	}
	existing, _ := reqBody["instructions"].(string)
	if strings.Contains(existing, CodexSparkImageUnsupportedMarker) || strings.Contains(existing, "<sub2api-codex-spark-image-unsupported>") {
		return false
	}
	existing = strings.TrimRight(existing, " \t\r\n")
	if strings.TrimSpace(existing) == "" {
		reqBody["instructions"] = CodexSparkImageUnsupportedText
		return true
	}
	reqBody["instructions"] = existing + "\n\n" + CodexSparkImageUnsupportedText
	return true
}

func ValidateOpenAIResponsesImageModel(reqBody map[string]any, model string, imageOnly bool) error {
	if !HasOpenAIImageGenerationTool(reqBody) {
		return nil
	}
	model = strings.TrimSpace(model)
	if !imageOnly {
		return nil
	}
	return fmt.Errorf("/v1/responses image_generation requests require a Responses-capable text model; image-only model %q is not allowed", model)
}

func NormalizeOpenAIResponsesImageOnlyModel(reqBody map[string]any, imageOnly bool) bool {
	if len(reqBody) == 0 {
		return false
	}
	imageModel := strings.TrimSpace(FirstNonEmptyString(reqBody["model"]))
	if !imageOnly {
		return false
	}

	modified := false
	tools, _ := reqBody["tools"].([]any)
	imageToolIndex := -1
	for i, rawTool := range tools {
		toolMap, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(FirstNonEmptyString(toolMap["type"])) == "image_generation" {
			imageToolIndex = i
			break
		}
	}
	if imageToolIndex < 0 {
		tools = append(tools, map[string]any{
			"type":  "image_generation",
			"model": imageModel,
		})
		imageToolIndex = len(tools) - 1
		reqBody["tools"] = tools
		modified = true
	}

	if toolMap, ok := tools[imageToolIndex].(map[string]any); ok {
		if strings.TrimSpace(FirstNonEmptyString(toolMap["model"])) == "" {
			toolMap["model"] = imageModel
			modified = true
		}
		for _, key := range []string{
			"size",
			"quality",
			"background",
			"output_format",
			"output_compression",
			"moderation",
			"style",
			"partial_images",
		} {
			if value, exists := reqBody[key]; exists && value != nil {
				if _, toolHas := toolMap[key]; !toolHas {
					toolMap[key] = value
				}
				delete(reqBody, key)
				modified = true
			}
		}
	}

	if prompt := strings.TrimSpace(FirstNonEmptyString(reqBody["prompt"])); prompt != "" {
		if _, hasInput := reqBody["input"]; !hasInput {
			reqBody["input"] = prompt
		}
		delete(reqBody, "prompt")
		modified = true
	}

	if _, ok := reqBody["tool_choice"]; !ok {
		reqBody["tool_choice"] = map[string]any{"type": "image_generation"}
		modified = true
	}
	if imageModel != ImagesResponsesMainModel {
		modified = true
	}
	reqBody["model"] = ImagesResponsesMainModel
	return modified
}

// ExtractTextFromContent extracts plain text from a content value that is either
// a Go string or a []any of text-like content-part maps.
func ExtractTextFromContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, part := range v {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch t, _ := m["type"].(string); t {
			case "text", "input_text", "output_text":
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return ""
	}
}

// ExtractSystemMessagesFromInput copies system text into instructions and changes
// retained messages to developer for Responses JSON mode. With omitPromoted,
// losslessly promoted text items are removed; mixed or malformed content is retained.
func ExtractSystemMessagesFromInput(reqBody map[string]any, omitPromoted bool) bool {
	input, ok := reqBody["input"].([]any)
	if !ok || len(input) == 0 {
		return false
	}

	var systemTexts []string
	filteredInput := make([]any, 0, len(input))
	modified := false
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok || m["role"] != "system" {
			filteredInput = append(filteredInput, item)
			continue
		}

		if omitPromoted {
			if losslessText, lossless := ExtractLosslessTextFromContent(m["content"]); lossless {
				if losslessText != "" {
					systemTexts = append(systemTexts, losslessText)
				}
				modified = true
				continue
			}
		}

		if text := ExtractTextFromContent(m["content"]); text != "" {
			systemTexts = append(systemTexts, text)
		}
		m["role"] = "developer"
		filteredInput = append(filteredInput, item)
		modified = true
	}
	if omitPromoted && len(filteredInput) != len(input) {
		reqBody["input"] = filteredInput
	}

	if len(systemTexts) == 0 {
		return modified
	}

	extracted := strings.Join(systemTexts, "\n\n")
	if existing, ok := reqBody["instructions"].(string); ok && strings.TrimSpace(existing) != "" {
		reqBody["instructions"] = extracted + "\n\n" + existing
	} else {
		reqBody["instructions"] = extracted
	}
	return true
}

// ExtractLosslessTextFromContent returns text when every content part can be
// represented in instructions without dropping non-text content.
func ExtractLosslessTextFromContent(content any) (string, bool) {
	switch v := content.(type) {
	case string:
		return v, true
	case []any:
		var b strings.Builder
		for _, part := range v {
			m, ok := part.(map[string]any)
			if !ok {
				return "", false
			}
			typeName, ok := m["type"].(string)
			if !ok || (typeName != "text" && typeName != "input_text" && typeName != "output_text") {
				return "", false
			}
			text, ok := m["text"].(string)
			if !ok {
				return "", false
			}
			_, _ = b.WriteString(text)
		}
		return b.String(), true
	default:
		return "", false
	}
}

func ExtractPromptLikeInstructionsFromInput(reqBody map[string]any) string {
	input, ok := reqBody["input"].([]any)
	if !ok || len(input) == 0 {
		return ""
	}
	var texts []string
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "developer", "system":
			if text := strings.TrimSpace(ExtractTextFromContent(m["content"])); text != "" {
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n\n")
}

// DefaultCodexSynthInstructions selects embedded model-specific Codex instructions.
// It returns a minimal coding prompt when the selected embedded text is empty.
func DefaultCodexSynthInstructions(model string) string {
	if instructions := strings.TrimSpace(CodexBaseInstructionsForModel(model)); instructions != "" {
		return instructions
	}
	return "You are a helpful coding assistant."
}

// EnsureCodexReasoningInclude adds reasoning.encrypted_content when reasoning is a nonempty object.
// Encrypted content carries reasoning between store=false turns. Existing entries
// are retained, duplicate insertion is avoided, and non-array include values are unchanged.
func EnsureCodexReasoningInclude(reqBody map[string]any) bool {
	reasoning, ok := reqBody["reasoning"].(map[string]any)
	if !ok || len(reasoning) == 0 {
		return false
	}
	const encrypted = "reasoning.encrypted_content"
	switch existing := reqBody["include"].(type) {
	case nil:
		reqBody["include"] = []any{encrypted}
		return true
	case []any:
		for _, v := range existing {
			if s, ok := v.(string); ok && s == encrypted {
				return false
			}
		}
		reqBody["include"] = append(existing, encrypted)
		return true
	default:
		// Leave unexpected include types to upstream validation.
		return false
	}
}

// ApplyCodexClientMetadata fills a missing or blank x-codex-installation-id with deviceID.
// Other metadata fields are retained. Empty device IDs and unsupported metadata types
// leave the request unchanged.
func ApplyCodexClientMetadata(reqBody map[string]any, deviceID string) bool {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return false
	}
	const key = "x-codex-installation-id"
	switch existing := reqBody["client_metadata"].(type) {
	case map[string]any:
		if v, ok := existing[key].(string); ok && strings.TrimSpace(v) != "" {
			return false
		}
		existing[key] = deviceID
		reqBody["client_metadata"] = existing
		return true
	case map[string]string:
		if strings.TrimSpace(existing[key]) != "" {
			return false
		}
		next := make(map[string]any, len(existing)+1)
		for k, v := range existing {
			next[k] = v
		}
		next[key] = deviceID
		reqBody["client_metadata"] = next
		return true
	case nil:
		reqBody["client_metadata"] = map[string]any{key: deviceID}
		return true
	default:
		return false
	}
}

// ApplyInstructions fills empty instructions with the model-specific default.
func ApplyInstructions(reqBody map[string]any, isCodexCLI bool) bool {
	if !IsInstructionsEmpty(reqBody) {
		return false
	}
	model, _ := reqBody["model"].(string)
	reqBody["instructions"] = DefaultCodexSynthInstructions(model)
	return true
}

// IsInstructionsEmpty reports whether instructions is missing, nil, non-string or blank.
func IsInstructionsEmpty(reqBody map[string]any) bool {
	val, exists := reqBody["instructions"]
	if !exists {
		return true
	}
	if val == nil {
		return true
	}
	str, ok := val.(string)
	if !ok {
		return true
	}
	return strings.TrimSpace(str) == ""
}

func NormalizeCodexFilterCallID(itemType, id string, preserve bool) string {
	if preserve && len(id) <= CodexCallIDMaxLength {
		return id
	}
	return NormalizeCodexCallIDForItemType(itemType, id)
}

func CodexItemReferenceIDMappings(input []any, preserveCallIDs bool) map[string]string {
	mappings := make(map[string]string)
	ambiguous := make(map[string]struct{})
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType := strings.TrimSpace(FirstNonEmptyString(item["type"]))
		if !IsCodexToolCallItemType(itemType) {
			continue
		}
		rawCallID := strings.TrimSpace(FirstNonEmptyString(item["call_id"]))
		if rawCallID == "" {
			continue
		}
		normalized := NormalizeCodexFilterCallID(itemType, rawCallID, preserveCallIDs)
		if existing, exists := mappings[rawCallID]; exists && existing != normalized {
			delete(mappings, rawCallID)
			ambiguous[rawCallID] = struct{}{}
			continue
		}
		if _, conflict := ambiguous[rawCallID]; !conflict {
			mappings[rawCallID] = normalized
		}
	}
	return mappings
}

func CodexInputItemIDs(input []any) map[string]struct{} {
	itemIDs := make(map[string]struct{})
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(FirstNonEmptyString(item["type"])) == "item_reference" {
			continue
		}
		itemType := strings.TrimSpace(FirstNonEmptyString(item["type"]))
		id := strings.TrimSpace(FirstNonEmptyString(item["id"]))
		if id != "" && !ShouldStripOpenAIResponsesInputItemID(itemType, id) {
			itemIDs[id] = struct{}{}
		}
	}
	return itemIDs
}

func CodexInputCallIDs(input []any) map[string]struct{} {
	callIDs := make(map[string]struct{})
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || !IsCodexToolCallItemType(strings.TrimSpace(FirstNonEmptyString(item["type"]))) {
			continue
		}
		if callID := strings.TrimSpace(FirstNonEmptyString(item["call_id"])); callID != "" {
			callIDs[callID] = struct{}{}
		}
	}
	return callIDs
}

func FilterCodexInputWithOptions(input []any, opts CodexInputFilterOptions) []any {
	filtered := make([]any, 0, len(input))
	referenceIDMappings := CodexItemReferenceIDMappings(input, opts.PreserveCallIDs)
	inputItemIDs := CodexInputItemIDs(input)
	inputCallIDs := CodexInputCallIDs(input)
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok {
			filtered = append(filtered, item)
			continue
		}
		typ, _ := m["type"].(string)

		// OAuth uses store=false, so replayed reasoning IDs can cause upstream 404s.
		// Remove id and call_id while retaining encrypted_content, content and summary
		// for reasoning replay. A missing or null summary becomes an empty array to satisfy
		// upstream validation (issue #1957).
		// Compaction summaries require encrypted_content and use the generic path below:
		// PreserveReferences controls their IDs while their ciphertext is retained.
		if typ == "reasoning" {
			newItem := make(map[string]any, len(m))
			for key, value := range m {
				if key == "id" || key == "call_id" {
					// rs_* id replayed under store=false 404s; strip it.
					continue
				}
				newItem[key] = value
			}
			if summary, ok := newItem["summary"]; !ok || summary == nil {
				// Upstream requires a summary field; an empty array satisfies it.
				newItem["summary"] = []any{}
			}
			filtered = append(filtered, newItem)
			continue
		}

		// Normalize tool-call identifiers and legacy call_* references while retaining unrelated item IDs.
		fixCallIDPrefix := func(id string) string {
			return NormalizeCodexFilterCallID(typ, id, opts.PreserveCallIDs)
		}

		if typ == "item_reference" {
			if !opts.PreserveReferences {
				continue
			}
			newItem := make(map[string]any, len(m))
			maps.Copy(newItem, m)
			if id, ok := newItem["id"].(string); ok && strings.HasPrefix(strings.TrimSpace(id), "call_") {
				trimmedID := strings.TrimSpace(id)
				_, referencesExistingItem := inputItemIDs[trimmedID]
				if !referencesExistingItem {
					if normalizedID, mapped := referenceIDMappings[trimmedID]; mapped {
						newItem["id"] = normalizedID
					} else if _, hasSameTurnCall := inputCallIDs[trimmedID]; !hasSameTurnCall {
						// A bare call_* reference is a legacy function-call identifier.
						// Normalize it even when its call item lives in an earlier turn.
						newItem["id"] = NormalizeCodexCallID(trimmedID)
					}
				}
			}
			filtered = append(filtered, newItem)
			continue
		}

		newItem := m
		copied := false
		// Copy on modification so caller-owned input maps remain unchanged.
		ensureCopy := func() {
			if copied {
				return
			}
			newItem = make(map[string]any, len(m))
			maps.Copy(newItem, m)
			copied = true
		}

		if IsCodexToolCallItemType(typ) {
			callID, ok := m["call_id"].(string)
			if !ok || strings.TrimSpace(callID) == "" {
				if id, ok := m["id"].(string); ok && strings.TrimSpace(id) != "" {
					callID = id
					ensureCopy()
					newItem["call_id"] = callID
				}
			}

			if callID != "" {
				fixedCallID := fixCallIDPrefix(callID)
				if fixedCallID != callID {
					ensureCopy()
					newItem["call_id"] = fixedCallID
				}
			}
		}

		if !IsCodexToolCallItemType(typ) {
			ensureCopy()
			delete(newItem, "call_id")
		}

		if CodexInputItemRequiresName(typ) {
			if strings.TrimSpace(FirstNonEmptyString(m["name"])) == "" {
				name := FirstNonEmptyString(m["tool_name"])
				if name == "" {
					if function, ok := m["function"].(map[string]any); ok {
						name = FirstNonEmptyString(function["name"])
					}
				}
				if name == "" {
					name = "tool"
				}
				ensureCopy()
				newItem["name"] = name
			}
		}

		if typ == "reasoning" {
			// Reasoning IDs cannot be resolved under store=false; retain the reasoning content.
			ensureCopy()
			delete(newItem, "id")
			if summary, ok := newItem["summary"]; !ok || summary == nil {
				// An empty summary satisfies upstream validation when the field is missing.
				newItem["summary"] = []any{}
			}
		}

		if !opts.PreserveReferences {
			ensureCopy()
			delete(newItem, "id")
		} else if id, ok := m["id"].(string); ok && ShouldStripOpenAIResponsesInputItemID(typ, id) {
			ensureCopy()
			delete(newItem, "id")
		}

		filtered = append(filtered, newItem)
	}
	return filtered
}

func IsCodexToolCallItemType(typ string) bool { return protocolopenai.IsCodexToolCallItemType(typ) }

// IsCodexToolCallInputType recognizes call inputs whose IDs have type-specific prefixes.
// Output items are excluded; OpenAIResponsesInputItemIDPrefix handles their constraints.
func IsCodexToolCallInputType(typ string) bool {
	switch typ {
	case "function_call",
		"tool_call",
		"local_shell_call",
		"tool_search_call",
		"custom_tool_call",
		"mcp_tool_call":
		return true
	default:
		return false
	}
}

func CodexInputItemRequiresName(typ string) bool {
	switch strings.TrimSpace(typ) {
	case "function_call", "custom_tool_call", "mcp_tool_call":
		return true
	default:
		return false
	}
}

func NormalizeCodexTools(reqBody map[string]any) bool {
	rawTools, ok := reqBody["tools"]
	if !ok || rawTools == nil {
		return false
	}
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}

	modified := false
	validTools := make([]any, 0, len(tools))

	for _, tool := range tools {
		toolMap, ok := tool.(map[string]any)
		if !ok {
			// Keep unknown structure as-is to avoid breaking upstream behavior.
			validTools = append(validTools, tool)
			continue
		}

		toolType, _ := toolMap["type"].(string)
		toolType = strings.TrimSpace(toolType)
		if toolType != "function" {
			validTools = append(validTools, toolMap)
			continue
		}

		// OpenAI Responses-style tools use top-level name/parameters.
		if name, ok := toolMap["name"].(string); ok && strings.TrimSpace(name) != "" {
			validTools = append(validTools, toolMap)
			continue
		}

		// ChatCompletions-style tools use {type:"function", function:{...}}.
		functionValue, hasFunction := toolMap["function"]
		function, ok := functionValue.(map[string]any)
		if !hasFunction || functionValue == nil || !ok || function == nil {
			// Drop invalid function tools.
			modified = true
			continue
		}

		if _, ok := toolMap["name"]; !ok {
			if name, ok := function["name"].(string); ok && strings.TrimSpace(name) != "" {
				toolMap["name"] = name
				modified = true
			}
		}
		if _, ok := toolMap["description"]; !ok {
			if desc, ok := function["description"].(string); ok && strings.TrimSpace(desc) != "" {
				toolMap["description"] = desc
				modified = true
			}
		}
		if _, ok := toolMap["parameters"]; !ok {
			if params, ok := function["parameters"]; ok {
				toolMap["parameters"] = params
				modified = true
			}
		}
		if _, ok := toolMap["strict"]; !ok {
			if strict, ok := function["strict"]; ok {
				toolMap["strict"] = strict
				modified = true
			}
		}

		validTools = append(validTools, toolMap)
	}

	if modified {
		reqBody["tools"] = validTools
	}

	return modified
}

// IsOpenAIImageGenerationType recognizes the hosted image generation tool type.
func IsOpenAIImageGenerationType(value string) bool {
	return strings.TrimSpace(value) == "image_generation"
}

func OpenAIResponsesInputItemIDPrefix(itemType string) (string, bool) {
	switch strings.TrimSpace(itemType) {
	case "message":
		return "msg", true
	case "reasoning":
		return "rs", true
	case "web_search_call":
		return "ws", true
	case "custom_tool_call":
		return protocolopenai.OpenAIResponsesToolCallIDPrefix(itemType), true
	case "tool_search_call":
		return protocolopenai.OpenAIResponsesToolCallIDPrefix(itemType), true
	case "custom_tool_call_output":
		// Although custom calls use ctc IDs, OpenAI validates replayed custom
		// call output item IDs against the generic fc namespace.
		return "fc", true
	default:
		if IsCodexToolCallInputType(itemType) {
			return protocolopenai.OpenAIResponsesToolCallIDPrefix(itemType), true
		}
		return "", false
	}
}

// ShouldStripOpenAIResponsesInputItemID reports whether a replay ID violates its type prefix.
// Remove invalid IDs: inventing a replacement may reference a different upstream object.
func ShouldStripOpenAIResponsesInputItemID(itemType, id string) bool {
	prefix, constrained := OpenAIResponsesInputItemIDPrefix(itemType)
	if !constrained {
		return false
	}
	return id == "" || !strings.HasPrefix(id, prefix)
}

// IsOpenAIImageGenNamespaceName recognizes the Codex image generation namespace.
func IsOpenAIImageGenNamespaceName(value string) bool {
	return strings.TrimSpace(value) == "image_gen"
}

func OpenAIAnyToolChoiceSelectsImageGeneration(choice any) bool {
	switch v := choice.(type) {
	case string:
		return IsOpenAIImageGenerationType(v)
	case map[string]any:
		choiceType := strings.TrimSpace(FirstNonEmptyString(v["type"]))
		if IsOpenAIImageGenerationType(choiceType) {
			return true
		}
		if choiceType == "namespace" &&
			(IsOpenAIImageGenNamespaceName(FirstNonEmptyString(v["name"])) ||
				IsOpenAIImageGenNamespaceName(FirstNonEmptyString(v["namespace"]))) {
			return true
		}
		if tool, ok := v["tool"].(map[string]any); ok && OpenAIAnyToolChoiceSelectsImageGeneration(tool) {
			return true
		}
		if fn, ok := v["function"].(map[string]any); ok && IsOpenAIImageGenerationType(FirstNonEmptyString(fn["name"])) {
			return true
		}
	}
	return false
}
