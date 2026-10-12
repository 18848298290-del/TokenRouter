package openai

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

func ShouldStripNonPairCallID(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "message", "reasoning", "image_generation_call":
		return true
	default:
		return false
	}
}

// IsCompactionItemType reports whether an item is a Codex remote compaction result.
// Upstreams use both "compaction" and "compaction_summary".
func IsCompactionItemType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

func JSONValueMayContainImageInput(value gjson.Result) bool {
	if !value.Exists() {
		return false
	}
	if value.IsArray() {
		found := false
		value.ForEach(func(_, item gjson.Result) bool {
			if JSONValueMayContainImageInput(item) {
				found = true
				return false
			}
			return true
		})
		return found
	}
	if value.IsObject() {
		if strings.TrimSpace(value.Get("type").String()) == "input_image" || value.Get("image_url").Exists() {
			return true
		}
		return JSONValueMayContainImageInput(value.Get("content"))
	}
	return false
}

func IsEmptyBase64DataURI(raw string) bool {
	if !strings.HasPrefix(raw, "data:") {
		return false
	}
	rest := strings.TrimPrefix(raw, "data:")
	semicolonIdx := strings.Index(rest, ";")
	if semicolonIdx < 0 {
		return false
	}
	rest = rest[semicolonIdx+1:]
	if !strings.HasPrefix(rest, "base64,") {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(rest, "base64,")) == ""
}

func NormalizeCompatSeedJSON(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var tmp any
	if err := json.Unmarshal(v, &tmp); err != nil {
		return string(v)
	}
	out, err := json.Marshal(tmp)
	if err != nil {
		return string(v)
	}
	return string(out)
}

func IsCodexToolCallItemType(typ string) bool {
	switch typ {
	case "function_call",
		"tool_call",
		"local_shell_call",
		"tool_search_call",
		"custom_tool_call",
		"mcp_tool_call",
		"function_call_output",
		"mcp_tool_call_output",
		"custom_tool_call_output",
		"tool_search_output":
		return true
	default:
		return false
	}
}
