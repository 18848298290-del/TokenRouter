package openai

import (
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

func StreamItemHasVisibleOutput(item gjson.Result) bool {
	if item.Get("arguments").String() != "" || item.Get("input").String() != "" || item.Get("result").String() != "" {
		return true
	}
	for _, path := range []string{"content", "summary"} {
		for _, part := range item.Get(path).Array() {
			if part.Get("text").String() != "" || part.Get("transcript").String() != "" {
				return true
			}
		}
	}
	return false
}

// StreamDataStartsVisibleOutput detects client-usable content for TTFT; structural progress alone returns false.
func StreamDataStartsVisibleOutput(data, eventType string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" || trimmed == "[DONE]" || !gjson.Valid(trimmed) {
		return false
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		eventType = strings.TrimSpace(gjson.Get(trimmed, "type").String())
	}
	if strings.HasSuffix(eventType, ".delta") {
		delta := gjson.Get(trimmed, "delta")
		return delta.Exists() && delta.String() != ""
	}
	switch eventType {
	case "response.output_text.done",
		"response.reasoning_summary_text.done",
		"response.reasoning_text.done",
		"response.audio_transcript.done":
		return gjson.Get(trimmed, "text").String() != ""
	case "response.function_call_arguments.done":
		return gjson.Get(trimmed, "arguments").String() != ""
	case "response.custom_tool_call_input.done":
		return gjson.Get(trimmed, "input").String() != ""
	case "response.image_generation_call.partial_image":
		return gjson.Get(trimmed, "partial_image_b64").String() != ""
	case "response.content_part.added", "response.content_part.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		part := gjson.Get(trimmed, "part")
		return part.Get("text").String() != "" || part.Get("transcript").String() != ""
	case "response.output_item.added", "response.output_item.done":
		return StreamItemHasVisibleOutput(gjson.Get(trimmed, "item"))
	case "response.completed", "response.done":
		if slices.ContainsFunc(gjson.Get(trimmed, "response.output").Array(), StreamItemHasVisibleOutput) {
			return true
		}
	}
	return false
}

// ResponseBodyHasVisibleOutput checks output items for text, reasoning, or tool content.
func ResponseBodyHasVisibleOutput(body []byte) bool {
	return slices.ContainsFunc(gjson.GetBytes(body, "output").Array(), StreamItemHasVisibleOutput)
}
