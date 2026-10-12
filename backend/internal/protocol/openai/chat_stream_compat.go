package openai

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Tool-argument fragments can carry empty identity fields after a first chunk
// supplied their ID and name. A client that overwrites every present field
// would replace its stored identity with empty text, so raw Chat cleanup
// removes those exact empty keys while retaining the argument fragments. The
// repair is stateless and keeps every call's array position.
//
// Raw streams can terminate with a done sentinel, a usage object, or a
// nonblank finish reason. Those observations accumulate independently.
// Truncation also depends on whether data payloads were observed and whether
// output began: an already forwarded non-SSE response can finish without SSE
// markers, while an empty or unterminated SSE read lacks the required
// evidence.

// RawStreamTerminalState accumulates raw Chat stream terminal evidence for one
// read. Callers trim each data payload and serialize state access; the zero
// value is ready to observe a new stream.
type RawStreamTerminalState struct {
	// sawDataLine records SSE participation. sawDone, sawUsage, and
	// sawFinishReason accumulate independent termination signals; any one
	// ends truncation detection, while data presence removes the non-SSE exemption.
	sawDataLine     bool // observed SSE data payload
	sawDone         bool // observed exact done sentinel
	sawUsage        bool // observed usage object value
	sawFinishReason bool // observed nonblank finish reason
}

// EnsureOpenAIChatStreamUsage sets stream_options.include_usage to true for a
// Chat request body. Existing false values are overwritten, and patch errors
// return the supplied body; callers choose whether the request follows the raw
// streaming path.
func EnsureOpenAIChatStreamUsage(body []byte) ([]byte, error) {
	updated, err := sjson.SetBytes(body, "stream_options.include_usage", true)
	if err != nil {
		return body, err
	}
	return updated, nil
}

// IsOpenAIChatUsageOnlyStreamChunk reports an existing usage key with an empty
// choices array. Key presence can include null usage, and path inspection does
// not validate the complete document or its token counts.
func IsOpenAIChatUsageOnlyStreamChunk(payload string) bool {
	if strings.TrimSpace(payload) == "" {
		return false
	}
	if !gjson.Get(payload, "usage").Exists() {
		return false
	}
	choices := gjson.Get(payload, "choices")
	return choices.Exists() && choices.IsArray() && len(choices.Array()) == 0
}

// ExtractCCStreamUsage reads an object-shaped usage value from one Chat
// payload. Missing or nonobject usage returns nil; an empty object returns a
// present zero-count observation. Values follow OpenAIUsageFromGJSON coercion,
// and callers decide whether the observation replaces earlier stream counts.
func ExtractCCStreamUsage(payload string) *ForwardUsage {
	usageResult := gjson.Get(payload, "usage")
	if !usageResult.Exists() || !usageResult.IsObject() {
		return nil
	}
	u, ok := OpenAIUsageFromGJSON(usageResult)
	if !ok {
		return nil
	}
	return &u
}

// ChatChunkStartsResponsesOutput reports content/reasoning pointer presence or
// any tool-call records in a choice. Empty pointed-to text still qualifies,
// and a nil chunk returns false; this signal identifies structural output for
// Responses conversion.
func ChatChunkStartsResponsesOutput(chunk *ChatCompletionsChunk) bool {
	if chunk == nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil || choice.Delta.ReasoningText() != nil || len(choice.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// ChatCompletionsChunkHasToolCallDelta reports whether any choice contains
// tool-call records in its delta.
func ChatCompletionsChunkHasToolCallDelta(chunk *ChatCompletionsChunk) bool {
	if chunk == nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if len(choice.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// OpenAIChatCompletionServiceTierEventType returns response.completed for
// usage-only payloads or choices with a nonblank gjson finish-reason string.
// Other payloads return empty text; this classification supplies metadata
// precedence, rather than validating successful delivery.
func OpenAIChatCompletionServiceTierEventType(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	if IsOpenAIChatUsageOnlyStreamChunk(string(payload)) {
		return "response.completed"
	}
	for _, choice := range gjson.GetBytes(payload, "choices").Array() {
		if strings.TrimSpace(choice.Get("finish_reason").String()) != "" {
			return "response.completed"
		}
	}
	return ""
}

// StripEmptyChatToolCallIdentityFromSSELine delegates identity cleanup for an
// SSE data payload and retains its exact line prefix. Non-data lines, blank
// payloads, the done sentinel, and unchanged cleanup results keep the supplied
// line.
func StripEmptyChatToolCallIdentityFromSSELine(line string) string {
	payload, ok := ExtractSSEDataLine(line)
	if !ok {
		return line
	}
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" || trimmed == "[DONE]" {
		return line
	}
	rewritten, changed := StripEmptyChatToolCallIdentity([]byte(payload))
	if !changed {
		return line
	}
	prefixLen := len(line) - len(payload)
	if prefixLen < 0 {
		return line
	}
	return line[:prefixLen] + string(rewritten)
}

// StripEmptyChatToolCallIdentity removes exact empty-string tool IDs and
// function names from choices[*].delta.tool_calls[*]. Null, whitespace-only,
// and nonstring identities remain. It requires valid JSON after its literal
// tool_calls screen; an escaped spelling that misses this screen is returned
// unchanged.
//
// Removing those keys lets a client that overwrites every present identity
// keep its earlier ID and name while appending later argument fragments. Every
// choice and call-array position is visited. Arguments, indexes, and other
// fields remain in the raw JSON; any deletion failure returns the supplied
// payload with false, discarding the candidate sequence.
func StripEmptyChatToolCallIdentity(payload []byte) ([]byte, bool) {
	if len(payload) == 0 {
		return payload, false
	}
	// The literal screen avoids parsing ordinary content chunks. Escaped key
	// spellings can miss this screen, so this helper is a targeted raw-stream repair.
	if !bytes.Contains(payload, []byte("tool_calls")) {
		return payload, false
	}
	if !gjson.ValidBytes(payload) {
		return payload, false
	}
	choices := gjson.GetBytes(payload, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return payload, false
	}
	// Keep candidate deletions separate from the result decision. A later error
	// rejects all earlier removals and returns the source payload to the reader.
	updated := payload
	changed := false
	for ci, choice := range choices.Array() {
		delta := choice.Get("delta")
		if !delta.Exists() || !delta.IsObject() {
			continue
		}
		toolCalls := delta.Get("tool_calls")
		if !toolCalls.Exists() || !toolCalls.IsArray() {
			continue
		}
		for ti, tc := range toolCalls.Array() {
			if id := tc.Get("id"); id.Exists() && id.Type == gjson.String && id.Str == "" {
				next, err := sjson.DeleteBytes(updated, "choices."+strconv.Itoa(ci)+".delta.tool_calls."+strconv.Itoa(ti)+".id")
				if err != nil {
					return payload, false
				}
				updated = next
				changed = true
			}
			if name := tc.Get("function.name"); name.Exists() && name.Type == gjson.String && name.Str == "" {
				next, err := sjson.DeleteBytes(updated, "choices."+strconv.Itoa(ci)+".delta.tool_calls."+strconv.Itoa(ti)+".function.name")
				if err != nil {
					return payload, false
				}
				updated = next
				changed = true
			}
		}
	}
	if !changed {
		return payload, false
	}
	return updated, true
}

// ObserveDataLine records one caller-trimmed SSE payload and marks that data
// was seen even when the payload is empty or malformed. Exact [DONE], an
// object-shaped usage value, or a nonblank gjson finish-reason string each
// supplies terminal evidence. A nil receiver has no effect.
//
// The flags accumulate across calls, so one later payload cannot erase a prior
// terminal observation. Usage-object presence is sufficient even for an empty
// object, and finish-reason path inspection does not validate the enclosing
// JSON. IsTruncated combines this evidence with whether the caller already
// forwarded output.
func (t *RawStreamTerminalState) ObserveDataLine(payload string) {
	if t == nil {
		return
	}
	t.sawDataLine = true
	if payload == "[DONE]" {
		t.sawDone = true
		return
	}
	if usage := gjson.Get(payload, "usage"); usage.Exists() && usage.IsObject() {
		t.sawUsage = true
	}
	if t.sawFinishReason {
		return
	}
	for _, choice := range gjson.Get(payload, "choices").Array() {
		// gjson string conversion maps null to empty text. Require a nonblank
		// result so unfinished choice chunks cannot supply this terminal signal.
		if strings.TrimSpace(choice.Get("finish_reason").String()) != "" {
			t.sawFinishReason = true
			return
		}
	}
}

// Terminated reports whether any done, usage-object, or finish-reason evidence
// was recorded.
func (t *RawStreamTerminalState) Terminated() bool {
	return t != nil && (t.sawDone || t.sawUsage || t.sawFinishReason)
}

// IsTruncated reports missing terminal evidence after data was observed or
// before any client output began. Nil state and a recorded terminal signal
// return false. The caller passes whether output actually started, since these
// flags cannot detect a forwarded non-SSE body.
//
// A started response with no data observations is exempt: it can be plain JSON
// returned for a streaming request. Once an SSE payload was seen, that
// exemption ends, and a response lacking all three terminal signals is
// considered truncated regardless of previously forwarded content.
func (t *RawStreamTerminalState) IsTruncated(clientOutputStarted bool) bool {
	if t == nil || t.Terminated() {
		return false
	}
	return t.sawDataLine || !clientOutputStarted
}

// SawDataLine reports whether this state observed any SSE data payload at all.
func (t *RawStreamTerminalState) SawDataLine() bool { return t != nil && t.sawDataLine }
