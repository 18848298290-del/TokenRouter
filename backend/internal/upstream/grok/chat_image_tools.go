package grok

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// A final user Chat message containing an inline image can make an automatic
// view_image declaration unnecessary for that turn. Historical images do not
// establish that condition, and a named view_image selection still requires the
// tool. Required tool choice also needs at least one declaration after
// filtering.
//
// Removal therefore combines current message content with tool-choice intent and
// the declarations that remain. Other tool definitions keep their order, while
// JSON encoding can change their whitespace and escaping. When all tools disappear,
// parallel_tool_calls and an automatic choice are removed with them. The
// original message content is retained, allowing the native Chat path to use the
// supplied inline image while caller-selected tool behavior remains
// representable.

// StripRedundantGrokChatViewImageTool removes automatic view_image declarations
// for a final user turn with inline images. A named view_image selection or
// required choice with no other remaining tool keeps the original request. Other
// non-target requests return their supplied bytes.
//
// Filtering resolves function names from the nested Chat declaration or flat
// name and retains other declarations in order. A populated remainder replaces
// tools alone. An empty remainder also removes parallel_tool_calls and automatic
// tool_choice so the resulting Chat request does not carry controls for a
// removed tool set.
func StripRedundantGrokChatViewImageTool(body []byte) ([]byte, error) {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body, nil
	}
	items := messages.Array()
	if len(items) == 0 {
		return body, nil
	}
	current := items[len(items)-1]
	if strings.TrimSpace(current.Get("role").String()) != "user" ||
		!protocolopenai.JSONValueMayContainImageInput(current) {
		return body, nil
	}

	toolChoice := gjson.GetBytes(body, "tool_choice")
	// A named view_image choice expresses caller intent for this turn; retain its
	// declaration even when inline images are already present.
	if toolChoice.IsObject() && strings.TrimSpace(toolChoice.Get("type").String()) == "function" {
		choiceName := strings.TrimSpace(toolChoice.Get("function.name").String())
		if choiceName == "" {
			choiceName = strings.TrimSpace(toolChoice.Get("name").String())
		}
		if choiceName == "view_image" {
			return body, nil
		}
	}

	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, nil
	}
	filtered := make([]json.RawMessage, 0, len(tools.Array()))
	changed := false
	for _, tool := range tools.Array() {
		toolName := strings.TrimSpace(tool.Get("function.name").String())
		if toolName == "" {
			toolName = strings.TrimSpace(tool.Get("name").String())
		}
		if strings.TrimSpace(tool.Get("type").String()) == "function" && toolName == "view_image" {
			changed = true
			continue
		}
		filtered = append(filtered, json.RawMessage(tool.Raw))
	}
	if !changed {
		return body, nil
	}
	// Required tool choice needs an available declaration. Retain the original tool
	// set when filtering would leave that choice impossible.
	if len(filtered) == 0 && strings.TrimSpace(toolChoice.String()) == "required" {
		return body, nil
	}

	if len(filtered) > 0 {
		encoded, err := json.Marshal(filtered)
		if err != nil {
			return nil, err
		}
		return sjson.SetRawBytes(body, "tools", encoded)
	}

	// Removing the final declaration also invalidates parallel-tool controls and an
	// automatic choice, so clear those Chat fields together.
	out, err := sjson.DeleteBytes(body, "tools")
	if err != nil {
		return nil, err
	}
	out, err = sjson.DeleteBytes(out, "parallel_tool_calls")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(toolChoice.String()) == "auto" {
		out, err = sjson.DeleteBytes(out, "tool_choice")
	}
	return out, err
}
