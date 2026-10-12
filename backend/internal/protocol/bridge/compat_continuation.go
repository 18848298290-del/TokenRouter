package bridge

import (
	"encoding/json"
	"strings"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

func TrimCompatResponsesInputToLatestTurn(req *protocolopenai.ResponsesRequest) {
	if req == nil || len(req.Input) == 0 {
		return
	}

	var items []protocolopenai.ResponsesInputItem
	if err := json.Unmarshal(req.Input, &items); err != nil || len(items) == 0 {
		return
	}

	start := latestAnthropicCompatResponsesInputTurnStart(items)
	trimmed := append([]protocolopenai.ResponsesInputItem(nil), items[start:]...)
	if len(trimmed) == len(items) {
		return
	}
	if input, err := json.Marshal(trimmed); err == nil {
		req.Input = input
	}
}

// latestAnthropicCompatResponsesInputTurnStart finds the latest input suffix,
// expanding it to include calls whose outputs would otherwise lose their context.
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

// expandAnthropicCompatResponsesInputToolCallStart includes earlier calls matching retained outputs.
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
