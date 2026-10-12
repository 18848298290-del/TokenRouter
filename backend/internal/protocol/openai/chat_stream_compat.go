package openai

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RawStreamTerminalState records terminal signals in a raw Chat Completions SSE stream.
// HTTP 200 alone cannot establish completion; missing terminal signals indicate truncation.
// Any of the following counts as termination:
//   - [DONE], the Chat Completions sentinel.
//   - A usage object, normally sent in the final include_usage chunk.
//   - A nonempty finish_reason such as stop, length, or tool_calls.
//
// This accepts upstreams that close after a final chunk or omit include_usage.
type RawStreamTerminalState struct {
	// sawDataLine enables SSE truncation checks after any data line.
	// Already-forwarded non-SSE bodies are exempt.
	sawDataLine     bool
	sawDone         bool
	sawUsage        bool
	sawFinishReason bool
}

// EnsureOpenAIChatStreamUsage enables usage in raw Chat Completions streaming requests.
// Forwarded usage supports downstream proxies and billing systems.
func EnsureOpenAIChatStreamUsage(body []byte) ([]byte, error) {
	updated, err := sjson.SetBytes(body, "stream_options.include_usage", true)
	if err != nil {
		return body, err
	}
	return updated, nil
}

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

// ExtractCCStreamUsage reads usage from one Chat Completions chunk.
// Callers can replace previously observed counts when an upstream repeats usage.
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

// ChatCompletionsChunkHasToolCallDelta reports whether any choice contains tool call deltas.
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

// OpenAIChatCompletionServiceTierEventType identifies terminal Chat chunks so their
// service tier can take precedence over earlier request-tier echoes.
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

// StripEmptyChatToolCallIdentityFromSSELine removes empty tool IDs and function names
// from a Chat Completions SSE data line, retaining the data prefix.
//
// Compatible upstreams can send valid identity in the first tool delta, then empty
// id and function.name fields alongside later argument fragments. Clients such as dsh
// overwrite existing values whenever a field is present, producing an unknown empty tool.
// Removing empty identity fields prevents that overwrite while retaining arguments.
// The rewrite is stateless and applies to raw Chat forwarding for every provider.
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

// StripEmptyChatToolCallIdentity removes empty string IDs and function names from
// choices[*].delta.tool_calls[*]. It retains arguments, indexes, types, other fields,
// and nonempty identities across all choices and tool indexes.
//
// Empty or invalid JSON, unrelated payloads, and deletion failures return the original
// payload with false. The boolean reports whether fields were removed.
func StripEmptyChatToolCallIdentity(payload []byte) ([]byte, bool) {
	if len(payload) == 0 {
		return payload, false
	}
	// Skip parsing chunks that cannot contain tool calls.
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

// ObserveDataLine records terminal signals from one SSE data payload. Callers trim whitespace first.
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
		// A null finish_reason becomes an empty string and does not terminate the stream.
		if strings.TrimSpace(choice.Get("finish_reason").String()) != "" {
			t.sawFinishReason = true
			return
		}
	}
}

// Terminated reports whether any terminal signal was observed.
func (t *RawStreamTerminalState) Terminated() bool {
	return t != nil && (t.sawDone || t.sawUsage || t.sawFinishReason)
}

// IsTruncated reports missing termination after SSE data or an entirely empty response.
// A started non-SSE response is exempt because it has no data lines or SSE terminator.
func (t *RawStreamTerminalState) IsTruncated(clientOutputStarted bool) bool {
	if t == nil || t.Terminated() {
		return false
	}
	return t.sawDataLine || !clientOutputStarted
}

// SawDataLine reports whether a data payload was observed, for truncation diagnostics.
func (t *RawStreamTerminalState) SawDataLine() bool { return t != nil && t.sawDataLine }
