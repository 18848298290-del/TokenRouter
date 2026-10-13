package provider

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Codex OAuth transformation uses gateway image-intent and Messages-bridge
// hints while the upstream transformer owns request encoding. Exact Spark
// identity disables image paths, and hosted image generation is distinguished
// from client image functions and namespaces. Guard markers and Anthropic
// cache-key prefixes identify bridge requests for compatibility handling.
//
// Claude Code task guidance is inserted as one developer message after
// existing leading developer input. Typed and decoded input paths share marker
// detection and insertion ordering but have different representation behavior:
// typed serialization follows ResponsesInputItem fields, while decoded maps
// retain their nested values. Callers keep request objects stable during
// mutation and inspect transform results before sending a body that could be
// partially changed on error.

const (
	// These markers identify injected Claude Code task guidance and its legacy
	// spelling so bridge recognition and duplicate checks agree across request representations.
	legacyClaudeCodeTodoGuardMarker = "<sub2api-claude-code-todo-guard>"

	// OpenAICompatClaudeCodeTodoGuardMarker identifies the current injected task guidance.
	OpenAICompatClaudeCodeTodoGuardMarker = "<tokenrouter-claude-code-todo-guard>"
	// OpenAICompatClaudeCodeTodoGuardText supplies task-state guidance as developer input.
	OpenAICompatClaudeCodeTodoGuardText = OpenAICompatClaudeCodeTodoGuardMarker + "\nWhen using Claude Code todo or task tracking tools, keep the visible task list consistent. Do not send final or summary text while any item remains in_progress. Before finishing, asking the user to choose, or reporting a blocker, update the todo list so completed work is completed and deferred work is pending/open; leave an item in_progress only when active work will continue in the same turn.\n</tokenrouter-claude-code-todo-guard>"
)

// ApplyCodexOAuthTransform mutates a decoded request through the upstream
// Codex transformer using CLI and Compact flags. The returned result carries
// modification, model, cache-key, and possible error observations; callers
// inspect it before forwarding.
func ApplyCodexOAuthTransform(reqBody map[string]any, isCodexCLI bool, isCompact bool) openai.CodexTransformResult {
	return ApplyCodexOAuthTransformWithOptions(reqBody, openai.CodexOAuthTransformOptions{IsCodexCLI: isCodexCLI, IsCompact: isCompact})
}

// ApplyCodexOAuthTransformWithOptions installs the gateway Messages-bridge
// detector and delegates request transformation with the remaining supplied
// options. reqBody is mutable and may retain partial transformation before the
// returned result reports an error.
func ApplyCodexOAuthTransformWithOptions(reqBody map[string]any, opts openai.CodexOAuthTransformOptions) openai.CodexTransformResult {
	opts.IsMessagesBridge = IsOpenAICompatMessagesBridgeRequestBody
	return openai.ApplyCodexOAuthTransformWithOptions(reqBody, opts)
}

// IsCodexSparkModel reports whether the model has the normalized exact Spark
// identity.
func IsCodexSparkModel(model string) bool {
	return openai.IsCodexSparkModel(model)
}

// StripOpenAIImageGenerationToolsFromRawPayload detects image declarations
// before delegating raw-body stripping. The upstream helper returns
// replacement bytes, its changed flag, and errors; callers keep the input
// bytes stable while detection and editing run.
func StripOpenAIImageGenerationToolsFromRawPayload(payload []byte) ([]byte, bool, error) {
	return openai.StripOpenAIImageGenerationToolsFromRawPayload(payload, ImageIntent().OpenAIRequestBodyHasImageGenerationDeclaration(payload))
}

// ValidateCodexSparkInput rejects recognized image input when model is exact
// Spark after normalization. Other models and text-only input pass, with the
// upstream validator defining accepted Chat and Responses image shapes.
func ValidateCodexSparkInput(reqBody map[string]any, model string) error {
	return openai.ValidateCodexSparkInput(reqBody, model, IsCodexSparkModel(model))
}

// EnsureOpenAIResponsesImageGenerationTool adds a hosted image tool to mutable
// non-Spark requests when image capability is absent. Existing client image
// functions, namespace declarations, or hosted tools prevent duplication; the
// returned boolean reports modification.
func EnsureOpenAIResponsesImageGenerationTool(reqBody map[string]any) bool {
	return openai.EnsureOpenAIResponsesImageGenerationTool(reqBody, IsCodexSparkModel(openai.FirstNonEmptyString(reqBody["model"])))
}

// EnsureOpenAIResponsesImageGenerationToolChoiceAuto adds auto selection for a
// hosted image tool when tool_choice is absent. Spark, client image functions,
// and existing choices retain their configuration; the request map is mutated
// on success.
func EnsureOpenAIResponsesImageGenerationToolChoiceAuto(reqBody map[string]any) bool {
	return openai.EnsureOpenAIResponsesImageGenerationToolChoiceAuto(reqBody, IsCodexSparkModel(openai.FirstNonEmptyString(reqBody["model"])))
}

// ApplyCodexImageGenerationBridgeInstructions adds hosted-image guidance to
// eligible non-Spark requests and reports modification. Existing bridge
// markers, client image functions, or absent image tools retain current
// instructions.
func ApplyCodexImageGenerationBridgeInstructions(reqBody map[string]any) bool {
	return openai.ApplyCodexImageGenerationBridgeInstructions(reqBody, IsCodexSparkModel(openai.FirstNonEmptyString(reqBody["model"])))
}

// ValidateOpenAIResponsesImageModel rejects image-only top-level model names
// when the body declares hosted image generation. The error tells callers to
// use a Responses-capable text driver; other shapes pass unchanged.
func ValidateOpenAIResponsesImageModel(reqBody map[string]any, model string) error {
	return openai.ValidateOpenAIResponsesImageModel(reqBody, model, media.IsImageGenerationModel(strings.TrimSpace(model)))
}

// NormalizeOpenAIResponsesImageOnlyModel converts recognized image-only
// requests to the upstream text-driver/image-tool shape. It mutates model,
// input, tool options, and choice according to the upstream helper and returns
// its modification flag.
func NormalizeOpenAIResponsesImageOnlyModel(reqBody map[string]any) bool {
	return openai.NormalizeOpenAIResponsesImageOnlyModel(reqBody, media.IsImageGenerationModel(openai.FirstNonEmptyString(reqBody["model"])))
}

// ApplyCodexClientMetadata fills missing installation metadata from the
// provider device ID. Nil targets return false; the upstream helper retains
// existing nonblank IDs and declines unsupported metadata shapes.
func ApplyCodexClientMetadata(reqBody map[string]any, provider *ExecutionProvider) bool {
	if provider == nil {
		return false
	}
	return openai.ApplyCodexClientMetadata(reqBody, provider.View().GetOpenAIDeviceID())
}

// IsOpenAICompatMessagesBridgeBody recognizes either current/legacy guard
// marker bytes or an Anthropic compatibility cache-key prefix. Empty bodies
// return false, and marker scanning does not establish JSON validity or
// authenticated client identity.
func IsOpenAICompatMessagesBridgeBody(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if bytes.Contains(body, []byte(OpenAICompatClaudeCodeTodoGuardMarker)) || bytes.Contains(body, []byte(legacyClaudeCodeTodoGuardMarker)) {
		return true
	}
	return IsOpenAICompatMessagesBridgePromptCacheKey(gjson.GetBytes(body, "prompt_cache_key").String())
}

// IsOpenAICompatMessagesBridgeRequestBody recognizes guard text in decoded
// input or an Anthropic compatibility cache-key prefix. Nil maps return false;
// input recognition reads []any items and is a transformation hint rather than
// authorization.
func IsOpenAICompatMessagesBridgeRequestBody(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	if input, ok := reqBody["input"].([]any); ok && (InputContainsText(input, OpenAICompatClaudeCodeTodoGuardMarker) || InputContainsText(input, legacyClaudeCodeTodoGuardMarker)) {
		return true
	}
	return IsOpenAICompatMessagesBridgePromptCacheKey(openai.FirstNonEmptyString(reqBody["prompt_cache_key"]))
}

// IsOpenAICompatMessagesBridgePromptCacheKey checks trimmed cache keys for
// metadata, cache, or digest bridge prefixes. Prefix matching is case
// sensitive and supplies a compatibility hint for Codex transformation.
func IsOpenAICompatMessagesBridgePromptCacheKey(key string) bool {
	key = strings.TrimSpace(key)
	return strings.HasPrefix(key, "anthropic-metadata-") ||
		strings.HasPrefix(key, "anthropic-cache-") ||
		strings.HasPrefix(key, "anthropic-digest-")
}

// AppendOpenAICompatClaudeCodeTodoGuard inserts guard guidance after leading
// developer messages in typed Responses input. Nil requests, missing or
// malformed input, existing current/legacy markers, and encoding errors return
// false.
//
// Insertion keeps the order of all existing items and publishes req.Input only
// after serialization succeeds. The guard uses a developer message so its task
// guidance sits beside the initial developer instructions before other
// history. Parsing through typed ResponsesInputItem follows that type's
// encoding rules; callers should not infer byte-for-byte retention of
// arbitrary unknown input fields.
func AppendOpenAICompatClaudeCodeTodoGuard(req *protocolopenai.ResponsesRequest) bool {
	if req == nil || len(req.Input) == 0 {
		return false
	}

	// A failed typed input decode leaves the existing request intact. Skip guard
	// injection when the input cannot be rebuilt through the typed Responses representation.
	var items []protocolopenai.ResponsesInputItem
	if err := json.Unmarshal(req.Input, &items); err != nil {
		return false
	}
	if len(items) == 0 || (ResponsesInputItemsContainText(items, OpenAICompatClaudeCodeTodoGuardMarker) || ResponsesInputItemsContainText(items, legacyClaudeCodeTodoGuardMarker)) {
		return false
	}

	// Keep guard encoding temporary before the input list changes. An encoding
	// failure leaves req.Input untouched and reports no adopted guidance injection.
	content, err := json.Marshal([]protocolopenai.ResponsesContentPart{{
		Type: "input_text",
		Text: OpenAICompatClaudeCodeTodoGuardText,
	}})
	if err != nil {
		return false
	}

	guard := protocolopenai.ResponsesInputItem{
		Type:    "message",
		Role:    "developer",
		Content: content,
	}

	// Keep leading developer instructions ahead of the guard and retain later history
	// in its existing order so task guidance joins the initial instruction segment.
	insertAt := 0
	for insertAt < len(items) && items[insertAt].Type == "message" && items[insertAt].Role == "developer" {
		insertAt++
	}

	items = append(items, protocolopenai.ResponsesInputItem{})
	copy(items[insertAt+1:], items[insertAt:])
	items[insertAt] = guard

	// Serialization publishes the ordered typed input only when it succeeds;
	// a failed rebuild leaves the caller's original request input available for forwarding decisions.
	input, err := json.Marshal(items)
	if err != nil {
		return false
	}
	req.Input = input
	return true
}

// AppendOpenAICompatClaudeCodeTodoGuardToRequestBody inserts guard guidance
// after leading developer items in mutable decoded input. Nil maps, non-array
// or empty input, and either existing marker return false.
//
// The existing item values retain their order while the slice gains one
// developer message. Type and role checks trim string values; insertion stops
// at the first other shape. The resulting slice is written to reqBody; append
// can reuse its backing array, and retained nested items remain shared.
// Callers serialize access to input storage and nested values during mutation
// and subsequent transformation reads.
func AppendOpenAICompatClaudeCodeTodoGuardToRequestBody(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}

	input, ok := reqBody["input"].([]any)
	if !ok || len(input) == 0 || (InputContainsText(input, OpenAICompatClaudeCodeTodoGuardMarker) || InputContainsText(input, legacyClaudeCodeTodoGuardMarker)) {
		return false
	}

	guard := map[string]any{
		"type": "message",
		"role": "developer",
		"content": []any{
			map[string]any{
				"type": "input_text",
				"text": OpenAICompatClaudeCodeTodoGuardText,
			},
		},
	}

	// Keep leading developer instructions ahead of the guard and retain later history
	// in its existing order so task guidance joins the initial instruction segment.
	insertAt := 0
	for insertAt < len(input) {
		item, ok := input[insertAt].(map[string]any)
		if !ok || strings.TrimSpace(openai.FirstNonEmptyString(item["type"])) != "message" || strings.TrimSpace(openai.FirstNonEmptyString(item["role"])) != "developer" {
			break
		}
		insertAt++
	}

	input = append(input, nil)
	copy(input[insertAt+1:], input[insertAt:])
	input[insertAt] = guard
	reqBody["input"] = input
	return true
}

// ResponsesInputItemsContainText finds trimmed nonblank needle text in each
// typed item's raw content bytes. Empty needles return false; substring
// matching reads serialized content rather than decoding individual text
// parts.
func ResponsesInputItemsContainText(items []protocolopenai.ResponsesInputItem, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, item := range items {
		if strings.Contains(string(item.Content), needle) {
			return true
		}
	}
	return false
}

// InputContainsText finds a nonblank trimmed substring in JSON-encoded decoded
// input items. Unencodable items are skipped, so false does not establish
// absence inside values that cannot be serialized.
func InputContainsText(input []any, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, item := range input {
		// Unencodable items supply no searchable JSON text. Keep scanning subsequent
		// items so a malformed value cannot hide a guard present elsewhere in input.
		b, err := json.Marshal(item)
		if err == nil && strings.Contains(string(b), needle) {
			return true
		}
	}
	return false
}
