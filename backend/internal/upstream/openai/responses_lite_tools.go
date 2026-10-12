package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// ResponsesLiteValidationError identifies the request field that failed Lite validation.
type ResponsesLiteValidationError struct {
	param   string
	message string
}

func (e *ResponsesLiteValidationError) Error() string { return e.message }

func newOpenAIResponsesLiteValidationError(param, format string, args ...any) error {
	return &ResponsesLiteValidationError{param: param, message: fmt.Sprintf(format, args...)}
}

// normalizeOpenAIResponsesLiteTools enables all-turn reasoning, disables parallel tool calls,
// and moves namespace declarations into input.additional_tools.
// Unsupported hosted tools return an error because dropping them would change the requested work.
func normalizeOpenAIResponsesLiteTools(reqBody map[string]any) (bool, error) {
	if reqBody == nil {
		return false, nil
	}
	if parallel, exists := reqBody["parallel_tool_calls"]; exists {
		if _, ok := parallel.(bool); !ok {
			return false, newOpenAIResponsesLiteValidationError("parallel_tool_calls", "responses Lite requires parallel_tool_calls to be a boolean")
		}
	}
	if rawReasoning, exists := reqBody["reasoning"]; exists && rawReasoning != nil {
		if _, ok := rawReasoning.(map[string]any); !ok {
			return false, newOpenAIResponsesLiteValidationError("reasoning", "responses Lite requires reasoning to be an object")
		}
	}
	rawTools, exists := reqBody["tools"]
	if !exists || rawTools == nil {
		return ensureOpenAIResponsesLiteRequestFields(reqBody)
	}
	tools, ok := rawTools.([]any)
	if !ok {
		return false, newOpenAIResponsesLiteValidationError("tools", "responses Lite requires tools to be an array")
	}

	topLevelTools := make([]any, 0, len(tools))
	namespaceTools := make([]any, 0, len(tools))
	for index, rawTool := range tools {
		if customTool, ok := rawTool.(string); ok {
			if strings.TrimSpace(customTool) == "" {
				return false, fmt.Errorf("responses Lite custom tool at index %d must not be empty", index)
			}
			topLevelTools = append(topLevelTools, rawTool)
			continue
		}
		tool, ok := rawTool.(map[string]any)
		if !ok {
			return false, fmt.Errorf("responses Lite tool at index %d must be an object", index)
		}
		toolType := strings.TrimSpace(FirstNonEmptyString(tool["type"]))
		switch toolType {
		case "function", "custom", "tool_search":
			topLevelTools = append(topLevelTools, rawTool)
		case "namespace":
			namespaceTools = append(namespaceTools, rawTool)
		case "":
			return false, fmt.Errorf("responses Lite tool at index %d is missing type", index)
		default:
			return false, fmt.Errorf("responses Lite does not support top-level tool type %q at index %d", toolType, index)
		}
	}
	if len(namespaceTools) == 0 {
		return ensureOpenAIResponsesLiteRequestFields(reqBody)
	}

	input, err := appendOpenAIResponsesLiteAdditionalTools(reqBody["input"], namespaceTools)
	if err != nil {
		return false, err
	}
	if _, err := ensureOpenAIResponsesLiteRequestFields(reqBody); err != nil {
		return false, err
	}
	reqBody["input"] = input
	if len(topLevelTools) == 0 {
		delete(reqBody, "tools")
	} else {
		reqBody["tools"] = topLevelTools
	}
	return ensureOpenAIResponsesLiteParallelToolCalls(reqBody, true)
}

// ensureOpenAIResponsesLiteParallelToolCalls disables parallel calls and combines the change flags.
func ensureOpenAIResponsesLiteParallelToolCalls(reqBody map[string]any, changed bool) (bool, error) {
	if ensureOpenAIResponsesLiteParallelToolCallsDisabled(reqBody) {
		return true, nil
	}
	return changed, nil
}

// ensureOpenAIResponsesLiteRequestFields applies Lite reasoning and parallel-call requirements.
func ensureOpenAIResponsesLiteRequestFields(reqBody map[string]any) (bool, error) {
	reasoningChanged, err := ensureOpenAIResponsesLiteReasoningContext(reqBody)
	if err != nil {
		return false, err
	}
	parallelToolCallsChanged := ensureOpenAIResponsesLiteParallelToolCallsDisabled(reqBody)
	return reasoningChanged || parallelToolCallsChanged, nil
}

// ensureOpenAIResponsesLiteReasoningContext sets all_turns while retaining other reasoning fields.
func ensureOpenAIResponsesLiteReasoningContext(reqBody map[string]any) (bool, error) {
	rawReasoning, exists := reqBody["reasoning"]
	if !exists || rawReasoning == nil {
		reqBody["reasoning"] = map[string]any{"context": "all_turns"}
		return true, nil
	}
	reasoning, ok := rawReasoning.(map[string]any)
	if !ok {
		return false, newOpenAIResponsesLiteValidationError("reasoning", "responses Lite requires reasoning to be an object")
	}
	if context, ok := reasoning["context"].(string); ok && context == "all_turns" {
		return false, nil
	}
	reasoning["context"] = "all_turns"
	return true, nil
}

// ensureOpenAIResponsesLiteParallelToolCallsDisabled sets parallel_tool_calls to false.
func ensureOpenAIResponsesLiteParallelToolCallsDisabled(reqBody map[string]any) bool {
	if parallelToolCalls, exists := reqBody["parallel_tool_calls"].(bool); exists && !parallelToolCalls {
		return false
	}
	reqBody["parallel_tool_calls"] = false
	return true
}

func appendOpenAIResponsesLiteAdditionalTools(input any, namespaceTools []any) ([]any, error) {
	var items []any
	switch typed := input.(type) {
	case nil:
		items = make([]any, 0, 1)
	case string:
		items = []any{map[string]any{
			"type":    "message",
			"role":    "user",
			"content": typed,
		}}
	case []any:
		items = typed
	default:
		return nil, fmt.Errorf("responses Lite namespace tools require input to be a string or array")
	}

	var target map[string]any
	var targetTools []any
	var allAdditionalTools []any
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(FirstNonEmptyString(item["type"])) != "additional_tools" {
			continue
		}
		rawAdditionalTools, exists := item["tools"]
		additionalTools := []any(nil)
		toolsOK := true
		if exists && rawAdditionalTools != nil {
			additionalTools, toolsOK = rawAdditionalTools.([]any)
		}
		if !toolsOK {
			return nil, fmt.Errorf("responses Lite input.additional_tools tools must be an array")
		}
		if target == nil {
			target = item
			targetTools = additionalTools
		}
		allAdditionalTools = append(allAdditionalTools, additionalTools...)
	}

	merged, err := mergeOpenAIResponsesLiteAdditionalTools(allAdditionalTools, namespaceTools)
	if err != nil {
		return nil, err
	}
	newTools := merged[len(allAdditionalTools):]
	if target != nil {
		if len(newTools) > 0 {
			target["tools"] = append(append([]any(nil), targetTools...), newTools...)
		}
		return items, nil
	}

	items = append(items, map[string]any{
		"type":  "additional_tools",
		"role":  "developer",
		"tools": newTools,
	})
	return items, nil
}

func mergeOpenAIResponsesLiteAdditionalTools(existing []any, moved []any) ([]any, error) {
	merged := append([]any(nil), existing...)
	seen := make(map[string]any, len(existing)+len(moved))
	for _, rawTool := range existing {
		if identity := openAIResponsesLiteToolIdentity(rawTool); identity != "" {
			if previous, exists := seen[identity]; exists && !reflect.DeepEqual(previous, rawTool) {
				return nil, fmt.Errorf("responses Lite additional_tools contains conflicting definitions for %s", openAIResponsesLiteToolIdentityForError(rawTool))
			}
			seen[identity] = rawTool
		}
	}
	for _, rawTool := range moved {
		identity := openAIResponsesLiteToolIdentity(rawTool)
		if identity != "" {
			if previous, exists := seen[identity]; exists {
				if reflect.DeepEqual(previous, rawTool) {
					continue
				}
				return nil, fmt.Errorf("responses Lite additional_tools conflicts with migrated %s", openAIResponsesLiteToolIdentityForError(rawTool))
			}
			seen[identity] = rawTool
		}
		merged = append(merged, rawTool)
	}
	return merged, nil
}

func openAIResponsesLiteToolIdentity(rawTool any) string {
	tool, ok := rawTool.(map[string]any)
	if !ok {
		return ""
	}
	toolType := strings.TrimSpace(FirstNonEmptyString(tool["type"]))
	name := strings.TrimSpace(FirstNonEmptyString(tool["name"]))
	if toolType == "" || name == "" {
		return ""
	}
	return toolType + "\x00" + name
}

func openAIResponsesLiteToolIdentityForError(rawTool any) string {
	tool, _ := rawTool.(map[string]any)
	return fmt.Sprintf("tool type %q name %q", strings.TrimSpace(FirstNonEmptyString(tool["type"])), strings.TrimSpace(FirstNonEmptyString(tool["name"])))
}

// NormalizeResponsesLiteToolsPayload validates and rewrites a Lite request, preserving JSON numbers.
// It returns the original bytes when validation fails or no fields change.
func NormalizeResponsesLiteToolsPayload(body []byte) ([]byte, bool, error) {
	if !json.Valid(body) {
		return body, false, fmt.Errorf("decode responses Lite request body: invalid JSON")
	}
	var requestBody map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&requestBody); err != nil {
		return body, false, fmt.Errorf("decode responses Lite request body: %w", err)
	}
	changed, err := normalizeOpenAIResponsesLiteTools(requestBody)
	if err != nil || !changed {
		return body, false, err
	}
	rebuilt, err := wirejson.Marshal(requestBody)
	if err != nil {
		return body, false, fmt.Errorf("encode responses Lite request body: %w", err)
	}
	return rebuilt, true, nil
}

// NormalizeResponsesLiteParallelToolCallsPayload disables parallel calls in an OpenAI Lite request.
func NormalizeResponsesLiteParallelToolCallsPayload(body []byte) ([]byte, bool, error) {
	if !gjson.ValidBytes(body) {
		return body, false, fmt.Errorf("decode responses Lite request body: invalid JSON")
	}
	if !gjson.ParseBytes(body).IsObject() {
		return body, false, fmt.Errorf("responses Lite request body must be a JSON object")
	}
	if parallelToolCalls := gjson.GetBytes(body, "parallel_tool_calls"); parallelToolCalls.Type == gjson.False {
		return body, false, nil
	}
	rebuilt, err := sjson.SetBytes(body, "parallel_tool_calls", false)
	if err != nil {
		return body, false, fmt.Errorf("encode responses Lite request body: %w", err)
	}
	return rebuilt, true, nil
}

// Parameter returns the original request field for the HTTP adapter's client error.
func (e *ResponsesLiteValidationError) Parameter() string { return e.param }
