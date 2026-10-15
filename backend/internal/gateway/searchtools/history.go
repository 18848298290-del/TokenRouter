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

// history.go removes emulated or selected upstream search blocks from replayed
// message history. ToolUseIDPrefix distinguishes those synthetic call/results
// from upstream-issued search identities, while retained summary text keeps
// their readable context.
//
// Filtering removes synthetic pairs, or all supported server-search blocks when
// callers select stripAll for mapped-model rules. Empty messages receive
// role-specific text to retain their conversation positions. Declined and
// failed rewrites return supplied bytes; changed messages use encoding/json
// float64 decoding, so numbers inside that replaced messages array can lose
// precision. The surrounding top-level body is patched at messages after its
// replacement array is prepared, retaining other raw top-level fields.

// These type labels identify paired search invocation/result blocks whose
// replay eligibility follows their source IDs or the caller's stripAll rule.
const (
	blockTypeServerToolUse       = "server_tool_use"        // server search invocation block
	blockTypeWebSearchToolResult = "web_search_tool_result" // server search result block
)

// These quoted type names screen for supported search blocks before message
// decoding, letting unrelated history retain its supplied bytes and formatting.
var (
	patternServerToolUse       = []byte(`"server_tool_use"`)
	patternWebSearchToolResult = []byte(`"web_search_tool_result"`)
)

// FilterWebSearchHistoryBlocks removes synthetic server-search calls/results
// and optionally upstream-issued equivalents. Callers select stripAll from
// mapped-model replay rules and keep body storage stable while the byte view
// is inspected.
//
// Invocation id and result tool_use_id use ToolUseIDPrefix to identify
// emulated blocks. Other blocks and summary text retain their order; empty
// messages receive a role-specific placeholder. Changed message arrays use
// float64 number decoding, while other top-level bytes bypass that encoding.
// Missing arrays, no eligible removal, and decode/marshal/patch failures
// return the original body without adopting a replacement.
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
	// Prepare the selected message array before making content changes. Decode
	// failure retains the complete supplied body for later protocol or error handling.
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

		// Keep content intact until an excluded block is found. Copy its retained
		// prefix once so later removals leave the remaining block order available for replay.
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
		// Retain the message's conversation position after every block was removed.
		// Role-specific placeholder text supplies a nonempty replay content array.
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
		// Filtering changed the temporary array, but encoding did not produce its
		// replacement. Retain the original body so callers adopt no incomplete rewrite.
		return body
	}
	out, err := sjson.SetRawBytes(body, "messages", msgsBytes)
	if err != nil {
		// The prepared array could not be installed into the request. Keep the
		// complete original representation for the caller's unchanged forwarding path.
		return body
	}
	return out
}

// shouldStripWebSearchBlock reports whether a supported server-search block is
// excluded by stripAll or its synthetic prefix. It reads invocation id and
// result tool_use_id; unrelated block types stay eligible regardless of their
// names.
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
