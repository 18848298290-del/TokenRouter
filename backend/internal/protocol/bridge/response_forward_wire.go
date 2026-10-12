package bridge

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// ResponsesStreamOutputItems stores raw output_item.done items by output_index.
// Completed items retain identity, status, phase, order, and unknown types during reconstruction.
// Delta accumulation models one reasoning item, one message, and function calls when done items are absent.
type ResponsesStreamOutputItems struct {
	items map[int]json.RawMessage
}

func NewResponsesStreamOutputItems() *ResponsesStreamOutputItems {
	return &ResponsesStreamOutputItems{items: make(map[int]json.RawMessage)}
}

// Observe copies raw items from response.output_item.done events so reconstruction retains
// vendor extensions and fields unknown to this gateway.
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

func (r *ResponsesStreamOutputItems) HasItems() bool {
	return r != nil && len(r.items) > 0
}

// Count returns the number of distinct completed output indexes.
func (r *ResponsesStreamOutputItems) Count() int {
	if r == nil {
		return 0
	}
	return len(r.items)
}

// BuildOutput returns recorded items ordered by output_index.
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
	if err != nil {
		return nil, false
	}
	return encoded, true
}

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
		// A shorter terminal output has omitted reported items; rebuild it from done events.
		// Keep a nonempty terminal output with at least as many items.
		if terminalCount > 0 && terminalCount >= doneItems.Count() {
			return data, false
		}
		if terminalCount == 0 && !hasAccumulatedOutput {
			return data, false
		}
	}

	outputJSON := []byte("[]")
	// Prefer reported items as ReconstructResponseOutputFromSSE does. Image items also arrive
	// in done events, so appending imageOutputs here would duplicate them.
	if reconstructed, ok := doneItems.BuildOutput(); ok {
		outputJSON = reconstructed
	} else if reconstructed, ok := BuildResponsesOutputJSON(acc, imageOutputs); ok {
		outputJSON = reconstructed
	}
	updated, err := sjson.SetRawBytes(data, "response.output", outputJSON)
	if err != nil {
		return data, false
	}
	return updated, true
}

// CollectRawResponsesOutputItemsFromSSE collects raw done items in arrival order.
// It repairs completed image status while retaining fields such as encrypted_content,
// summary, and opaque (#3777 issue 2). If done events lack compaction, it also reads
// compaction items from added events, where some upstreams send the complete result.
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
		if normalized, changed := protocolopenai.NormalizeCompletedImageGenerationStatus(data); changed {
			data = normalized
		}
		if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != "response.output_item.done" {
			return
		}
		appendItem(gjson.GetBytes(data, "item"))
	})
	// Some streams report compaction only in added events. Read those only when done lacks it
	// to avoid duplicating the single compaction item Codex expects.
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
	outputJSON, err := json.Marshal(items)
	if err != nil {
		return nil, false
	}
	return outputJSON, true
}

// ReconstructResponseOutputFromSSE rebuilds output from raw SSE, preferring completed items.
// Delta accumulation covers text, function calls, and reasoning but loses unknown types
// such as compaction. Missing that item breaks Codex remote compact v2 (#3887).
// It returns (nil, false) when no output can be reconstructed.
func ReconstructResponseOutputFromSSE(bodyText string) ([]byte, bool) {
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
			if err := json.Unmarshal(data, &event); err == nil {
				acc.ProcessEvent(&event)
			}
		}
	})
	return BuildResponsesOutputJSON(acc, imageOutputs)
}

func BuildResponsesOutputJSON(acc *BufferedResponseAccumulator, imageOutputs []json.RawMessage) ([]byte, bool) {
	if (acc == nil || !acc.HasContent()) && len(imageOutputs) == 0 {
		return nil, false
	}

	var output []json.RawMessage
	if acc != nil && acc.HasContent() {
		outputJSON, err := json.Marshal(acc.BuildOutput())
		if err != nil {
			return nil, false
		}
		if err := json.Unmarshal(outputJSON, &output); err != nil {
			return nil, false
		}
	}
	output = append(output, imageOutputs...)

	outputJSON, err := json.Marshal(output)
	if err != nil {
		return nil, false
	}
	return outputJSON, true
}
