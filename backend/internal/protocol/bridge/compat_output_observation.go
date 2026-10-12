package bridge

import (
	"bytes"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// CompatOutputMeaning returns content-present and terminal flags for a complete frame emitted here.
// Text, reasoning, and tool calls count as content.
func CompatOutputMeaning(frame []byte) (bool, bool) {
	for line := range bytes.SplitSeq(frame, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(data, []byte("[DONE]")) {
			return false, true
		}
		value := gjson.ParseBytes(data)
		kind := value.Get("type").String()
		switch kind {
		case "message_stop", "response.completed":
			return false, true
		case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta":
			return value.Get("delta").String() != "", false
		case "response.output_item.added":
			return value.Get("item.type").String() == "function_call", false
		case "content_block_start":
			block := value.Get("content_block")
			return block.Get("type").String() == "tool_use" || block.Get("text").String() != "" || block.Get("thinking").String() != "", false
		case "content_block_delta":
			delta := value.Get("delta")
			return delta.Get("text").String() != "" || delta.Get("thinking").String() != "" || delta.Get("partial_json").String() != "", false
		}
		for _, choice := range value.Get("choices").Array() {
			delta := choice.Get("delta")
			if delta.Get("content").String() != "" || delta.Get("reasoning_content").String() != "" || len(delta.Get("tool_calls").Array()) > 0 {
				return true, false
			}
		}
	}
	return false, false
}

// CompatJSONHasContent checks completed JSON for text, reasoning, refusal, or tool calls.
func CompatJSONHasContent(data []byte) bool {
	if anthropic.ObserveMessage(string(data)).Semantic {
		return true
	}
	value := gjson.ParseBytes(data)
	for _, choice := range value.Get("choices").Array() {
		message := choice.Get("message")
		if message.Get("content").String() != "" || message.Get("reasoning_content").String() != "" || len(message.Get("tool_calls").Array()) > 0 {
			return true
		}
	}
	for _, item := range value.Get("output").Array() {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			return true
		}
		for _, part := range item.Get("content").Array() {
			if part.Get("text").String() != "" || part.Get("refusal").String() != "" {
				return true
			}
		}
		for _, part := range item.Get("summary").Array() {
			if part.Get("text").String() != "" {
				return true
			}
		}
	}
	return false
}
