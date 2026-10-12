package anthropic

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// Messages execution needs separate evidence that a response reports usage,
// produces content, and reaches a terminal event. Counter presence includes
// zero-valued observations, while semantic output requires content text or a
// recognizable tool call; response model identity is read before downstream
// format conversion can substitute the client's requested model.

// Observation holds independent usage, output, completion, and response-model evidence.
type Observation struct {
	// HasUsage, Semantic, and Terminal describe different aspects of one payload.
	// A usage-only delta can set HasUsage without Semantic or Terminal, and a
	// message_stop event can set Terminal without reporting either other flag.
	//
	// HasUsage records a recognized nonnull usage field, including an explicit
	// zero count. It establishes presence independently of positive billing totals.
	//
	// Semantic records recognized nonempty content. Reasoning, signature, and
	// argument deltas or tool identifiers can establish output before text arrives.
	//
	// Terminal records completion evidence, including upstream errors. Consumers
	// combine it with delivery and error handling before treating a request as successful.
	HasUsage, Semantic, Terminal bool /* HasUsage: nonnull counter presence;
	Semantic: recognized content presence; Terminal: observed completion signal */
	Model string // normalized upstream model
}

// ObserveMessage reads usage, semantic content, and model identity from a
// buffered Messages response. It marks the response terminal even when fields
// are absent; callers establish HTTP delivery and JSON validity separately.
func ObserveMessage(data string) Observation {
	value := gjson.Parse(data)
	observed := Observation{HasUsage: hasUsageFields(value.Get("usage")), Terminal: true, Model: protocol.NormalizeResponseModel(protocol.ResponseModelString([]byte(data), "model"))}
	value.Get("content").ForEach(func(_, block gjson.Result) bool {
		observed.Semantic = observed.Semantic || semanticBlock(block)
		return true
	})
	return observed
}

// ObserveEvent reads completion evidence from one Messages SSE data payload.
// It recognizes message and content-block events plus the DONE sentinel.
// Unknown events return empty evidence, and message_start retains its model
// observation only when the enclosing event is valid JSON.
func ObserveEvent(data string) Observation {
	if strings.TrimSpace(data) == "[DONE]" {
		return Observation{Terminal: true}
	}
	value := gjson.Parse(data)
	observed := Observation{}
	switch value.Get("type").String() {
	case "message_start":
		// The embedded message has the buffered response shape, but this event
		// opens the stream; its presence cannot establish that generation ended.
		observed = ObserveMessage(value.Get("message").Raw)
		observed.Terminal = false
		if !gjson.Valid(data) {
			// Partial JSON can expose a model path through gjson. Discard that
			// identity so an invalid frame cannot supply an upstream model record.
			observed.Model = ""
		}
	case "message_delta":
		observed.HasUsage = hasUsageFields(value.Get("usage"))
	case "content_block_start":
		observed.Semantic = semanticBlock(value.Get("content_block"))
	case "content_block_delta":
		delta := value.Get("delta")
		for _, field := range []string{"text", "thinking", "partial_json", "signature"} {
			if delta.Get(field).String() != "" {
				observed.Semantic = true
			}
		}
	case "message_stop", "error":
		observed.Terminal = true
	}
	return observed
}

// hasUsageFields reports whether a recognized usage counter is present and
// nonnull. The counter's value need not be positive, allowing an upstream
// zero-token report to remain distinct from missing usage evidence.
func hasUsageFields(value gjson.Result) bool {
	for _, field := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "cached_tokens", "cache_creation.ephemeral_5m_input_tokens", "cache_creation.ephemeral_1h_input_tokens"} {
		v := value.Get(field)
		if v.Exists() && v.Type != gjson.Null {
			return true
		}
	}
	return false
}

// semanticBlock reports whether a Messages content block contains observable
// text, reasoning, redacted data, or a tool identifier or name. Empty and
// unknown blocks return false, including tool blocks with arguments alone.
func semanticBlock(value gjson.Result) bool {
	switch value.Get("type").String() {
	case "text":
		return value.Get("text").String() != ""
	case "thinking":
		return value.Get("thinking").String() != ""
	case "redacted_thinking":
		return value.Get("data").String() != ""
	case "tool_use", "server_tool_use":
		// Tool starts can carry their identity before argument fragments arrive;
		// count that start as output so an empty initial input object remains usable.
		return value.Get("id").String() != "" || value.Get("name").String() != ""
	}
	return false
}
