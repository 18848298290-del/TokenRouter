package openai

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// OAuth Responses reserves the python tool name, so caller-defined Python tools
// use an alias on the upstream request. Declarations, selected tool choices,
// and historical function calls must agree on that alias or a response could
// refer to the wrong client tool.
//
// Before mutating request objects, aliasing checks every collected name for
// collisions and chooses the legacy alias when existing history requires it.
// The resulting reverse map follows the attempt into response restoration.
// Restoration visits recognized tool-bearing protocol locations rather than
// replacing arbitrary strings, so argument text and unrelated metadata retain
// their contents. Byte-level wrappers decode numbers losslessly and return the
// supplied bytes when no supported field needs a change.

// These names distinguish the reserved OAuth Python tool from the current and
// legacy client aliases used when replaying declarations and function-call history.
const (
	CodexReservedPythonToolName = "python"
	CodexPythonToolAlias        = "python__tokenrouter"
	legacyCodexPythonToolAlias  = "python__sub2api"
)

// CodexToolNameField identifies one mutable tool-name location in a decoded
// request.
type CodexToolNameField struct {
	// object references the decoded map containing key; aliasing and restoration
	// mutate that original map after validating the collected names.
	object map[string]any
	key    string // tool-name object field key
	name   string // collected original tool-name value
}

// AliasOpenAIOAuthReservedToolNames rewrites reserved names in reqBody and
// returns an alias-to-original map. The decoded maps are mutated in place only
// after all collected names pass collision checks. Nil input and requests
// requiring no rewrite return no map and changed=false.
//
// Legacy function-call history selects the legacy alias when that name has no
// explicit declaration, allowing continuation to reference the same tool. A
// caller declaration already owning an alias remains a distinct name and can
// trigger a collision. Validating the complete set before mutation prevents an
// error from leaving declarations and historical references partially renamed.
func AliasOpenAIOAuthReservedToolNames(reqBody map[string]any) (map[string]string, bool, error) {
	if reqBody == nil {
		return nil, false, nil
	}

	fields := CollectOpenAIResponsesToolNameFields(reqBody)
	// Historical legacy calls need their existing alias when no declaration owns
	// that spelling. An explicit declaration makes the name a separate caller tool.
	alias := CodexPythonToolAlias
	legacyCall, legacyDeclaration := false, false
	for _, field := range fields {
		if field.name != legacyCodexPythonToolAlias {
			continue
		}
		if field.object["type"] == "function_call" {
			legacyCall = true
		} else {
			legacyDeclaration = true
		}
	}
	if legacyCall && !legacyDeclaration {
		alias = legacyCodexPythonToolAlias
	}
	aliasName := func(name string) string {
		if strings.EqualFold(strings.TrimSpace(name), CodexReservedPythonToolName) {
			return alias
		}
		return name
	}
	owners := make(map[string]string)
	reverse := make(map[string]string)
	for _, field := range fields {
		normalized := aliasName(field.name)
		original := field.name
		if alias == legacyCodexPythonToolAlias && field.name == legacyCodexPythonToolAlias {
			original = CodexReservedPythonToolName
		}
		if normalized != field.name {
			original = strings.TrimSpace(field.name)
		}
		if previous, exists := owners[normalized]; exists && previous != original {
			return nil, false, fmt.Errorf("tool names %q and %q both normalize to %q", previous, original, normalized)
		}
		owners[normalized] = original
		if normalized != field.name {
			reverse[normalized] = original
		}
	}
	if len(reverse) == 0 {
		return nil, false, nil
	}
	// All name owners have passed collision checks. Rewrite the collected maps
	// together so declarations and history cannot diverge after a validation failure.
	for _, field := range fields {
		if aliased := aliasName(field.name); aliased != field.name {
			field.object[field.key] = aliased
		}
	}
	return reverse, true, nil
}

// CollectOpenAIResponsesToolNameFields locates nonblank function names in
// declarations, nested namespaces, tool choices, session updates, and input
// history. Returned objects reference the original decoded maps, so callers can
// mutate those specific fields after validation.
func CollectOpenAIResponsesToolNameFields(reqBody map[string]any) []CodexToolNameField {
	fields := make([]CodexToolNameField, 0, 8)
	appendName := func(object map[string]any, key string) {
		if object == nil {
			return
		}
		name, ok := object[key].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return
		}
		fields = append(fields, CodexToolNameField{object: object, key: key, name: name})
	}
	var collectTools func(any)
	collectTools = func(rawTools any) {
		tools, ok := rawTools.([]any)
		if !ok {
			return
		}
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			toolType := strings.ToLower(strings.TrimSpace(FirstNonEmptyString(tool["type"])))
			if toolType == "function" {
				appendName(tool, "name")
				if function, ok := tool["function"].(map[string]any); ok {
					appendName(function, "name")
				}
			}
			if toolType == "namespace" {
				collectTools(tool["tools"])
			}
		}
	}
	collectTools(reqBody["tools"])
	if functions, ok := reqBody["functions"].([]any); ok {
		for _, raw := range functions {
			function, _ := raw.(map[string]any)
			appendName(function, "name")
		}
	}
	if strings.EqualFold(strings.TrimSpace(FirstNonEmptyString(reqBody["type"])), "session.update") {
		if session, ok := reqBody["session"].(map[string]any); ok {
			collectTools(session["tools"])
		}
	}
	if choice, ok := reqBody["tool_choice"].(map[string]any); ok {
		if strings.EqualFold(strings.TrimSpace(FirstNonEmptyString(choice["type"])), "function") {
			appendName(choice, "name")
			if function, ok := choice["function"].(map[string]any); ok {
				appendName(function, "name")
			}
		}
	}
	if input, ok := reqBody["input"].([]any); ok {
		for _, raw := range input {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			typ := strings.ToLower(strings.TrimSpace(FirstNonEmptyString(item["type"])))
			if typ == "additional_tools" {
				collectTools(item["tools"])
			}
			if typ == "function_call" {
				appendName(item, "name")
				if function, ok := item["function"].(map[string]any); ok {
					appendName(function, "name")
				}
			}
		}
	}
	return fields
}

// AliasOpenAIOAuthReservedToolNamesBody decodes and aliases reserved OAuth
// names when an ASCII-folded Python marker may be present. It returns the
// supplied bytes on errors or unchanged requests, and number-preserving
// decoding protects large numeric fields during a rewrite.
func AliasOpenAIOAuthReservedToolNamesBody(body []byte) ([]byte, map[string]string, bool, error) {
	if len(body) == 0 || !ContainsASCIIFold(body, []byte(CodexReservedPythonToolName)) {
		return body, nil, false, nil
	}
	var reqBody map[string]any
	if err := wirejson.DecodeUseNumber(body, &reqBody); err != nil {
		return body, nil, false, fmt.Errorf("decode OAuth reserved tool names: %w", err)
	}
	reverse, changed, err := AliasOpenAIOAuthReservedToolNames(reqBody)
	if err != nil || !changed {
		return body, reverse, false, err
	}
	normalized, err := json.Marshal(reqBody)
	if err != nil {
		return body, nil, false, fmt.Errorf("encode OAuth reserved tool names: %w", err)
	}
	return normalized, reverse, true, nil
}

// ContainsASCIIFold reports whether needle occurs in haystack with ASCII letter
// case ignored. An empty needle returns false, and bytes outside the ASCII
// uppercase range are compared directly.
func ContainsASCIIFold(haystack, needle []byte) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		matched := true
		for j := range needle {
			a, b := haystack[i+j], needle[j]
			// Fold ASCII letters for the reserved-name precheck; other bytes retain
			// exact comparisons so the search does not interpret UTF-8 request text.
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// MergeCodexToolNameReverseMaps copies base and overlay into a new reverse-name
// map. Overlay values take precedence for duplicate aliases, and callers can
// mutate the returned map independently of both inputs.
func MergeCodexToolNameReverseMaps(base, overlay map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(overlay))
	maps.Copy(merged, base)
	maps.Copy(merged, overlay)
	return merged
}

// RestoreCodexToolNamesInJSON restores supported response tool fields using
// reverse. Invalid JSON, absent mappings, unchanged objects, and failed
// encoding return the supplied bytes, while decoded numbers retain their
// numeric precision during restoration.
func RestoreCodexToolNamesInJSON(data []byte, reverse map[string]string) []byte {
	if len(data) == 0 || len(reverse) == 0 || !json.Valid(data) {
		return data
	}
	// Restore through parsed tool locations with UseNumber decoding so alias
	// repair does not replace unrelated string content or round large numeric metadata.
	var decoded any
	// Response repair is best effort; unreadable data remains available to the
	// downstream response path instead of being lost because optional restoration failed.
	if err := wirejson.DecodeUseNumber(data, &decoded); err != nil {
		return data
	}
	if !RestoreCodexToolNameFields(decoded, reverse) {
		return data
	}
	restored, err := json.Marshal(decoded)
	// Keep upstream bytes if the repaired object cannot be encoded, so optional
	// tool-name restoration cannot discard a response that remains usable downstream.
	if err != nil {
		return data
	}
	return restored
}

// RestoreCodexToolNameFields restores matching aliases in supported response
// objects and reports whether any field changed. The object maps are modified
// in place, and reverse keys match the emitted name exactly. Nonobject roots
// return false.
//
// Responses lifecycle items, completed output, Chat function calls, Messages
// tool-use blocks, and session tool declarations each expose names in different
// locations. The traversal follows those locations and nested namespace
// declarations while leaving tool argument payloads untouched. This keeps the
// original name available to the downstream client without interpreting
// arbitrary strings as tool identity.
func RestoreCodexToolNameFields(value any, reverse map[string]string) bool {
	root, ok := value.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	restoreItem := func(raw any) {
		item, ok := raw.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(FirstNonEmptyString(item["type"])), "function_call") {
			return
		}
		name, _ := item["name"].(string)
		if original, exists := reverse[name]; exists {
			item["name"] = original
			changed = true
		}
	}
	restoreOutput := func(raw any) {
		output, _ := raw.([]any)
		for _, item := range output {
			restoreItem(item)
		}
	}
	restoreResponse := func(raw any) {
		response, ok := raw.(map[string]any)
		if ok {
			restoreOutput(response["output"])
		}
	}
	restoreFunction := func(raw any) {
		function, ok := raw.(map[string]any)
		if !ok {
			return
		}
		name, _ := function["name"].(string)
		if original, exists := reverse[name]; exists {
			function["name"] = original
			changed = true
		}
	}
	restoreChatToolCalls := func(raw any) {
		toolCalls, _ := raw.([]any)
		for _, rawCall := range toolCalls {
			call, ok := rawCall.(map[string]any)
			if !ok {
				continue
			}
			callType := strings.ToLower(strings.TrimSpace(FirstNonEmptyString(call["type"])))
			if callType == "" || callType == "function" {
				restoreFunction(call["function"])
			}
		}
	}
	restoreMessageContent := func(raw any) {
		content, _ := raw.([]any)
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok || !strings.EqualFold(strings.TrimSpace(FirstNonEmptyString(block["type"])), "tool_use") {
				continue
			}
			name, _ := block["name"].(string)
			if original, exists := reverse[name]; exists {
				block["name"] = original
				changed = true
			}
		}
	}
	var restoreTools func(any)
	restoreTools = func(raw any) {
		tools, _ := raw.([]any)
		for _, rawTool := range tools {
			tool, ok := rawTool.(map[string]any)
			if !ok {
				continue
			}
			toolType := strings.ToLower(strings.TrimSpace(FirstNonEmptyString(tool["type"])))
			if toolType == "function" {
				name, _ := tool["name"].(string)
				if original, exists := reverse[name]; exists {
					tool["name"] = original
					changed = true
				}
			}
			if toolType == "namespace" {
				restoreTools(tool["tools"])
			}
		}
	}

	eventType := strings.ToLower(strings.TrimSpace(FirstNonEmptyString(root["type"])))
	switch eventType {
	case "response.output_item.added", "response.output_item.done":
		restoreItem(root["item"])
	case "response.created", "response.in_progress", "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		restoreResponse(root["response"])
	case "session.created", "session.updated":
		if session, ok := root["session"].(map[string]any); ok {
			restoreTools(session["tools"])
		}
	}
	if _, hasOutput := root["output"]; hasOutput {
		restoreOutput(root["output"])
	}
	if choices, ok := root["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, ok := rawChoice.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"message", "delta"} {
				if message, ok := choice[key].(map[string]any); ok {
					restoreChatToolCalls(message["tool_calls"])
				}
			}
		}
	}
	restoreMessageContent(root["content"])
	if block, ok := root["content_block"].(map[string]any); ok && strings.EqualFold(strings.TrimSpace(FirstNonEmptyString(block["type"])), "tool_use") {
		name, _ := block["name"].(string)
		if original, exists := reverse[name]; exists {
			block["name"] = original
			changed = true
		}
	}
	return changed
}
