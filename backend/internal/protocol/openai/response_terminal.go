package openai

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// response_terminal.go supplements missing compaction output from SSE history
// and identifies terminal Responses events.
// A compact Responses stream can finish with an output array that omits the
// compaction item carried by an earlier output-item event. Buffered conversion
// can supplement that item while retaining its raw JSON fields, provided the
// response already has output for this append path.
//
// Empty-output reconstruction is handled separately. The helper prefers a
// completed compaction item over an added item and leaves an existing
// compaction item untouched. Terminal detection is another responsibility:
// both a supplied event type and the DONE sentinel can identify termination,
// including failed or error events. Those classifiers perform path extraction
// without validating complete JSON, allowing their callers to decide how to
// handle trailing or malformed payload content.

// SupplementCompactionItemFromSSE appends a missing raw compaction item to
// nonempty compact response output. Noncompact responses, empty output, existing
// compaction, missing stream items, and failed patch operations retain the
// supplied response.
//
// FindRawCompactionItemFromSSE selects the first output_item.done compaction
// item, then an output_item.added item when no done item exists. Other output
// remains in its existing order. Callers validate response and stream JSON
// separately and use their dedicated reconstruction path for empty output arrays.
func SupplementCompactionItemFromSSE(compact bool, finalResponse []byte, bodyText string) []byte {
	if !compact {
		return finalResponse
	}
	if len(gjson.GetBytes(finalResponse, "output").Array()) == 0 {
		// Empty output needs complete reconstruction from stream items, not an
		// append to this partial final response; leave that case for the owning conversion path.
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
		// Failed appending cannot provide a repaired compact response. Return
		// the supplied body so callers retain its existing output for other recovery handling.
		return finalResponse
	}
	return patched
}

// OpenAIStreamEventIsTerminal recognizes the trimmed DONE sentinel or a
// terminal type extracted from the data payload. Blank input returns false;
// complete JSON validity is left to the caller, so trailing data can still
// expose a terminal type.
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

// OpenAIStreamEventIsTerminalWithType recognizes the DONE sentinel or a terminal
// eventType supplied by the caller. Blank data returns false even with a
// terminal type; other nonblank payloads are classified without parsing or
// validating their JSON.
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

// GenericFailedEventPayload returns a fixed response.failed payload containing
// the generic upstream gateway error message. Its optional byte arguments are
// ignored, letting compatible callers use the helper without exposing those
// supplied details in the event.
func GenericFailedEventPayload(_ ...[]byte) []byte {
	return []byte(`{"type":"response.failed","response":{"error":{"message":"Upstream gateway error"}}}`)
}
