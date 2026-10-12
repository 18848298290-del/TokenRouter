package bridge

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// A Responses terminal event may contain less output than the preceding
// completed-item events. Retaining each raw completed item keeps its reported
// identity, content structure, and extension fields available for rebuilding
// that terminal array. Streaming reconstruction orders those records by output
// index and replaces repeated observations at the same index. Buffered
// collection instead keeps first arrival order and deduplicates by item
// identity.
//
// When completed items are unavailable, delta accumulation supplies known
// text, reasoning, and function calls, followed by separately observed image
// items. Choosing completed items first avoids combining those two
// descriptions of the same output. Added compaction items supplement buffered
// done events only when no completed compaction item was collected.

// ResponsesStreamOutputItems retains copied completed-item JSON for one
// stream's terminal reconstruction. Construct it with
// NewResponsesStreamOutputItems before observing events, and serialize
// observation and reads because the map has no synchronization.
type ResponsesStreamOutputItems struct {
	// items owns copied object JSON keyed by output index; later events at the
	// same index replace earlier items before BuildOutput sorts the keys.
	items map[int]json.RawMessage
}

// NewResponsesStreamOutputItems allocates an empty completed-item recorder for
// one stream. Its initialized map accepts observations; the zero value
// supports empty reads but cannot store a valid completed item.
func NewResponsesStreamOutputItems() *ResponsesStreamOutputItems {
	return &ResponsesStreamOutputItems{items: make(map[int]json.RawMessage)}
}

// Observe stores an object item from a valid response.output_item.done
// document. Nil receivers, empty or invalid documents, other event types, and
// nonobject items have no effect. The output index comes from gjson.Int, so an
// absent or unparseable value selects zero.
//
// Each accepted item is copied before publication so later changes to the
// event buffer cannot alter reconstruction. Reusing an index replaces its
// earlier item; raw unknown fields remain available when BuildOutput
// serializes the stored JSON.
func (r *ResponsesStreamOutputItems) Observe(data []byte) {
	if r == nil || len(data) == 0 || !gjson.ValidBytes(data) {
		return
	}
	if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != "response.output_item.done" {
		return
	}
	item := gjson.GetBytes(data, "item")
	if !item.Exists() || !item.IsObject() {
		return
	}
	index := int(gjson.GetBytes(data, "output_index").Int())
	r.items[index] = json.RawMessage(append([]byte(nil), item.Raw...))
}

// HasItems reports whether this recorder contains any completed output
// indexes.
func (r *ResponsesStreamOutputItems) HasItems() bool {
	return r != nil && len(r.items) > 0
}

// Count returns the number of distinct stored output indexes, including index
// zero. Nil receivers return zero, allowing terminal reconstruction to compare
// an output array with an absent completed-item recorder.
func (r *ResponsesStreamOutputItems) Count() int {
	if r == nil {
		return 0
	}
	return len(r.items)
}

// BuildOutput encodes stored raw items in ascending output-index order. A nil
// or empty recorder returns nil and false, and an encoding failure has the
// same result. Returned bytes are newly encoded storage that the caller can
// modify.
//
// Sorting the keys restores stream positions when completion events arrive out
// of order. The retained raw objects keep fields the typed delta accumulator
// cannot express, while gaps between indexes produce no placeholder items in
// the resulting array.
func (r *ResponsesStreamOutputItems) BuildOutput() ([]byte, bool) {
	if !r.HasItems() {
		return nil, false
	}
	indexes := make([]int, 0, len(r.items))
	for index := range r.items {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	ordered := make([]json.RawMessage, 0, len(indexes))
	for _, index := range indexes {
		ordered = append(ordered, r.items[index])
	}
	encoded, err := json.Marshal(ordered)
	// Encoding failure leaves the caller free to use its other reconstruction
	// source; no partly encoded array is exposed as successful output.
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// NormalizeResponsesStreamingTerminalOutput rebuilds output for completed,
// done, incomplete, or cancelled terminal events. A nonempty terminal array
// stays unchanged when its count is at least the number of recorded done
// indexes; an empty array stays unchanged when every reconstruction source is
// empty. This count check does not compare item identities or contents.
//
// Reconstruction prefers sorted done items, then known accumulated output and
// image items, and otherwise installs an empty array. Other event types and
// patch failures return the supplied bytes with false. Callers establish valid
// event JSON and maintain all observations for the same stream before invoking
// this repair.
func NormalizeResponsesStreamingTerminalOutput(data []byte, acc *BufferedResponseAccumulator, doneItems *ResponsesStreamOutputItems, imageOutputs []json.RawMessage) ([]byte, bool) {
	eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
	switch eventType {
	case "response.completed", "response.done", "response.incomplete", "response.cancelled", "response.canceled":
	default:
		return data, false
	}

	output := gjson.GetBytes(data, "response.output")
	hasAccumulatedOutput := (acc != nil && acc.HasContent()) || len(imageOutputs) > 0 || doneItems.HasItems()
	if output.Exists() && output.IsArray() {
		terminalCount := len(output.Array())
		// Use the done-index count as evidence of missing items. An equal or larger
		// terminal count keeps the upstream array even when its identities differ.
		if terminalCount > 0 && terminalCount >= doneItems.Count() {
			return data, false
		}
		if terminalCount == 0 && !hasAccumulatedOutput {
			return data, false
		}
	}

	outputJSON := []byte("[]")
	// Done events can already contain image items and fields absent from the
	// accumulator. Select their entire array before considering separate image
	// observations, which may describe the same images.
	if reconstructed, ok := doneItems.BuildOutput(); ok {
		outputJSON = reconstructed
	} else if reconstructed, ok := BuildResponsesOutputJSON(acc, imageOutputs); ok {
		outputJSON = reconstructed
	}
	updated, err := sjson.SetRawBytes(data, "response.output", outputJSON)
	// Failed patching retains the terminal event for the upstream reader; this
	// helper reports no repair rather than replacing it with incomplete JSON.
	if err != nil {
		return data, false
	}
	return updated, true
}

// CollectRawResponsesOutputItemsFromSSE collects object items from
// output_item.done payloads in arrival order. The first trimmed nonempty item
// ID wins; an item without such an ID uses its complete raw JSON as the
// deduplication key. Completed image status is normalized before collection.
//
// When no done compaction item survives collection, a second pass appends
// compaction objects found in added events. Those additions follow the done
// items and share their deduplication set. Unknown fields remain in each raw
// item. Empty collection or encoding failure returns nil and false, so callers
// can try delta reconstruction.
func CollectRawResponsesOutputItemsFromSSE(bodyText string) ([]byte, bool) {
	var items []json.RawMessage
	seen := make(map[string]struct{})
	hasCompactionItem := false
	appendItem := func(item gjson.Result) {
		if !item.Exists() || !item.IsObject() {
			return
		}
		key := strings.TrimSpace(item.Get("id").String())
		if key == "" {
			key = item.Raw
		}
		// Keep the first completed representation of an identity so repeated
		// done payloads cannot add duplicate output entries during buffered reads.
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		if protocolopenai.IsResponsesCompactionItemType(item.Get("type").String()) {
			hasCompactionItem = true
		}
		items = append(items, json.RawMessage(item.Raw))
	}
	protocolopenai.ForEachSSEDataPayload(bodyText, func(data []byte) {
		// A done image with a result may still carry an in-progress status.
		// Normalize that known mismatch before retaining the item for terminal output.
		if normalized, changed := protocolopenai.NormalizeCompletedImageGenerationStatus(data); changed {
			data = normalized
		}
		if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != "response.output_item.done" {
			return
		}
		appendItem(gjson.GetBytes(data, "item"))
	})
	// Added compaction can be the only reported representation of that result.
	// Search this second event family only when completed collection contains
	// no compaction, then apply the same first-identity deduplication.
	if !hasCompactionItem {
		protocolopenai.ForEachSSEDataPayload(bodyText, func(data []byte) {
			if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != "response.output_item.added" {
				return
			}
			item := gjson.GetBytes(data, "item")
			if !protocolopenai.IsResponsesCompactionItemType(item.Get("type").String()) {
				return
			}
			appendItem(item)
		})
	}
	if len(items) == 0 {
		return nil, false
	}
	// A malformed collected raw item can fail encoding. Return no raw result
	// so the reconstruction caller can continue with typed delta observations.
	outputJSON, err := json.Marshal(items)
	if err != nil {
		return nil, false
	}
	return outputJSON, true
}

// ReconstructResponseOutputFromSSE builds an output array from a buffered SSE
// body. It first returns raw completed items and any selected compaction
// additions, because typed accumulation cannot reproduce their unknown fields
// or arbitrary item identities. No separate image list is appended to that
// result.
//
// When raw collection produces no output, recognized events feed a fresh
// accumulator and image outputs use a separate deduplication set. Invalid
// typed events are skipped while later payloads remain readable.
// BuildResponsesOutputJSON combines those known observations; an empty result
// or failed encoding returns nil and false.
func ReconstructResponseOutputFromSSE(bodyText string) ([]byte, bool) {
	// Prefer complete item records because delta conversion models a smaller
	// set of fields and can otherwise lose compaction or vendor output types.
	if outputJSON, ok := CollectRawResponsesOutputItemsFromSSE(bodyText); ok {
		return outputJSON, true
	}
	acc := NewBufferedResponseAccumulator()
	imageOutputs := make([]json.RawMessage, 0, 1)
	seenImages := make(map[string]struct{})
	protocolopenai.ForEachSSEDataPayload(bodyText, func(data []byte) {
		if imageOutput, ok := protocolopenai.ExtractImageGenerationOutputFromSSEData(data, seenImages); ok {
			imageOutputs = append(imageOutputs, imageOutput)
		}
		eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
		if protocolopenai.ResponsesStreamEventMayContributeToOutput(eventType) {
			var event protocolopenai.ResponsesStreamEvent
			// A bad typed payload contributes no delta, while the SSE iterator
			// can still deliver later independent observations from this body.
			if err := json.Unmarshal(data, &event); err == nil {
				acc.ProcessEvent(&event)
			}
		}
	})
	return BuildResponsesOutputJSON(acc, imageOutputs)
}

// BuildResponsesOutputJSON encodes known accumulated output followed by
// supplied image items. A nil or content-free accumulator contributes nothing,
// and wholly empty inputs return nil and false. Encoding or decoding failures
// also return nil and false.
//
// The accumulator first builds its modeled reasoning, message, and function
// items. Decoding their encoded array into RawMessage entries lets image JSON
// join the same output array without decoding those image fields into a
// reduced typed shape. Callers choose image deduplication and keep
// observations consistent before this operation.
func BuildResponsesOutputJSON(acc *BufferedResponseAccumulator, imageOutputs []json.RawMessage) ([]byte, bool) {
	if (acc == nil || !acc.HasContent()) && len(imageOutputs) == 0 {
		return nil, false
	}

	var output []json.RawMessage
	if acc != nil && acc.HasContent() {
		// Convert modeled items to raw array entries before joining image JSON.
		// Either conversion failure rejects the combined result for the caller.
		outputJSON, err := json.Marshal(acc.BuildOutput())
		if err != nil {
			return nil, false
		}
		if err := json.Unmarshal(outputJSON, &output); err != nil {
			return nil, false
		}
	}
	output = append(output, imageOutputs...)

	// Supplied image entries remain raw and can be invalid. Reject the whole
	// array on an encoding error rather than exposing partial image output.
	outputJSON, err := json.Marshal(output)
	if err != nil {
		return nil, false
	}
	return outputJSON, true
}
