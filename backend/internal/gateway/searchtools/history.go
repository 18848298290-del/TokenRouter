package searchtools

import (
	"bytes"
	"encoding/json"
	"strings"
	"unsafe"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

const (
	blockTypeServerToolUse       = "server_tool_use"
	blockTypeWebSearchToolResult = "web_search_tool_result"
)

// Quoted block type names allow a quick substring check
// regardless of whitespace around JSON keys and values.
var (
	patternServerToolUse       = []byte(`"server_tool_use"`)
	patternWebSearchToolResult = []byte(`"web_search_tool_result"`)
)

// FilterWebSearchHistoryBlocks removes search blocks rejected by the target upstream.
// Gateway-emulated server_tool_use and web_search_tool_result blocks have ToolUseIDPrefix IDs and are always removed.
// Callers set stripAll for thinking-passback providers such as DeepSeek, Kimi, and GLM,
// which accept text, thinking, image, tool_use, and tool_result blocks. Otherwise upstream-issued search blocks remain.
// Text summaries retain search context. Emptied messages receive placeholder text as in FilterThinkingBlocksForRetry.
// It returns body unchanged when no blocks need removal.
func FilterWebSearchHistoryBlocks(body []byte, stripAll bool) []byte {
	if !bytes.Contains(body, patternServerToolUse) && !bytes.Contains(body, patternWebSearchToolResult) {
		return body
	}

	jsonStr := *(*string)(unsafe.Pointer(&body))
	msgsRes := gjson.Get(jsonStr, "messages")
	if !msgsRes.Exists() || !msgsRes.IsArray() {
		return body
	}

	var messages []any
	if err := json.Unmarshal(wire.SliceRawFromBody(body, msgsRes), &messages); err != nil {
		return body
	}

	modified := false
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msgMap["content"].([]any)
		if !ok {
			continue
		}

		// Allocate a replacement slice only after finding a block to remove.
		var newContent []any
		for i, block := range content {
			blockMap, isMap := block.(map[string]any)
			if isMap && shouldStripWebSearchBlock(blockMap, stripAll) {
				if newContent == nil {
					newContent = make([]any, 0, len(content))
					newContent = append(newContent, content[:i]...)
				}
				continue
			}
			if newContent != nil {
				newContent = append(newContent, block)
			}
		}
		if newContent == nil {
			continue
		}
		modified = true
		if len(newContent) == 0 {
			role, _ := msgMap["role"].(string)
			placeholder := "(content removed)"
			if role == "assistant" {
				placeholder = "(assistant content removed)"
			}
			newContent = []any{map[string]any{"type": "text", "text": placeholder}}
		}
		msgMap["content"] = newContent
	}

	if !modified {
		return body
	}

	msgsBytes, err := json.Marshal(messages)
	if err != nil {
		return body
	}
	out, err := sjson.SetRawBytes(body, "messages", msgsBytes)
	if err != nil {
		return body
	}
	return out
}

func shouldStripWebSearchBlock(block map[string]any, stripAll bool) bool {
	blockType, _ := block["type"].(string)
	switch blockType {
	case blockTypeServerToolUse:
		if stripAll {
			return true
		}
		id, _ := block["id"].(string)
		return strings.HasPrefix(id, ToolUseIDPrefix)
	case blockTypeWebSearchToolResult:
		if stripAll {
			return true
		}
		id, _ := block["tool_use_id"].(string)
		return strings.HasPrefix(id, ToolUseIDPrefix)
	default:
		return false
	}
}
