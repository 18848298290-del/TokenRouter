package openai

import (
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// compaction_trigger.go detects Responses compaction requests and moves or
// collapses their triggers behind conversation input.
// Remote compaction detection uses a compaction_trigger in the Responses input
// array, alongside the caller's path and streaming policy. Request preparation
// can place one trigger after all conversation items while retaining the order
// and decoded values of the other history entries.
//
// Reordering or collapsing triggers emits a fresh minimal trigger object;
// attributes on removed trigger objects are therefore discarded. A single
// already-final trigger keeps its supplied bytes and attributes. Number-preserving
// object decoding protects unrelated valid numeric literals during full request
// serialization, while the detection helper uses gjson paths and leaves complete
// JSON validation to the caller. Failed decoding or serialization returns the
// supplied body together with the error rather than a partially normalized request.

// NormalizeCompactionTriggerInputOrder emits one minimal trigger after ordered
// nontrigger input items. Empty bodies, nonarray or empty input, absent triggers,
// and a single already-final trigger return the supplied bytes and false.
//
// Moved or duplicate triggers lose their extra attributes when replaced with
// the minimal type-only object; other decoded input entries keep their order
// and values. DecodeUseNumber retains valid numeric literals through serialization.
// Decoding or encoding errors return the input and false with the error, and
// successful normalization returns fresh encoded bytes and true.
func NormalizeCompactionTriggerInputOrder(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	var payload map[string]any
	if err := wirejson.DecodeUseNumber(body, &payload); err != nil {
		return body, false, err
	}
	input, ok := payload["input"].([]any)
	if !ok || len(input) == 0 {
		return body, false, nil
	}
	triggerCount := 0
	normalized := make([]any, 0, len(input))
	for _, raw := range input {
		// Collect history independently of every trigger position. One minimal
		// trigger is appended afterward, so repeated triggers cannot interleave
		// the forwarded conversation entries.
		item, itemOK := raw.(map[string]any)
		if itemOK && item["type"] == "compaction_trigger" {
			triggerCount++
			continue
		}
		normalized = append(normalized, raw)
	}
	if triggerCount == 0 {
		return body, false, nil
	}
	if triggerCount == 1 {
		if last, ok := input[len(input)-1].(map[string]any); ok && last["type"] == "compaction_trigger" {
			return body, false, nil
		}
	}
	// A moved or repeated trigger is replaced by its type-only wire object.
	// Extra trigger attributes are discarded while ordinary history retains
	// the decoded values and order.
	normalized = append(normalized, map[string]any{"type": "compaction_trigger"})
	payload["input"] = normalized
	encoded, err := wirejson.Marshal(payload)
	if err != nil {
		return body, false, err
	}
	return encoded, true, nil
}

// HasCompactionTriggerInInput detects a trigger type in an input array.
// Empty bodies and other input shapes return false. Handlers combine this signal
// with path and stream policy to select remote compaction; this gjson path
// inspection does not validate the complete request document.
func HasCompactionTriggerInInput(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "compaction_trigger" {
			found = true
			return false
		}
		return true
	})
	return found
}
