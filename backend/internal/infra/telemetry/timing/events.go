package timing

import (
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// events.go classifies decoded protocol events for request performance observations.
// Upstream arrival and downstream writes use identical content rules so protocol
// conversion cannot silently introduce a different definition of useful output.
// The classifier inspects known payload fields and never records payload text.
//
// Each protocol puts content in different envelopes: a Messages block, a Chat
// choice, a Responses delta or item, or a Google candidate. Recognizing those
// fields directly prevents response identifiers, encrypted reasoning and signatures
// from becoming useful-output evidence. Upstream collectors use the result to
// delimit generation, and downstream writers use it after successful delivery.
// These observations stay separate from protocol retry commitment and billing.

// DataEvent excludes explicit protocol heartbeats from data event timing.
// A payload type takes precedence over the optional SSE event name. Empty
// Claude text and thinking deltas remain keepalives in either encoding.
func DataEvent(data, event string) bool {
	if strings.TrimSpace(data) == "" {
		return false
	}
	kind := gjson.Get(data, "type").String()
	// Payload type wins over an inconsistent event header, matching protocol
	// normalization that retains real content inside a mislabeled ping frame.
	if kind == "" {
		kind = event
	}
	// Claude Code keepalive can be encoded as an empty content delta. It must
	// not establish response delivery while the actual upstream is still idle.
	if kind == "content_block_delta" {
		delta := gjson.Get(data, "delta")
		if delta.Get("type").String() == "text_delta" && delta.Get("text").String() == "" {
			return false
		}
		if delta.Get("type").String() == "thinking_delta" && delta.Get("thinking").String() == "" {
			return false
		}
	}
	return kind != "ping" && kind != "heartbeat" && kind != "keepalive"
}

// UsefulOutput recognizes text, plaintext reasoning and tool content across
// upstream and client wire protocols. Signature, encrypted reasoning and lifecycle metadata
// are excluded, independently of each platform's business-semantic observation.
//
// Messages, Responses, Chat and Google output retain different nesting and
// delta shapes after conversion. This predicate follows their known content
// positions rather than searching arbitrary JSON for strings. The caller runs
// it on complete received or successfully written events, and its result changes
// diagnostic timestamps without changing retry eligibility or settlement.
func UsefulOutput(data, event string) bool {
	v := gjson.Parse(data)
	kind := v.Get("type").String()
	// SSE can carry the event type outside JSON. Preserve payload precedence
	// when both exist, as the protocol adapters do when normalizing named events.
	if kind == "" {
		kind = event
	}
	switch kind {
	case "content_block_delta":
		d := v.Get("delta")
		return nonemptyString(d.Get("text")) || nonemptyString(d.Get("thinking")) || nonemptyString(d.Get("partial_json"))
	case "content_block_start":
		return content(v.Get("content_block"))
	case "message_start":
		return content(v.Get("message"))
	}
	if strings.HasPrefix(kind, "response.") {
		if strings.HasSuffix(kind, ".delta") && !strings.Contains(kind, "encrypted") && !strings.Contains(kind, "signature") &&
			(strings.Contains(kind, "text") || strings.Contains(kind, "reasoning") || strings.HasSuffix(kind, "_arguments.delta") || strings.HasSuffix(kind, "_input.delta")) {
			return nonemptyString(v.Get("delta"))
		}
		for _, key := range []string{"text", "arguments", "input", "item", "part", "response"} {
			if content(v.Get(key)) {
				return true
			}
		}
	}
	for _, choice := range v.Get("choices").Array() {
		if content(choice.Get("delta")) || content(choice.Get("message")) {
			return true
		}
	}
	for _, key := range []string{"candidates", "response.candidates"} {
		for _, candidate := range v.Get(key).Array() {
			if content(candidate.Get("content")) {
				return true
			}
		}
	}
	return false
}

// content inspects only known content-bearing fields and tool identities.
// It never walks arbitrary metadata, so model names, response IDs and encrypted
// payloads cannot become visible-output evidence through generic recursion.
//
// Arrays are traversed only after a recognized content field selects them.
// Plaintext text and reasoning, explicit tool identities and argument fragments
// establish output; absent values stop recursion. Tool starts can therefore
// qualify before their arguments arrive, while an ordinary response ID or an
// encrypted reasoning block cannot qualify simply because its string is nonempty.
func content(v gjson.Result) bool {
	if !v.Exists() {
		return false
	}
	if v.Type == gjson.String {
		return v.String() != ""
	}
	if v.IsArray() {
		return slices.ContainsFunc(v.Array(), content)
	}
	for _, key := range []string{"text", "thinking", "reasoning_content", "reasoning", "arguments", "partial_json"} {
		if value := v.Get(key); value.Type == gjson.String && value.String() != "" {
			return true
		}
	}
	kind := v.Get("type").String()
	if kind == "tool_use" || kind == "server_tool_use" || kind == "function_call" || kind == "custom_tool_call" {
		if nonemptyString(v.Get("name")) || toolInput(v.Get("input")) {
			return true
		}
	}
	if nonemptyString(v.Get("functionCall.name")) || toolInput(v.Get("functionCall.args")) || nonemptyString(v.Get("function.name")) || toolInput(v.Get("function.arguments")) {
		return true
	}
	// Legacy Chat streams deliver a single function_call instead of tool_calls.
	// Its name and argument fragments carry the same useful content as modern tools.
	if nonemptyString(v.Get("function_call.name")) || toolInput(v.Get("function_call.arguments")) {
		return true
	}
	for _, key := range []string{"content", "parts", "output", "summary", "tool_calls"} {
		if content(v.Get(key)) {
			return true
		}
	}
	return false
}

// nonemptyString recognizes actual string content while retaining whitespace output tokens.
func nonemptyString(value gjson.Result) bool {
	return value.Type == gjson.String && value.String() != ""
}

// toolInput accepts argument fragments or populated structured tool inputs.
// Empty containers and scalar metadata are placeholders; they cannot establish useful
// output before a tool name or actual argument content arrives.
func toolInput(value gjson.Result) bool {
	if nonemptyString(value) {
		return true
	}
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	present := false
	value.ForEach(func(gjson.Result, gjson.Result) bool { present = true; return false })
	return present
}
