package anthropic

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// message_cache.go removes message cache markers and inserts breakpoints at
// selected conversation positions.
// Message history grows as conversations continue, so retaining every earlier
// cache marker would accumulate breakpoints. Preparation can clear message
// markers and rebuild them for the final message and, after four messages, the
// second-most-recent user message.
//
// Adding a marker to string content converts that message into a text-block
// array, while object blocks in array content retain their fields and nonempty
// TTL values.
// Selection here concerns message positions; EnforceCacheControlLimit later
// handles the combined system, tool, and message count. JSON edits are best
// effort, and the raw string conversion uses Go quoting, so unusual text
// escapes need care before treating its output as valid JSON.

// StripMessageCacheControl deletes message content-block markers and returns
// successful edits. Call it before rebuilding message breakpoints so appended
// history does not keep earlier markers; nonarray messages or content remain
// untouched.
func StripMessageCacheControl(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	// Delete only markers inside array message content before rebuilding message
	// anchors, keeping system and tool markers available for their separate
	// preparation steps.
	msgIdx := -1
	messages.ForEach(func(_, msg gjson.Result) bool {
		msgIdx++
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		blockIdx := -1
		content.ForEach(func(_, block gjson.Result) bool {
			blockIdx++
			if !block.Get("cache_control").Exists() {
				return true
			}
			path := fmt.Sprintf("messages.%d.content.%d.cache_control", msgIdx, blockIdx)
			// A failed path deletion keeps that marker in the working body; later
			// content blocks still undergo the same message-cache cleanup pass.
			if next, err := sjson.DeleteBytes(body, path); err == nil {
				body = next
			}
			return true
		})
		return true
	})
	return body
}

// AddMessageCacheBreakpoints marks the final message and, with at least four
// messages, the second-most-recent user message. Empty or nonarray history
// returns directly, and StripMessageCacheControl can clear old message markers
// first.
//
// The final message can be an assistant message; the earlier anchor is selected
// separately by scanning user roles backward. Each selected message delegates
// content conversion and TTL handling. Two message anchors leave space for one
// system and one tool marker, but this helper does not enforce their combined
// count; EnforceCacheControlLimit owns that later reduction.
func AddMessageCacheBreakpoints(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	arr := messages.Array()
	if len(arr) == 0 {
		return body
	}

	body = InjectCacheControlOnLastContentBlock(body, len(arr)-1, &arr[len(arr)-1])

	// The earlier anchor is enabled after four history entries, then selected by
	// user role rather than fixed index so assistant turns do not shift its
	// identity.
	if len(arr) >= 4 {
		userCount := 0
		for i := len(arr) - 1; i >= 0; i-- {
			if arr[i].Get("role").String() != "user" {
				continue
			}
			userCount++
			if userCount == 2 {
				body = InjectCacheControlOnLastContentBlock(body, i, &arr[i])
				break
			}
		}
	}

	return body
}

// InjectCacheControlOnLastContentBlock attaches cache control at idx using the
// supplied message snapshot. Callers align msg with that body's index and pass
// a nonnil result. Content that is neither a string nor a nonempty array leaves
// the body unchanged.
//
// String content, including empty text, becomes one block through raw JSON
// assembly. For an array of object blocks, a nonempty ttl string result keeps
// the final block's marker. An absent or empty ttl receives the default while
// other marker fields remain. Go-only string escapes can make the assembled
// JSON invalid, and failed sjson edits retain the corresponding supplied body.
func InjectCacheControlOnLastContentBlock(body []byte, idx int, msg *gjson.Result) []byte {
	content := msg.Get("content")

	// String content needs a block object to hold a marker. Its Go-quoted text is
	// inserted as raw JSON here, so unusual Go-only escapes remain an input
	// limitation.
	if content.Type == gjson.String {
		text := content.String()
		blockRaw := fmt.Sprintf(
			`[{"type":"text","text":%s,"cache_control":{"type":"ephemeral","ttl":%q}}]`,
			MustJSONString(text), DefaultCacheControlTTL,
		)
		if next, err := sjson.SetRawBytes(body, fmt.Sprintf("messages.%d.content", idx), []byte(blockRaw)); err == nil {
			body = next
		}
		return body
	}

	if !content.IsArray() {
		return body
	}
	contentArr := content.Array()
	if len(contentArr) == 0 {
		return body
	}
	lastBlockIdx := len(contentArr) - 1
	lastBlock := contentArr[lastBlockIdx]

	if cc := lastBlock.Get("cache_control"); cc.Exists() && cc.Get("ttl").String() != "" {
		return body
	}

	pathPrefix := fmt.Sprintf("messages.%d.content.%d.cache_control", idx, lastBlockIdx)
	existingCC := lastBlock.Get("cache_control")
	// Retain existing marker attributes while filling an absent or empty TTL;
	// replacing the whole cache object would discard additional fields supplied
	// by the client.
	if existingCC.Exists() {
		if next, err := sjson.SetBytes(body, pathPrefix+".ttl", DefaultCacheControlTTL); err == nil {
			body = next
		}
		return body
	}
	raw := fmt.Sprintf(`{"type":"ephemeral","ttl":%q}`, DefaultCacheControlTTL)
	if next, err := sjson.SetRawBytes(body, pathPrefix, []byte(raw)); err == nil {
		body = next
	}
	return body
}

// MustJSONString returns Go's quoted string representation for raw assembly
// callers. Go escapes such as hexadecimal byte escapes can be invalid JSON, so
// callers need JSON-safe input or their own encoding.
func MustJSONString(s string) string {
	return fmt.Sprintf("%q", s)
}
