package openai

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// compatibility_fields.go normalizes OAuth Responses input fields,
// instructions, and model values for upstream preparation.
// OAuth-compatible request preparation accepts prompt as an input source when
// input is missing or null, then removes prompt and commands from the forwarded
// shape. Existing nonnull input takes precedence even when its content is empty.
//
// The map variant mutates the supplied object and retains the assigned prompt
// value's mutable sharing. The byte variant moves raw prompt JSON through
// field patches, returning the supplied body on any patch error rather than
// publishing its earlier edits. Other helpers normalize instructions and model
// fields under their own rules. Byte-oriented paths inspect fields without
// establishing full JSON validity, so callers validate request structure and
// keep model-selection policy outside these mechanical compatibility operations.

// NormalizeOpenAIOAuthResponsesCompatibilityFields moves a nonnil prompt value
// into missing or nil input before removing prompt and commands in place.
// Existing empty strings and slices still own input. Only a missing key or nil
// interface permits assignment, so a typed nil input can retain precedence.
//
// Nil maps return false; true reports removal of either supplied compatibility
// field, including a null prompt. Assigned maps or slices remain shared with
// references to the supplied prompt value. Callers copy the request and its
// mutable values when preparation needs an independently editable attempt.
func NormalizeOpenAIOAuthResponsesCompatibilityFields(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	changed := false
	if prompt, exists := reqBody["prompt"]; exists {
		// Existing nonnil input owns the request even when it is empty.
		// Move prompt only into a missing or nil slot before dropping its
		// compatibility spelling.
		if input, hasInput := reqBody["input"]; !hasInput || input == nil {
			if prompt != nil {
				reqBody["input"] = prompt
			}
		}
		delete(reqBody, "prompt")
		changed = true
	}
	// Drop commands even when no prompt was supplied; this compatibility
	// cleanup runs independently of which field supplied the forwarded input.
	if _, exists := reqBody["commands"]; exists {
		delete(reqBody, "commands")
		changed = true
	}
	return changed
}

// NormalizeOpenAIOAuthResponsesCompatibilityBody moves nonnull raw prompt JSON
// into missing or null input, then deletes prompt and commands. Empty bodies or
// absent compatibility fields return the supplied bytes and false.
//
// A successful edit reports true, including removal of a null prompt whose
// value was not moved. Any field-patch error returns the supplied body and false
// with the error, discarding earlier intermediate bytes. Raw field insertion
// avoids numeric decoding of the moved value; callers establish complete JSON
// validity and provide any required model or conversation fields separately.
func NormalizeOpenAIOAuthResponsesCompatibilityBody(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	normalized := body
	changed := false
	prompt := gjson.GetBytes(normalized, "prompt")
	if prompt.Exists() {
		input := gjson.GetBytes(normalized, "input")
		if prompt.Type != gjson.Null && (!input.Exists() || input.Type == gjson.Null) {
			// Moving Raw retains the prompt's encoded numeric values and object
			// fields while existing nonnull input remains authoritative over
			// this alternate request spelling.
			next, err := sjson.SetRawBytes(normalized, "input", []byte(prompt.Raw))
			if err != nil {
				return body, false, fmt.Errorf("normalize oauth responses prompt: %w", err)
			}
			normalized = next
		}
		next, err := sjson.DeleteBytes(normalized, "prompt")
		if err != nil {
			return body, false, fmt.Errorf("normalize oauth responses delete prompt: %w", err)
		}
		normalized = next
		changed = true
	}
	// Commands removal also applies when prompt was absent or input was
	// already authoritative, so that cleanup does not depend on moving prompt.
	if gjson.GetBytes(normalized, "commands").Exists() {
		next, err := sjson.DeleteBytes(normalized, "commands")
		if err != nil {
			return body, false, fmt.Errorf("normalize oauth responses delete commands: %w", err)
		}
		normalized = next
		changed = true
	}
	return normalized, changed, nil
}

// OpenAIResponsesToolCallIDPrefix returns ctc for custom-call items and tsc
// for tool-search items after trimming the type label. Other types return the
// generic fc prefix, including function calls and unknown labels.
func OpenAIResponsesToolCallIDPrefix(itemType string) string {
	switch strings.TrimSpace(itemType) {
	case "custom_tool_call", "custom_tool_call_output":
		return "ctc"
	case "tool_search_call", "tool_search_output":
		return "tsc"
	default:
		// Types outside the custom and tool-search families use the generic
		// function-call prefix, giving ID-formatting callers a discriminator
		// even for an unrecognized item label.
		return "fc"
	}
}

// EnsureCodexInstructionsField establishes a string instructions field in a
// mutable request map, retaining an existing string. Nil maps do nothing;
// missing, nil, and nonstring values become an empty string for
// Codex-compatible preparation.
func EnsureCodexInstructionsField(reqBody map[string]any) {
	if reqBody == nil {
		return
	}
	if value, ok := reqBody["instructions"]; !ok || value == nil {
		reqBody["instructions"] = ""
		return
	}
	if _, ok := reqBody["instructions"].(string); !ok {
		// Codex-compatible preparation expects instructions as text. Replace
		// another Go representation with empty text so later request
		// construction receives the expected field type.
		reqBody["instructions"] = ""
	}
}

// ReplaceModelInBody writes newModel as the top-level model through a JSON
// field patch. Empty bodies, an extracted model whose String equals newModel,
// or failed writes retain the supplied bytes. Complete JSON and model-type
// validation belong to the caller.
func ReplaceModelInBody(body []byte, newModel string) []byte {
	if len(body) == 0 {
		return body
	}
	if current := gjson.GetBytes(body, "model"); current.Exists() && current.String() == newModel {
		return body
	}
	newBody, err := sjson.SetBytes(body, "model", newModel)
	if err != nil {
		// A failed model patch cannot supply the requested mapping. Return the
		// supplied bytes so the caller retains its request for validation
		// or another preparation decision.
		return body
	}
	return newBody
}
