package openai

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// silent_refusal.go detects empty stop completions for large Chat requests and
// decides when buffered client output can be released.
// A large Chat-compatible request can finish with stop while providing no
// output or accounting evidence. One detector per attempt records content,
// tools, reasoning, usage, errors, and the latest nonblank finish reason so
// response readers can hold downstream output while that case is still
// possible.
//
// Any recorded content or accounting/error evidence permits output release and
// prevents silent-refusal classification. A non-stop finish reason also
// releases output; a lone stop finish remains withheld and qualifies as silent
// refusal. Raw observations use field presence for some compatibility shapes,
// including empty tool and reasoning fields, while decoded chunks follow their
// typed fields. These observations identify the transport symptom; callers
// choose retry or failure handling before committing downstream bytes.

// SilentRefusalMinRequestBodyBytes limits guarded detection to large requests,
// leaving smaller Chat-compatible attempts on their ordinary downstream-output release path.
const SilentRefusalMinRequestBodyBytes = 64 * 1024

// ChatSilentRefusalDetector records evidence used to classify one large-request
// response attempt.
type ChatSilentRefusalDetector struct {
	// sawContent, sawToolCall, sawFunctionCall, sawUsage, sawError, and
	// sawReasoning accumulate disqualifying evidence. sawFinish and finishReason
	// retain the latest nonblank termination needed to distinguish silent stop
	// from an unfinished stream. Callers serialize this attempt-local state.
	enabled         bool   // large-request detection selection
	sawContent      bool   // observed nonempty text flag
	sawToolCall     bool   // observed tool-call evidence flag
	sawFunctionCall bool   // observed legacy function-call flag
	sawUsage        bool   // observed accounting evidence flag
	sawError        bool   // observed upstream failure flag
	sawReasoning    bool   // observed reasoning evidence flag
	sawFinish       bool   // observed nonblank finish flag
	finishReason    string // latest observed finish reason
}

// NewChatSilentRefusalDetector creates an attempt-local detector enabled at the
// request-byte threshold. Callers serialize observations and decisions; smaller
// requests use the disabled behavior that permits immediate client output.
func NewChatSilentRefusalDetector(requestBodyLen int) *ChatSilentRefusalDetector {
	return &ChatSilentRefusalDetector{
		enabled: requestBodyLen >= SilentRefusalMinRequestBodyBytes,
	}
}

// Enabled reports whether a nonnil detector is active for this attempt.
func (d *ChatSilentRefusalDetector) Enabled() bool {
	return d != nil && d.enabled
}

// ObserveSSELine records a recognized SSE event or data line when detection is
// enabled. Nil or disabled detectors ignore input; event headers contribute
// error/reasoning evidence and data lines follow payload observation.
func (d *ChatSilentRefusalDetector) ObserveSSELine(line string) {
	if d == nil || !d.enabled {
		return
	}
	if eventType, ok := wire.ExtractSSEEventLine(line); ok {
		d.observeEventType(eventType)
		return
	}
	if payload, ok := wire.ExtractSSEDataLine(line); ok {
		d.ObservePayload([]byte(payload))
	}
}

// ObservePayload records recognized response evidence from valid JSON data. Nil
// or disabled detectors, blank input, DONE markers, and malformed JSON are
// ignored; both Chat and Responses shapes can contribute observations to this
// attempt.
func (d *ChatSilentRefusalDetector) ObservePayload(payload []byte) {
	if d == nil || !d.enabled {
		return
	}
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	if !gjson.ValidBytes(payload) {
		return
	}

	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	d.observeEventType(eventType)

	if gjson.GetBytes(payload, "error").Exists() {
		d.sawError = true
	}
	if usage := gjson.GetBytes(payload, "usage"); usage.Exists() && usage.IsObject() {
		d.sawUsage = true
	}
	if usage := gjson.GetBytes(payload, "response.usage"); usage.Exists() && usage.IsObject() {
		d.sawUsage = true
	}

	// Accept both raw Chat and Responses evidence so the selected response
	// conversion can guard large requests without discarding signals from compatible upstream shapes.
	d.observeChatChoicesPayload(payload)
	d.observeResponsesPayload(payload, eventType)
}

// ObserveChatChunk records typed Chat content, tools, reasoning, usage, and
// finish evidence. Nil or disabled detectors ignore input; a present reasoning
// value or usage pointer contributes evidence even when it carries no text or
// tokens.
func (d *ChatSilentRefusalDetector) ObserveChatChunk(chunk wire.ChatCompletionsChunk) {
	if d == nil || !d.enabled {
		return
	}
	if chunk.Usage != nil {
		d.sawUsage = true
	}
	for _, choice := range chunk.Choices {
		if choice.FinishReason != nil {
			d.observeFinishReason(*choice.FinishReason)
		}
		delta := choice.Delta
		if delta.Content != nil && *delta.Content != "" {
			d.sawContent = true
		}
		if delta.ReasoningText() != nil {
			d.sawReasoning = true
		}
		if len(delta.ToolCalls) > 0 {
			d.sawToolCall = true
		}
	}
}

// ShouldReleaseClientOutput reports whether the attempt has enough evidence to
// release buffered downstream bytes. Nil or disabled detectors return true.
// Enabled detectors wait while no content, tool, reasoning, usage, error, or
// non-stop finish has been observed.
//
// An empty stop response remains withheld because it may be the silent-refusal
// case. A different nonblank finish reason releases output even with no
// content, preserving outcomes such as length termination. Callers observe each
// chunk before this decision and keep the detector local to the attempt so
// prior output cannot authorize a later request.
func (d *ChatSilentRefusalDetector) ShouldReleaseClientOutput() bool {
	if d == nil || !d.enabled {
		return true
	}
	if d.sawContent || d.sawToolCall || d.sawFunctionCall || d.sawUsage || d.sawError || d.sawReasoning {
		return true
	}
	return d.sawFinish && d.finishReason != "" && d.finishReason != "stop"
}

// IsSilentRefusal reports whether an enabled attempt ended at stop with no
// other recorded evidence. Nil or disabled detectors return false. Content,
// tool calls, reasoning, usage, or errors each disqualify the result even when
// another event supplied stop.
//
// The latest nonblank finish reason controls the final comparison, and absence
// of any finish keeps the result false. This distinguishes an empty completed
// response from an unfinished stream. Callers invoke the decision after
// processing their available response events and choose the retry or error path
// before releasing bytes still held by the detector.
func (d *ChatSilentRefusalDetector) IsSilentRefusal() bool {
	if d == nil || !d.enabled {
		return false
	}
	return !d.sawContent &&
		!d.sawToolCall &&
		!d.sawFunctionCall &&
		!d.sawUsage &&
		!d.sawError &&
		!d.sawReasoning &&
		d.sawFinish &&
		d.finishReason == "stop"
}

// observeEventType records error and reasoning evidence from a trimmed event
// name. Empty names are ignored; recognized reasoning substrings count even
// when that structural event carries no reasoning text.
func (d *ChatSilentRefusalDetector) observeEventType(eventType string) {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return
	}
	if eventType == "error" || eventType == "response.failed" {
		d.sawError = true
	}
	if strings.Contains(eventType, "reasoning") || strings.Contains(eventType, "reasoning_summary") {
		d.sawReasoning = true
	}
}

// observeFinishReason records the latest trimmed nonblank finish reason for
// this attempt. Blank reasons leave earlier finish evidence intact, so missing
// fields cannot erase a previously observed terminal outcome.
func (d *ChatSilentRefusalDetector) observeFinishReason(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	d.sawFinish = true
	d.finishReason = reason
}

// observeChatChoicesPayload records raw Chat choice evidence from delta and
// finish fields. Missing or nonarray choices are ignored; tool and reasoning
// field presence qualifies even when those raw values are empty.
func (d *ChatSilentRefusalDetector) observeChatChoicesPayload(payload []byte) {
	choices := gjson.GetBytes(payload, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return
	}
	for _, choice := range choices.Array() {
		if finish := choice.Get("finish_reason"); finish.Exists() {
			d.observeFinishReason(finish.String())
		}
		delta := choice.Get("delta")
		if !delta.Exists() {
			continue
		}
		if content := delta.Get("content"); content.Exists() && content.String() != "" {
			d.sawContent = true
		}
		if delta.Get("tool_calls").Exists() {
			d.sawToolCall = true
		}
		if delta.Get("function_call").Exists() {
			d.sawFunctionCall = true
		}
		if delta.Get("reasoning").Exists() ||
			delta.Get("reasoning_content").Exists() ||
			delta.Get("reasoning_summary").Exists() {
			d.sawReasoning = true
		}
	}
}

// observeResponsesPayload records Responses event and completed-output evidence
// for this attempt. Completed/done implies stop, incomplete implies length, and
// recognized output tools or reasoning items prevent an otherwise empty
// completion from becoming silent refusal.
func (d *ChatSilentRefusalDetector) observeResponsesPayload(payload []byte, eventType string) {
	switch eventType {
	case "response.output_text.delta":
		if gjson.GetBytes(payload, "delta").String() != "" {
			d.sawContent = true
		}
	case "response.output_item.added":
		switch strings.TrimSpace(gjson.GetBytes(payload, "item.type").String()) {
		case "function_call":
			d.sawToolCall = true
		case "reasoning":
			d.sawReasoning = true
		}
	case "response.function_call_arguments.delta":
		d.sawToolCall = true
	case "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		d.sawReasoning = true
	case "response.completed", "response.done":
		d.observeFinishReason("stop")
	case "response.incomplete":
		d.observeFinishReason("length")
	case "response.failed":
		d.sawError = true
	}

	if output := gjson.GetBytes(payload, "response.output"); output.Exists() && output.IsArray() {
		for _, item := range output.Array() {
			switch strings.TrimSpace(item.Get("type").String()) {
			case "function_call":
				d.sawToolCall = true
			case "reasoning":
				d.sawReasoning = true
			case "message":
				d.observeResponseMessageItem(item)
			}
		}
	}
}

// observeResponseMessageItem records content when a response message has a
// nonempty text part. Missing or nonarray content contributes nothing; the
// first textual part is enough to disqualify silent refusal.
func (d *ChatSilentRefusalDetector) observeResponseMessageItem(item gjson.Result) {
	content := item.Get("content")
	if !content.Exists() || !content.IsArray() {
		return
	}
	for _, part := range content.Array() {
		if part.Get("text").String() != "" {
			d.sawContent = true
			return
		}
	}
}
