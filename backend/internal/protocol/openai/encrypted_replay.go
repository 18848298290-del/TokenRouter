package openai

import "strings"

// TrimEncryptedReasoningItems mutates input to remove provider-bound state for decryption recovery.
// It retains reusable reasoning fields and drops encrypted compaction items.
func TrimEncryptedReasoningItems(reqBody map[string]any) bool {
	if len(reqBody) == 0 {
		return false
	}

	inputValue, has := reqBody["input"]
	if !has {
		return false
	}

	switch input := inputValue.(type) {
	case []any:
		filtered := input[:0]
		changed := false
		for _, item := range input {
			nextItem, itemChanged, keep := SanitizeEncryptedReasoningInputItem(item)
			if itemChanged {
				changed = true
			}
			if !keep {
				continue
			}
			filtered = append(filtered, nextItem)
		}
		if !changed {
			return false
		}
		if len(filtered) == 0 {
			delete(reqBody, "input")
			return true
		}
		reqBody["input"] = filtered
		return true
	case []map[string]any:
		filtered := input[:0]
		changed := false
		for _, item := range input {
			nextItem, itemChanged, keep := SanitizeEncryptedReasoningInputItem(item)
			if itemChanged {
				changed = true
			}
			if !keep {
				continue
			}
			nextMap, ok := nextItem.(map[string]any)
			if !ok {
				filtered = append(filtered, item)
				continue
			}
			filtered = append(filtered, nextMap)
		}
		if !changed {
			return false
		}
		if len(filtered) == 0 {
			delete(reqBody, "input")
			return true
		}
		reqBody["input"] = filtered
		return true
	case map[string]any:
		nextItem, changed, keep := SanitizeEncryptedReasoningInputItem(input)
		if !changed {
			return false
		}
		if !keep {
			delete(reqBody, "input")
			return true
		}
		nextMap, ok := nextItem.(map[string]any)
		if !ok {
			return false
		}
		reqBody["input"] = nextMap
		return true
	default:
		return false
	}
}

func SanitizeEncryptedReasoningInputItem(item any) (next any, changed bool, keep bool) {
	inputItem, ok := item.(map[string]any)
	if !ok {
		return item, false, true
	}

	itemType, _ := inputItem["type"].(string)
	switch strings.TrimSpace(itemType) {
	case "compaction", "compaction_summary":
		if _, encrypted := inputItem["encrypted_content"]; encrypted {
			return nil, true, false
		}
		return item, false, true
	case "reasoning":
	default:
		return item, false, true
	}

	if _, has := inputItem["encrypted_content"]; has {
		delete(inputItem, "encrypted_content")
		changed = true
	}

	// xAI rejects null content with HTTP 422 during untagged enum deserialization.
	if v, has := inputItem["content"]; has && v == nil {
		delete(inputItem, "content")
		changed = true
	}

	if !changed {
		return item, false, true
	}
	if len(inputItem) == 1 {
		return nil, true, false
	}
	return inputItem, true, true
}
