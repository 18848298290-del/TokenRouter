package openai

import (
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// HTTP Responses visible-output observation checks content fields so empty
// lifecycle or item events can remain unobserved. Stream readers also use this
// classifier when the configured TTFT mode is visible; other timing modes
// select their own event rules. Streaming inspection validates full JSON,
// while buffered item inspection reads gjson paths and relies on separate
// caller validation when complete-document validity matters.

// StreamItemHasVisibleOutput reports whether recognized arguments, input,
// result, text, or transcript paths have nonempty gjson string forms. JSON
// containers, numbers, and booleans can qualify alongside strings. Callers
// validate the enclosing document and field types separately when they need
// a stricter output schema.
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

// StreamDataStartsVisibleOutput reports whether valid SSE data carries
// recognized content for HTTP visible-output observation and visible TTFT
// mode. The supplied eventType wins when nonblank; otherwise payload.type is
// used. Blank data, DONE, and invalid JSON return false. Content tests use
// gjson string forms, so JSON containers, numbers, and booleans can count as
// nonempty values; callers validate field types for a stricter output schema.
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

// ResponseBodyHasVisibleOutput checks top-level output items for the values
// recognized by StreamItemHasVisibleOutput. Missing output returns false;
// callers validate complete JSON separately because this path-based buffered
// check can inspect partially parsed payloads.
func ResponseBodyHasVisibleOutput(body []byte) bool {
	return slices.ContainsFunc(gjson.GetBytes(body, "output").Array(), StreamItemHasVisibleOutput)
}
