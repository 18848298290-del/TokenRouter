package openai

import (
	"encoding/json"
	"strings"
)

// MarshalJSON encodes ResponsesStreamEvent with the fields required on the wire.
//
// Responses stream fields can be required even when their values are zero:
// output_index, content_index, and summary_index can all be zero. Function call
// items require call_id, name, and arguments, including an empty arguments string.
// Message items require a content array, and output_text parts require
// text, annotations, and logprobs. Struct omitempty tags would omit these values,
// causing strict Codex clients to reject the item or delta.
//
// Chat and Anthropic conversions share these event-specific field rules.
// Unlisted event types use the default struct encoding.
func (e ResponsesStreamEvent) MarshalJSON() ([]byte, error) {
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
		// Lifecycle events such as response.created and response.completed
		// use the default struct encoding.
		type alias ResponsesStreamEvent
		return json.Marshal(alias(e))
	}
}

func (e ResponsesStreamEvent) wireBase() map[string]any {
	m := map[string]any{
		"type":            e.Type,
		"sequence_number": e.SequenceNumber,
	}
	return m
}

func (e ResponsesStreamEvent) putItemID(m map[string]any) {
	if e.ItemID != "" {
		m["item_id"] = e.ItemID
	}
}

// outputTextPartWire encodes an output_text part with text and empty
// annotations and logprobs arrays.
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

// summaryTextPartWire encodes a reasoning summary part.
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

// responsesItemWire fills required output item fields according to item type,
// including empty arrays and strings that omitempty would omit.
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
		// Codex routes namespace tools by namespace and name; omitting the
		// namespace would make a restored call unsupported.
		if item.Namespace != "" {
			m["namespace"] = item.Namespace
		}
	case "custom_tool_call":
		// Custom tools such as Codex exec take free-form input. Codex needs
		// call_id and name to route the call.
		m["call_id"] = item.CallID
		m["name"] = item.Name
		m["input"] = item.Input
	case "tool_search_call":
		// Codex executes restored tool_search calls when execution is "client".
		// Their arguments are encoded as JSON values.
		m["call_id"] = item.CallID
		m["execution"] = "client"
		m["arguments"] = ToolSearchCallArgumentsJSON(item.Arguments)
	}
	return m
}

// messageContentWire encodes message content and returns an empty array for empty input.
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

// reasoningSummaryWire encodes reasoning summaries and returns an array even for empty input.
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

// ToolSearchCallArgumentsJSON restores accumulated function arguments as raw JSON for tool_search_call.
// Empty arguments become an object; invalid JSON is encoded as a string so the
// client can report the argument error and request a retry.
func ToolSearchCallArgumentsJSON(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	fallback, _ := json.Marshal(arguments)
	return fallback
}
