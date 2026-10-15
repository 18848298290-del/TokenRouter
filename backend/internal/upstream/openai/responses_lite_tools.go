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

// responses_lite_tools.go normalizes Responses Lite tool placement and
// validates declaration identity before moving namespace tools.
// Responses Lite keeps ordinary function, custom, and tool-search declarations
// at the top level while moving namespace tools into input.additional_tools.
// Validation rejects unsupported hosted tools or conflicting definitions before
// the request loses those declarations, so conversion cannot silently change
// the requested tool set.
//
// Existing additional_tools items are considered together for identity checks,
// but new tools append to the first item or a new developer item. Equal
// type/name definitions are reused and unequal definitions are rejected.
// Reasoning context becomes all_turns and parallel calls are disabled after
// validation. Raw wrappers preserve JSON number values and return the supplied
// bytes on errors, keeping failed normalization from publishing a partial
// rewritten request.

// ResponsesLiteValidationError identifies a typed Lite validation failure for
// an HTTP field.
type ResponsesLiteValidationError struct {
	param   string // client request field identifier
	message string // formatted client validation explanation
}

// Error returns the Lite validation message prepared for the client.
func (e *ResponsesLiteValidationError) Error() string { return e.message }

// newOpenAIResponsesLiteValidationError creates a field-specific error with the
// formatted client message.
func newOpenAIResponsesLiteValidationError(param, format string, args ...any) error {
	return &ResponsesLiteValidationError{param: param, message: fmt.Sprintf(format, args...)}
}

// normalizeOpenAIResponsesLiteTools validates and rewrites a decoded Lite
// request in place. Nil requests return unchanged; callers own mutable maps and
// slices and serialize access. Existing parallel_tool_calls must be boolean,
// reasoning must be an object or null, and tools must have a supported shape.
//
// Namespace declarations are validated against every existing additional_tools
// item before top-level removal. Equal definitions are reused and conflicting
// definitions return an error. After validated movement, the input retains its
// messages and receives new namespaces in the first additional_tools item or a
// new developer item. Other supported tools stay top-level, reasoning becomes
// all_turns, and parallel calls become false.
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

	// Check merged identities before removing top-level namespaces, so a conflict
	// cannot leave the caller request with declarations only partially migrated into its input.
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

// ensureOpenAIResponsesLiteParallelToolCalls disables parallel calls and
// combines that assignment with changed. reqBody must be writable; an
// already-disabled value retains the supplied change flag, and this helper
// returns no validation error.
func ensureOpenAIResponsesLiteParallelToolCalls(reqBody map[string]any, changed bool) (bool, error) {
	if ensureOpenAIResponsesLiteParallelToolCallsDisabled(reqBody) {
		return true, nil
	}
	return changed, nil
}

// ensureOpenAIResponsesLiteRequestFields applies all-turn reasoning and
// disables parallel calls in a writable request. Reasoning validation happens
// first; its error returns before parallel-tool assignment, and the result
// combines both changes.
func ensureOpenAIResponsesLiteRequestFields(reqBody map[string]any) (bool, error) {
	reasoningChanged, err := ensureOpenAIResponsesLiteReasoningContext(reqBody)
	if err != nil {
		return false, err
	}
	parallelToolCallsChanged := ensureOpenAIResponsesLiteParallelToolCallsDisabled(reqBody)
	return reasoningChanged || parallelToolCallsChanged, nil
}

// ensureOpenAIResponsesLiteReasoningContext sets reasoning.context to all_turns
// in a writable request. Missing or null reasoning creates an object, another
// shape returns a typed error, and an already-correct context reports no
// change.
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

// ensureOpenAIResponsesLiteParallelToolCallsDisabled writes false unless a
// boolean false already exists. The request map must be writable; callers
// needing strict type validation perform it before this helper overwrites other
// values.
func ensureOpenAIResponsesLiteParallelToolCallsDisabled(reqBody map[string]any) bool {
	if parallelToolCalls, exists := reqBody["parallel_tool_calls"].(bool); exists && !parallelToolCalls {
		return false
	}
	reqBody["parallel_tool_calls"] = false
	return true
}

// appendOpenAIResponsesLiteAdditionalTools places namespace declarations into
// input without duplicating matching definitions. Nil input starts an empty
// sequence, strings become user messages, and arrays retain their supplied
// elements. Other input shapes and nonarray additional-tools fields return
// errors.
//
// All existing additional_tools declarations participate in merge validation
// before an existing item is changed. Newly accepted tools append to a copied
// tools slice on the first item, or a new developer item when none exists.
// Existing maps and tool objects remain shared with the input, so callers
// isolate them before using this helper for independently mutable request
// history.
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
	// Existing additional-tools items retain their locations. Append only newly
	// accepted namespaces to the first item so declarations already elsewhere are not duplicated.
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

// mergeOpenAIResponsesLiteAdditionalTools returns a new slice containing
// existing tools and accepted moved declarations. Tool objects remain shared;
// callers treat them according to their request ownership. A recognized
// type/name pair identifies one declaration for equality checks.
//
// Conflicting existing identities or a moved definition differing from an
// existing one returns an error before caller maps change. Equal moved
// definitions are skipped, while tools without a usable identity are appended.
// Existing entries retain their order and duplicates already equal in that
// existing slice remain present; deduplication applies to the moved additions.
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

// openAIResponsesLiteToolIdentity returns a trimmed type/name pair separated by
// a null byte. Nonobject declarations or missing values return empty text,
// causing the merge helper to retain them without identity deduplication.
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

// openAIResponsesLiteToolIdentityForError formats the trimmed type and name for
// merge diagnostics.
func openAIResponsesLiteToolIdentityForError(rawTool any) string {
	tool, _ := rawTool.(map[string]any)
	return fmt.Sprintf("tool type %q name %q", strings.TrimSpace(FirstNonEmptyString(tool["type"])), strings.TrimSpace(FirstNonEmptyString(tool["name"])))
}

// NormalizeResponsesLiteToolsPayload validates raw JSON and applies full Lite
// normalization with number-preserving decoding. Errors and unchanged requests
// return the supplied bytes; successful rewriting returns a new encoded body
// and true.
func NormalizeResponsesLiteToolsPayload(body []byte) ([]byte, bool, error) {
	if !json.Valid(body) {
		return body, false, fmt.Errorf("decode responses Lite request body: invalid JSON")
	}
	var requestBody map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	// Keep numbers as JSON numbers while metadata and tools are rewritten,
	// preventing large request identifiers from being rounded through float64 decoding.
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

// NormalizeResponsesLiteParallelToolCallsPayload sets parallel_tool_calls=false
// on a valid JSON object. An existing boolean false returns original bytes;
// other values are replaced directly, with shape or splice failures returning
// the supplied bytes and an error.
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

// Parameter returns the request field attached to this Lite validation error.
func (e *ResponsesLiteValidationError) Parameter() string { return e.param }
