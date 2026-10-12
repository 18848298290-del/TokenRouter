package bridge

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openAIResponsesToolCallItemTypes lists call types eligible to retain namespace.
// Keep it consistent with removeOpenAIResponsesRejectedNamespaceAtIndex.
// Codex FunctionCall and CustomToolCall use namespace; the additional call aliases
// support compatible clients.
var openAIResponsesToolCallItemTypes = map[string]bool{
	"function_call":    true,
	"tool_call":        true,
	"custom_tool_call": true,
	"mcp_tool_call":    true,
}

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

// StripOpenAIResponsesInputNamespaces removes namespace from direct input items.
// Tool declarations and nested fields remain intact. keepToolCallNamespaces retains it on call items.
// Rebuilding input once keeps processing linear in the history size.
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
