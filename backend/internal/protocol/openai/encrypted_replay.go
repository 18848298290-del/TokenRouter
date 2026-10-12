package openai

import "strings"

// Recovery retries remove encrypted_content from reasoning items and discard
// encrypted compaction items as complete objects. Plaintext summaries and other
// remaining reasoning attributes stay available unless cleanup leaves a type-only
// object. Unencrypted compaction objects retain their supplied fields, including
// identifiers, because the encrypted_content key determines whether they are
// discarded.
//
// Sanitization operates on decoded maps rather than raw JSON. Input arrays reuse
// their backing storage as retained entries move toward the front, and retained
// reasoning maps are edited in place. Callers isolate any history they need to
// keep before invoking cleanup; aliases can observe these mutations even when
// they retain the array's earlier length.

// TrimEncryptedReasoningItems sanitizes the request's input for an encrypted-state
// recovery attempt and reports whether any supported item changed. Input may be
// []any, []map[string]any, or one map[string]any; other representations pass through
// with a false result. Retained items keep their order, and an emptied input is
// removed from reqBody after a change.
//
// Callers provide exclusively owned mutable request data or copy shared history
// first. Filtering reuses array storage, and the sanitizer edits reasoning maps
// before the top-level input is replaced. A false result means no supported
// cleanup was reported, rather than validating the request for an upstream.
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
		// Compact retained entries into the same storage. Other slice aliases can
		// still expose dropped tail entries and overwritten positions after cleanup.
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
		// Keep the typed map-array form while compacting its existing storage;
		// retained maps share the sanitizer's in-place field deletions with aliases.
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
			// A replacement of another kind cannot enter a typed map array. Keep
			// this entry while earlier item mutations remain in the shared storage.
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
		// This branch installs map replacements. An unexpected replacement kind
		// leaves input assigned as before, after any mutation performed by sanitation.
		if !ok {
			return false
		}
		reqBody["input"] = nextMap
		return true
	default:
		return false
	}
}

// SanitizeEncryptedReasoningInputItem removes replay state from one decoded item
// and returns its replacement, change flag, and retention decision. Only
// map[string]any values with string type labels participate; surrounding type
// whitespace is ignored. Any encrypted_content key drops compaction or
// compaction_summary, including a key whose value is nil.
//
// Reasoning maps lose encrypted_content and literal nil content while other fields
// remain shared with the supplied map. A changed map containing only its type is
// dropped. Callers honor keep before installing next and isolate shared maps
// before calling; a dropped reasoning item may already have been mutated.
func SanitizeEncryptedReasoningInputItem(item any) (next any, changed bool, keep bool) {
	inputItem, ok := item.(map[string]any)
	if !ok {
		return item, false, true
	}

	itemType, _ := inputItem["type"].(string)
	switch strings.TrimSpace(itemType) {
	case "compaction", "compaction_summary":
		// Compaction recovery discards the whole encrypted item, including its
		// identifier; key presence governs this path even for an empty or nil value.
		if _, encrypted := inputItem["encrypted_content"]; encrypted {
			return nil, true, false
		}
		return item, false, true
	case "reasoning":
	default:
		return item, false, true
	}

	// Reasoning can retain its plaintext summary after ciphertext removal, so
	// delete this field in place before deciding whether useful attributes remain.
	if _, has := inputItem["encrypted_content"]; has {
		delete(inputItem, "encrypted_content")
		changed = true
	}

	// Literal nil content is removed independently of ciphertext. Recovery can
	// therefore retain a summary-only reasoning object even when no encrypted field
	// was supplied; an interface holding a typed nil value does not match this check.
	if v, has := inputItem["content"]; has && v == nil {
		delete(inputItem, "content")
		changed = true
	}

	if !changed {
		return item, false, true
	}
	// Field removal can leave only the type discriminator. Drop that skeleton
	// from replay; surviving summary, ID, or extension fields keep the object present.
	if len(inputItem) == 1 {
		return nil, true, false
	}
	return inputItem, true, true
}
