package openai

import (
	"encoding/json"
	"strings"
)

// Responses deltas need zero-valued position indexes, and newly opened items
// need empty argument strings or arrays before content arrives. Ordinary
// omitempty tags would remove these fields, so event-kind selection constructs
// maps whose required values stay present. Function, custom, and discovery
// calls also use different parameter fields even though the bridge accumulates
// argument text for them.
//
// Known item and part helpers retain the fields represented by their emitted
// shape. Empty message and reasoning arrays remain arrays, while a nil item
// becomes an empty object. Lifecycle events continue through ordinary struct
// encoding; callers that need unknown item extensions keep raw event data
// rather than relying on the reduced typed shapes assembled here.

// MarshalJSON selects the fields required for each recognized Responses event
// kind. Text, content, reasoning, item, and tool events include their relevant
// zero indexes and empty strings or arrays instead of relying on the struct's
// omitempty tags. Every selected map includes the sequence number, including
// zero.
//
// The item and part helpers construct known wire shapes, so their supported
// fields determine what survives serialization. Lifecycle and unlisted events
// use an alias of the ordinary struct and its field tags. Returned encoding
// errors propagate to the caller, which owns stream ordering and writing; this
// codec supplies JSON bytes for one event.
func (e ResponsesStreamEvent) MarshalJSON() ([]byte, error) {
	// Each mapped case selects its own fields, so retained Response or other
	// unrelated state cannot be emitted as part of a delta for another item.
	switch e.Type {
	case "response.output_text.delta", "response.output_text.done":
		m := e.wireBase()
		e.putItemID(m)
		m["output_index"] = e.OutputIndex
		m["content_index"] = e.ContentIndex
		if e.Type == "response.output_text.done" {
			m["text"] = e.Text
		} else {
			m["delta"] = e.Delta
		}
		return json.Marshal(m)

	case "response.content_part.added", "response.content_part.done":
		m := e.wireBase()
		e.putItemID(m)
		m["output_index"] = e.OutputIndex
		m["content_index"] = e.ContentIndex
		m["part"] = outputTextPartWire(e.Part)
		return json.Marshal(m)

	case "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		m := e.wireBase()
		e.putItemID(m)
		m["output_index"] = e.OutputIndex
		m["summary_index"] = e.SummaryIndex
		if e.Type == "response.reasoning_summary_text.done" {
			m["text"] = e.Text
		} else {
			m["delta"] = e.Delta
		}
		return json.Marshal(m)

	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		m := e.wireBase()
		e.putItemID(m)
		m["output_index"] = e.OutputIndex
		m["summary_index"] = e.SummaryIndex
		m["part"] = summaryTextPartWire(e.Part)
		return json.Marshal(m)

	case "response.output_item.added", "response.output_item.done":
		m := e.wireBase()
		m["output_index"] = e.OutputIndex
		m["item"] = responsesItemWire(e.Item)
		return json.Marshal(m)

	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		m := e.wireBase()
		e.putItemID(m)
		m["output_index"] = e.OutputIndex
		if e.CallID != "" {
			m["call_id"] = e.CallID
		}
		if e.Name != "" {
			m["name"] = e.Name
		}
		if e.Type == "response.function_call_arguments.done" {
			m["arguments"] = e.Arguments
		} else {
			m["delta"] = e.Delta
		}
		return json.Marshal(m)

	case "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		m := e.wireBase()
		e.putItemID(m)
		m["output_index"] = e.OutputIndex
		if e.CallID != "" {
			m["call_id"] = e.CallID
		}
		if e.Name != "" {
			m["name"] = e.Name
		}
		if e.Type == "response.custom_tool_call_input.done" {
			m["input"] = e.Input
		} else {
			m["delta"] = e.Delta
		}
		return json.Marshal(m)

	default:
		// alias exposes lifecycle and unlisted event fields with ordinary tags,
		// bypassing this event method so default serialization cannot call itself recursively.
		type alias ResponsesStreamEvent
		return json.Marshal(alias(e))
	}
}

// wireBase builds an event map with its type and unconditional sequence
// number.
func (e ResponsesStreamEvent) wireBase() map[string]any {
	m := map[string]any{
		"type":            e.Type,
		"sequence_number": e.SequenceNumber,
	}
	return m
}

// putItemID adds a nonempty item identity to the supplied event map. Empty
// identity leaves that map unchanged; callers pass a writable map when the
// event has an item ID.
func (e ResponsesStreamEvent) putItemID(m map[string]any) {
	if e.ItemID != "" {
		m["item_id"] = e.ItemID
	}
}

// outputTextPartWire builds the output-text shape with text and empty
// annotations/logprobs arrays. Nil parts use empty text, and a supplied part
// contributes its Text only; the emitted type remains output_text.
func outputTextPartWire(part *ResponsesContentPart) map[string]any {
	text := ""
	if part != nil {
		text = part.Text
	}
	return map[string]any{
		"type":        "output_text",
		"text":        text,
		"annotations": []any{},
		"logprobs":    []any{},
	}
}

// summaryTextPartWire builds a summary-text shape using the supplied part's
// Text. Nil parts select empty text, and the returned object uses summary_text
// regardless of the source part's type or other fields.
func summaryTextPartWire(part *ResponsesContentPart) map[string]any {
	text := ""
	if part != nil {
		text = part.Text
	}
	return map[string]any{
		"type": "summary_text",
		"text": text,
	}
}

// responsesItemWire builds the known output-item shape for a lifecycle event.
// Nil items produce an empty object; other items include type and ID even when
// empty, with status added when supplied. Message items default a blank role
// and retain their known content type/text fields.
//
// Reasoning uses an array summary, and function/custom calls include the
// identity and input fields their client dispatch requires. Namespace is
// retained on function calls; discovery calls include client execution and
// argument-value conversion. Fields outside the selected item shape are
// dropped, so callers needing arbitrary vendor items retain their raw JSON
// before this typed encoding.
func responsesItemWire(item *ResponsesOutput) map[string]any {
	if item == nil {
		return map[string]any{}
	}
	m := map[string]any{
		"type": item.Type,
		"id":   item.ID,
	}
	if item.Status != "" {
		m["status"] = item.Status
	}
	// Fill fields by the client-visible item kind, because freeform input and
	// discovery argument values cannot use ordinary function-call string fields.
	switch item.Type {
	case "message":
		role := item.Role
		if role == "" {
			role = "assistant"
		}
		m["role"] = role
		m["content"] = messageContentWire(item.Content)
	case "reasoning":
		m["summary"] = reasoningSummaryWire(item.Summary)
		if item.EncryptedContent != "" {
			m["encrypted_content"] = item.EncryptedContent
		}
	case "function_call":
		m["call_id"] = item.CallID
		m["name"] = item.Name
		m["arguments"] = item.Arguments
		// Restored namespace calls need both identity components. Retain namespace
		// beside name so the client can dispatch the recorded child tool.
		if item.Namespace != "" {
			m["namespace"] = item.Namespace
		}
	case "custom_tool_call":
		// Custom calls carry freeform input instead of serialized function arguments.
		// Keep name and call_id so the client can dispatch and correlate its result.
		m["call_id"] = item.CallID
		m["name"] = item.Name
		m["input"] = item.Input
	case "tool_search_call":
		// Discovery is dispatched on the client and accepts argument JSON values.
		// Convert the bridge's accumulated argument text before emitting this call.
		m["call_id"] = item.CallID
		m["execution"] = "client"
		m["arguments"] = ToolSearchCallArgumentsJSON(item.Arguments)
	}
	return m
}

// messageContentWire builds a nonnil array of content type/text maps,
// including an empty array for no parts. Blank types become output_text;
// image, file, and other source fields are outside these emitted maps.
func messageContentWire(parts []ResponsesContentPart) []map[string]any {
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		typ := p.Type
		if typ == "" {
			typ = "output_text"
		}
		out = append(out, map[string]any{"type": typ, "text": p.Text})
	}
	return out
}

// reasoningSummaryWire builds a nonnil array of summary type/text maps,
// including an empty array for no blocks. Blank block types become
// summary_text, while supplied nonempty type labels remain in the returned
// maps.
func reasoningSummaryWire(summary []ResponsesSummary) []map[string]any {
	out := make([]map[string]any, 0, len(summary))
	for _, s := range summary {
		typ := s.Type
		if typ == "" {
			typ = "summary_text"
		}
		out = append(out, map[string]any{"type": typ, "text": s.Text})
	}
	return out
}

// ToolSearchCallArgumentsJSON encodes discovery arguments as a JSON value.
// Blank text becomes an empty object, any valid trimmed JSON value stays raw,
// and invalid JSON becomes a quoted string of the supplied text.
func ToolSearchCallArgumentsJSON(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	// A string encoder has no unsupported-value or cycle error path. Quoting
	// this text yields valid JSON even when the attempted raw argument value is invalid.
	fallback, _ := json.Marshal(arguments)
	return fallback
}
