package bridge

import (
	"encoding/json"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// Full compatibility replay keeps a recent message tail while retaining the
// recognized tool relationships that cross its starting point. A fixed count
// alone can leave an output separated from its call, or a retained call
// separated from an earlier matching result in supplied history.
//
// Boundary expansion indexes the first occurrence of each literal nonempty ID,
// then repeatedly moves backward for matching calls/results until the selected
// suffix reaches a fixed point. Undecodable content contributes no IDs, and
// missing partners are not synthesized. Expansion can retain more than the
// nominal tail or prevent trimming entirely. A changed request receives a new
// message slice whose raw content bytes remain shared with the retained source
// messages; callers copy those bytes before independent mutation.

// openAICompatAnthropicReplayMaxTailMessages bounds the initial recent-message
// selection before recognized tool relationships expand its starting boundary.
const openAICompatAnthropicReplayMaxTailMessages = 12

// ApplyAnthropicCompatFullReplayGuard trims a request to a recent suffix after
// expanding its boundary for recognized tool IDs. Nil requests, short histories,
// and expansion to the first message return false with the message slice unchanged.
//
// The retained suffix can exceed twelve messages or include intervening history
// needed by an earlier matching call/result. Only IDs decoded from supported
// block arrays participate; malformed content and missing partners cannot
// establish a complete-pair guarantee. On true, Messages is replaced with a
// copied slice, while retained Content byte slices remain shared. Callers
// serialize request mutation and copy content before independent byte edits.
func ApplyAnthropicCompatFullReplayGuard(req *protocolanthropic.AnthropicRequest) bool {
	if req == nil || len(req.Messages) <= openAICompatAnthropicReplayMaxTailMessages {
		return false
	}

	// The nominal tail is only the initial candidate. Expand it before
	// copying so recognized cross-boundary partners remain within the replayed suffix.
	start := len(req.Messages) - openAICompatAnthropicReplayMaxTailMessages
	start = expandAnthropicCompatTrimBoundary(req.Messages, start)
	if start <= 0 {
		return false
	}

	req.Messages = append([]protocolanthropic.AnthropicMessage(nil), req.Messages[start:]...)
	return true
}

// expandAnthropicCompatTrimBoundary moves start backward for indexed partners
// of IDs inside the retained suffix. Invalid or first-message starts return
// unchanged. IDs match literally, without trimming, and the earliest occurrence
// of each use/result supplies the candidate boundary.
//
// Each expansion can expose another retained relationship, so scanning repeats
// until the boundary stops moving. The loop includes intervening messages as
// part of the suffix and can reach zero. Undecodable content supplies no IDs,
// leaving its dependencies outside what this helper can recognize.
func expandAnthropicCompatTrimBoundary(messages []protocolanthropic.AnthropicMessage, start int) int {
	if start <= 0 || start >= len(messages) {
		return start
	}

	toolUseIndex := make(map[string]int)
	toolResultIndex := make(map[string]int)
	for i, msg := range messages {
		uses, results := anthropicCompatMessageToolIDs(msg)
		for _, id := range uses {
			if _, exists := toolUseIndex[id]; !exists {
				toolUseIndex[id] = i
			}
		}
		for _, id := range results {
			if _, exists := toolResultIndex[id]; !exists {
				toolResultIndex[id] = i
			}
		}
	}

	// Moving backward can expose another relationship in the enlarged suffix.
	// Repeat until no recognized partner requires an earlier boundary, retaining
	// all intervening messages each time.
	for {
		next := start
		for i := start; i < len(messages); i++ {
			uses, results := anthropicCompatMessageToolIDs(messages[i])
			for _, id := range results {
				if useIdx, ok := toolUseIndex[id]; ok && useIdx < next {
					next = useIdx
				}
			}
			for _, id := range uses {
				if resultIdx, ok := toolResultIndex[id]; ok && resultIdx < next {
					next = resultIdx
				}
			}
		}
		if next == start {
			return start
		}
		start = next
	}
}

// anthropicCompatMessageToolIDs returns nonempty tool_use IDs and tool_result
// references from a typed content-block array. Unsupported or undecodable
// content returns no IDs, including ordinary string messages; IDs retain their
// supplied whitespace and case for the boundary helper's literal matching.
func anthropicCompatMessageToolIDs(msg protocolanthropic.AnthropicMessage) ([]string, []string) {
	var blocks []protocolanthropic.AnthropicContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		// Unreadable content cannot establish a typed tool relationship. Return
		// no IDs while leaving the original message available in the selected history.
		return nil, nil
	}

	uses := make([]string, 0, 1)
	results := make([]string, 0, 1)
	for _, block := range blocks {
		switch block.Type {
		case "tool_use":
			if block.ID != "" {
				uses = append(uses, block.ID)
			}
		case "tool_result":
			if block.ToolUseID != "" {
				results = append(results, block.ToolUseID)
			}
		}
	}
	return uses, results
}
