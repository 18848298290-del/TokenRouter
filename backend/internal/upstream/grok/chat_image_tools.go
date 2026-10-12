package grok

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// StripRedundantGrokChatViewImageTool removes an automatic view_image tool when the
// current user turn already contains an image, so Grok can answer from that image.
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
