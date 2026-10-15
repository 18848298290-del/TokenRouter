package bridge

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// responses_input_namespace.go removes disallowed namespace fields from direct
// Responses history items.
// Namespace fields can remain on ordinary Responses history items even when
// forwarding policy accepts them only on tool calls. Cleanup examines direct
// input objects and keeps the caller-selected call types; tool declarations
// and nested fields continue to carry their own namespace data.
//
// Raw item patching avoids decoding the complete request into generic numbers
// and maps. The array is rebuilt once after selected keys are removed, so
// unchanged objects retain their JSON spelling. A literal key precheck and
// gjson traversal establish the supported input path, while the caller owns
// full-document validation and platform or transport eligibility.

// openAIResponsesToolCallItemTypes identifies the four call-item spellings
// eligible to retain namespace during direct input cleanup when the caller
// selects that policy.
var openAIResponsesToolCallItemTypes = map[string]bool{
	"function_call":    true,
	"tool_call":        true,
	"custom_tool_call": true,
	"mcp_tool_call":    true,
}

// HasOpenAIResponsesNamespaceToolDeclaration reports a namespace declaration
// in the top-level tools array. It trims and compares the type case-
// insensitively, reads gjson paths without complete-document validation, and
// ignores nonarray tool containers.
func HasOpenAIResponsesNamespaceToolDeclaration(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	found := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		if strings.EqualFold(strings.TrimSpace(tool.Get("type").String()), "namespace") {
			found = true
			return false
		}
		return true
	})
	return found
}

// StripOpenAIResponsesInputNamespaces removes namespace keys from direct
// objects in an input array. With keepToolCallNamespaces, the configured call
// types retain their keys. Declaration tools and nested input fields remain
// untouched; callers provide valid JSON and determine forwarding policy before
// this cleanup.
//
// Each selected object is patched from its raw bytes, then the input array is
// replaced once. Literal key screening can skip escaped namespace spellings.
// Unchanged cases and either patch failure return the supplied body, with
// failures carrying an error. Buffer reconstruction retains each unmodified
// item's raw representation while array separators are rebuilt.
func StripOpenAIResponsesInputNamespaces(body []byte, keepToolCallNamespaces bool) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"namespace"`)) {
		return body, nil
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}

	var rebuilt bytes.Buffer
	rebuilt.Grow(len(input.Raw))
	// bytes.Buffer writes cannot fail. Rebuild separators once so raw objects
	// can be retained and sjson need not replace the full request per item.
	_ = rebuilt.WriteByte('[')
	changed := false
	first := true
	var stripErr error
	input.ForEach(func(_, item gjson.Result) bool {
		if !first {
			_ = rebuilt.WriteByte(',')
		}
		first = false
		itemBody := []byte(item.Raw)
		if item.IsObject() && item.Get("namespace").Exists() &&
			(!keepToolCallNamespaces || !IsOpenAIResponsesToolCallItemType(item.Get("type").String())) {
			// Remove only this object's direct key, because nested metadata and
			// retained call namespaces can still identify declarations or historical tools.
			itemBody, stripErr = sjson.DeleteBytes(itemBody, "namespace")
			if stripErr != nil {
				return false
			}
			changed = true
		}
		_, _ = rebuilt.Write(itemBody)
		return true
	})
	_ = rebuilt.WriteByte(']')
	if stripErr != nil {
		return body, fmt.Errorf("delete OpenAI input namespace: %w", stripErr)
	}
	if !changed {
		return body, nil
	}
	stripped, err := sjson.SetRawBytes(body, "input", rebuilt.Bytes())
	if err != nil {
		return body, fmt.Errorf("replace OpenAI input after namespace deletion: %w", err)
	}
	return stripped, nil
}
