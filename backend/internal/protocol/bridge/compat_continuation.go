package bridge

import (
	"encoding/json"
	"strings"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// compat_continuation.go trims compatibility input to a recent turn and its
// required earlier tool calls.
// Compatibility continuation can send the latest input suffix while retaining
// calls needed by its tool outputs. The last item selects the initial suffix:
// a user message includes immediately preceding outputs, and a final output
// includes its contiguous output group before matching earlier call IDs.
//
// Expansion walks backward for those trimmed IDs and keeps intervening items
// when a required call moves the suffix start. Typed decoding and serialization
// mean trimming retains only ResponsesInputItem fields: unknown item extensions
// can be dropped, and nonstring output is serialized through its decoded string
// field. Callers use this helper when that typed suffix is suitable for their
// continuation path, retaining raw history elsewhere when exact replay matters.

// TrimCompatResponsesInputToLatestTurn replaces a request's input with its
// selected continuation suffix after successful typed decoding and encoding.
// Nil requests, absent input, failed decoding, unchanged length, and failed
// serialization leave Input unchanged.
//
// The suffix can expand before the latest user turn to include matching tool
// calls and intervening history. Matching trims call IDs and selects the
// nearest earlier call for each required ID. Reserialization drops unmodeled
// fields and serializes decoded nonstring outputs as strings; callers choose
// this compatibility path with those representation limits in mind.
func TrimCompatResponsesInputToLatestTurn(req *protocolopenai.ResponsesRequest) {
	if req == nil || len(req.Input) == 0 {
		return
	}

	var items []protocolopenai.ResponsesInputItem
	if err := json.Unmarshal(req.Input, &items); err != nil || len(items) == 0 {
		// An unreadable or empty typed array supplies no safe suffix choice.
		// Keep the request's original Input representation available for the caller's continuation handling.
		return
	}

	start := latestAnthropicCompatResponsesInputTurnStart(items)
	trimmed := append([]protocolopenai.ResponsesInputItem(nil), items[start:]...)
	if len(trimmed) == len(items) {
		return
	}
	// Publish the new suffix only after it encodes successfully. A failed
	// serialization keeps the original history bytes on the caller's request.
	if input, err := json.Marshal(trimmed); err == nil {
		req.Input = input
	}
}

// latestAnthropicCompatResponsesInputTurnStart selects a last-item suffix,
// including adjacent function outputs before a final output or explicit user
// message. Empty input returns zero; other last-item forms select the last item.
//
// Tool-output suffixes then expand backward for their matching calls, retaining
// intervening history when a required call precedes the initial start. The
// explicit message type and user role control that branch; an untyped role
// message does not automatically receive the same expansion rule.
func latestAnthropicCompatResponsesInputTurnStart(items []protocolopenai.ResponsesInputItem) int {
	if len(items) == 0 {
		return 0
	}

	start := len(items) - 1
	last := items[start]
	switch {
	case last.Type == "function_call_output":
		for start > 0 && items[start-1].Type == "function_call_output" {
			start--
		}
	case last.Type == "message" && last.Role == "user":
		for start > 0 && items[start-1].Type == "function_call_output" {
			start--
		}
	default:
		return start
	}

	return expandAnthropicCompatResponsesInputToolCallStart(items, start)
}

// expandAnthropicCompatResponsesInputToolCallStart moves start backward to
// earlier function calls matching output IDs in the retained suffix. Invalid
// start returns unchanged; blank IDs add no matching requirement.
//
// IDs are trimmed on calls and outputs, and backward scanning chooses the
// nearest earlier occurrence of each required ID. Moving the start retains
// every intervening item as part of the suffix. Missing calls leave unmatched
// outputs in place rather than inventing context; callers validate the resulting
// tool history under their own replay policy.
func expandAnthropicCompatResponsesInputToolCallStart(items []protocolopenai.ResponsesInputItem, start int) int {
	if start < 0 || start >= len(items) {
		return start
	}

	needed := make(map[string]struct{})
	for i := start; i < len(items); i++ {
		if items[i].Type != "function_call_output" {
			continue
		}
		callID := strings.TrimSpace(items[i].CallID)
		if callID != "" {
			needed[callID] = struct{}{}
		}
	}
	if len(needed) == 0 {
		return start
	}

	// Moving to a matched earlier call also retains the intervening items,
	// keeping matched calls inside the contiguous continuation suffix while
	// unmatched outputs remain for the caller's replay validation.
	expandedStart := start
	for i := start - 1; i >= 0 && len(needed) > 0; i-- {
		if items[i].Type != "function_call" {
			continue
		}
		callID := strings.TrimSpace(items[i].CallID)
		if _, ok := needed[callID]; !ok {
			continue
		}
		delete(needed, callID)
		expandedStart = i
	}
	return expandedStart
}
