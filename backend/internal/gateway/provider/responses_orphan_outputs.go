package provider

import (
	"strings"

	openaicore "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Tool outputs can refer to calls outside the supplied input when
// previous_response_id names retained upstream history. Without that
// continuation identity, the request needs a matching local call or item
// reference so the output is not submitted alone.
//
// A two-pass scan gathers every supported call and item_reference before
// deciding which outputs survive, allowing an output to precede its matching
// context in the array. Empty call IDs and absent matches are removed, while
// non-output items remain in order. The replacement allocates its slice but
// retains the nested item values; callers keep those shared maps stable while
// later request transforms read them.

// SanitizeOpenAIResponsesOrphanToolOutputs removes unmatched tool outputs when
// no previous response can supply their calls. Empty input or
// hasPreviousResponseID returns false; reqBody must be writable when removal
// is needed.
//
// The first pass records call_id, or call id when absent, plus item_reference
// IDs across all input. The second retains output only when its nonblank
// call_id matches one of those sets, regardless of item ordering. Other items
// retain their references and order. A changed array replaces reqBody input,
// while nested retained item values remain shared.
func SanitizeOpenAIResponsesOrphanToolOutputs(reqBody map[string]any, input []any, hasPreviousResponseID bool) bool {
	if len(input) == 0 || hasPreviousResponseID {
		return false
	}

	// Gather the whole input before filtering so outputs can match call context
	// that appears later, with item references providing another supported identity source.
	toolCallIDs := make(map[string]struct{}, len(input))
	referenceIDs := make(map[string]struct{}, len(input))
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType := strings.TrimSpace(openai.FirstNonEmptyString(item["type"]))
		if itemType == "item_reference" {
			if id := strings.TrimSpace(openai.FirstNonEmptyString(item["id"])); id != "" {
				referenceIDs[id] = struct{}{}
			}
			continue
		}
		if !openaicore.IsCodexToolCallContextItemType(itemType) {
			continue
		}
		if id := strings.TrimSpace(openai.FirstNonEmptyString(item["call_id"], item["id"])); id != "" {
			toolCallIDs[id] = struct{}{}
		}
	}

	// Filter outputs against the gathered contexts; retained item values stay shared
	// and keep encounter order, while only the replacement slice is allocated here.
	modified := false
	normalized := make([]any, 0, len(input))
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || !openaicore.IsCodexToolCallOutputItemType(strings.TrimSpace(openai.FirstNonEmptyString(item["type"]))) {
			normalized = append(normalized, rawItem)
			continue
		}

		callID := strings.TrimSpace(openai.FirstNonEmptyString(item["call_id"]))
		_, hasToolCall := toolCallIDs[callID]
		_, hasReference := referenceIDs[callID]
		if callID != "" && (hasToolCall || hasReference) {
			normalized = append(normalized, rawItem)
			continue
		}

		modified = true
	}
	if !modified {
		return false
	}
	reqBody["input"] = normalized
	return true
}
