package openai

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SupplementCompactionItemFromSSE appends a missing compaction item to a nonempty compact response output.
// It copies raw JSON from output_item.done, or output_item.added when no done item exists.
// Codex remote compact v2 expects one compaction item from output_item.done;
// SSE-to-JSON conversion needs the same item. Non-compact responses pass through.
func SupplementCompactionItemFromSSE(compact bool, finalResponse []byte, bodyText string) []byte {
	if !compact {
		return finalResponse
	}
	if len(gjson.GetBytes(finalResponse, "output").Array()) == 0 {
		// The response output reconstruction path handles empty output arrays.
		return finalResponse
	}
	if ResponsesOutputHasCompactionItem(finalResponse) {
		return finalResponse
	}
	item, found := FindRawCompactionItemFromSSE(bodyText)
	if !found {
		return finalResponse
	}
	patched, err := sjson.SetRawBytes(finalResponse, "output.-1", item)
	if err != nil {
		return finalResponse
	}
	return patched
}

func OpenAIStreamEventIsTerminal(data string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false
	}
	if trimmed == "[DONE]" {
		return true
	}
	return OpenAIStreamEventTypeIsTerminal(gjson.Get(trimmed, "type").String())
}

// OpenAIStreamEventIsTerminalWithType checks the supplied event type, including the [DONE] sentinel.
func OpenAIStreamEventIsTerminalWithType(data, eventType string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false
	}
	if trimmed == "[DONE]" {
		return true
	}
	return OpenAIStreamEventTypeIsTerminal(eventType)
}

// GenericFailedEventPayload returns a generic response.failed event for clients.
func GenericFailedEventPayload(_ ...[]byte) []byte {
	return []byte(`{"type":"response.failed","response":{"error":{"message":"Upstream gateway error"}}}`)
}
