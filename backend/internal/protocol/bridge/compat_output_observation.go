package bridge

import (
	"bytes"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// compat_output_observation.go detects semantic output and completion evidence
// in compatibility responses.
// Converted outputs use Messages, Responses, or Chat event shapes even when
// they originate from the same Gemini execution path. Output observation reads
// their recognized fields and labels before the caller decides how content and
// terminal evidence affects delivery or completion.

// CompatOutputMeaning reads content and terminal evidence from data lines in
// a converted frame. DONE and recognized terminal labels return terminal;
// recognized delta/block/call forms can return content. The first recognized
// decision wins rather than combining every line's evidence.
// JSON validity and field types are not checked here: gjson String forms can
// make nonstring values count as nonempty. Callers apply this to their emitted
// compatibility frames and retain separate validation where needed.
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

// CompatJSONHasContent checks recognized Messages, Chat, and Responses fields
// for content evidence, including reasoning, refusals, and tool-call types.
// Missing recognized content returns false; some tool types count without
// populated IDs or arguments. Complete JSON and field-type validity are left
// separate from these gjson path and String-form observations.
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
