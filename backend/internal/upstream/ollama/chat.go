package ollama

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	wireopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// chat.go translates Ollama Cloud reasoning fields between request history and
// compatible JSON or streamed responses.
// Ollama Cloud chat payloads can use reasoning or thinking where compatible
// clients use reasoning_content. Request history adds reasoning from a nonempty
// assistant reasoning_content value only when neither alternate string is
// already nonempty. Response messages and deltas instead add reasoning_content
// from reasoning, then thinking, while any existing reasoning_content value
// takes precedence.
//
// The source fields remain present, allowing the same payload to retain its
// provider representation alongside the compatibility field. Invalid JSON,
// missing array containers, and failed JSON updates return the supplied bytes.
// SSE conversion operates on one data line at a time and retains its existing
// prefix spacing; terminal markers and lines requiring no normalization retain
// their original spelling.

// NormalizeOllamaCloudChatCompletionsRequest adds reasoning to assistant
// history entries carrying nonempty reasoning_content. It accepts JSON with a
// messages array and returns the supplied bytes for invalid input or when no
// entry needs the compatibility field.
//
// A nonempty reasoning or thinking string takes precedence, including strings
// containing only whitespace. The source reasoning_content and other message
// fields remain in the patched representation. Each update accumulates on the
// prior result; an update error returns the supplied body, so callers receive
// one completed normalization result.
func NormalizeOllamaCloudChatCompletionsRequest(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	updated := body
	changed := false
	for i, msg := range messages.Array() {
		if msg.Get("role").String() != "assistant" {
			continue
		}
		reasoningContent, ok := jsonNonEmptyString(msg.Get("reasoning_content"))
		if !ok {
			continue
		}
		// An existing provider reasoning alias takes precedence over
		// copied history. Keep that content associated with the
		// assistant message when adding compatibility fields.
		if _, has := jsonNonEmptyString(msg.Get("reasoning")); has {
			continue
		}
		if _, has := jsonNonEmptyString(msg.Get("thinking")); has {
			continue
		}
		next, err := sjson.SetBytes(updated, "messages."+strconv.Itoa(i)+".reasoning", reasoningContent)
		if err != nil {
			// A failed history patch invalidates this normalization
			// attempt. Return the supplied body so callers cannot
			// forward a partially normalized set of assistant
			// messages.
			return body
		}
		updated = next
		changed = true
	}
	if !changed {
		return body
	}
	return updated
}

// NormalizeOllamaCloudChatCompletionsResponseJSON fills missing
// reasoning_content in each choice's message and delta objects. Nonempty
// reasoning is preferred over thinking, and any existing reasoning_content
// value suppresses insertion, including an empty string or null.
//
// The provider's source field remains present beside the compatibility field.
// Updates accumulate across eligible containers, while invalid JSON, absent
// choice arrays, unchanged payloads, and update failures return the supplied
// body. Callers can apply this to complete responses or individual streaming
// JSON payloads.
func NormalizeOllamaCloudChatCompletionsResponseJSON(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	choices := gjson.GetBytes(body, "choices")
	if !choices.IsArray() {
		return body
	}
	updated := body
	changed := false
	for i, choice := range choices.Array() {
		for _, container := range []string{"message", "delta"} {
			obj := choice.Get(container)
			if !obj.Exists() || !obj.IsObject() {
				continue
			}
			// Presence is enough to reserve the client's reasoning
			// field. Retain null and empty values too so
			// normalization cannot replace an upstream choice about
			// that representation.
			if obj.Get("reasoning_content").Exists() {
				continue
			}
			// Response aliases prefer reasoning, then thinking,
			// because the compatibility field needs one selected
			// string while both provider fields remain in the
			// payload.
			src, ok := jsonNonEmptyString(obj.Get("reasoning"))
			if !ok {
				src, ok = jsonNonEmptyString(obj.Get("thinking"))
			}
			if !ok {
				continue
			}
			next, err := sjson.SetBytes(updated, "choices."+strconv.Itoa(i)+"."+container+".reasoning_content", src)
			if err != nil {
				// A failed response patch discards accumulated
				// alias additions, giving the caller its
				// supplied payload when the full conversion
				// cannot be completed.
				return body
			}
			updated = next
			changed = true
		}
	}
	if !changed {
		return body
	}
	return updated
}

// NormalizeOllamaCloudChatCompletionsSSELine normalizes one data payload and
// retains its prefix spacing. Empty payloads, DONE markers, other line types,
// and unchanged JSON return the original line; callers manage complete SSE
// event framing separately.
func NormalizeOllamaCloudChatCompletionsSSELine(line string) string {
	payload, ok := wireopenai.ExtractSSEDataLine(line)
	if !ok {
		return line
	}
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" || trimmed == "[DONE]" {
		return line
	}
	rewritten := NormalizeOllamaCloudChatCompletionsResponseJSON([]byte(payload))
	if string(rewritten) == payload {
		return line
	}
	prefixLen := len(line) - len(payload)
	// If payload extraction cannot identify a retained prefix, keep the
	// original line rather than joining rewritten JSON to an invalid
	// framing offset.
	if prefixLen < 0 {
		return line
	}
	return line[:prefixLen] + string(rewritten)
}

// jsonNonEmptyString returns a JSON string and whether it contains bytes.
// Whitespace counts as nonempty, so callers retain the supplied reasoning text
// exactly when selecting compatibility aliases.
func jsonNonEmptyString(v gjson.Result) (string, bool) {
	if v.Type != gjson.String || v.Str == "" {
		return "", false
	}
	return v.Str, true
}
