package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
)

// Invalid encrypted reasoning state is identified by a SHA-256 digest of the
// ciphertext returned in request history. The collector accepts reasoning and
// compaction types handled by the protocol sanitizer, so lineage tracking does
// not repeatedly decode unsupported items that it cannot remove.
//
// Decoded requests compact their input slices in place and can share item maps
// with callers. Replay-byte requests instead decode candidate items into
// separate maps, keep unmatched bodies shared, and encode replacements only
// when sanitization succeeds. Raw payloads probe for a digest match before
// decoding the complete object. Those paths keep valid ciphertext available and
// report how many items changed, while compaction removal and reasoning-shell
// retention follow the shared protocol sanitizer.

// OpenAIEncryptedContentDigest returns the lowercase SHA-256 hex digest of
// ciphertext bytes.
func OpenAIEncryptedContentDigest(encrypted string) string {
	sum := sha256.Sum256([]byte(encrypted))
	return hex.EncodeToString(sum[:])
}

// OpenAIEncryptedLineageItemType reports whether the trimmed type supports
// encrypted-replay sanitization. Reasoning, compaction, and compaction_summary
// qualify; other types are excluded from collection because the shared
// sanitizer cannot remove their ciphertext.
func OpenAIEncryptedLineageItemType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "reasoning", "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

// CollectOpenAIEncryptedContentDigestsRaw collects nonempty string ciphertext
// digests from supported input items. Arrays and a single object are accepted;
// callers keep payload immutable while its zero-copy string view is parsed.
func CollectOpenAIEncryptedContentDigestsRaw(payload []byte) []string {
	if len(payload) == 0 {
		return nil
	}
	input := gjson.Get(OpenAIWSPayloadStringView(payload), "input")
	if !input.Exists() {
		return nil
	}
	var digests []string
	appendItem := func(item gjson.Result) {
		if !OpenAIEncryptedLineageItemType(item.Get("type").String()) {
			return
		}
		encrypted := item.Get("encrypted_content")
		if encrypted.Type == gjson.String && encrypted.String() != "" {
			digests = append(digests, OpenAIEncryptedContentDigest(encrypted.String()))
		}
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			appendItem(item)
			return true
		})
		return digests
	}
	if input.IsObject() {
		appendItem(input)
	}
	return digests
}

// StripOpenAIInvalidEncryptedContentItems sanitizes matching ciphertext in
// reqBody and returns the changed-item count. It mutates the request map,
// compacts an input slice on its existing backing storage, and can mutate
// shared reasoning maps. Callers isolate that mutable state before passing it
// here.
//
// Digest matches select the protocol sanitizer, which drops encrypted
// compaction items and removes ciphertext and literal nil content from
// reasoning. A reasoning item survives when attributes besides its type remain.
// Empty filtered input is deleted from the request; unsupported shapes and
// unmatched digests leave the request unchanged, keeping fresh history
// available for the next attempt.
func StripOpenAIInvalidEncryptedContentItems(reqBody map[string]any, invalid map[string]struct{}) int {
	if len(reqBody) == 0 || len(invalid) == 0 {
		return 0
	}
	inputValue, has := reqBody["input"]
	if !has {
		return 0
	}
	stripped := 0
	stripItem := func(item any) (next any, changed bool, keep bool) {
		inputItem, ok := item.(map[string]any)
		if !ok {
			return item, false, true
		}
		encrypted, ok := inputItem["encrypted_content"].(string)
		if !ok || encrypted == "" {
			return item, false, true
		}
		if _, hit := invalid[OpenAIEncryptedContentDigest(encrypted)]; !hit {
			return item, false, true
		}
		// Apply the shared type-specific rule after the digest matches; valid
		// ciphertext and unsupported item types retain their history for later replay.
		return wire.SanitizeEncryptedReasoningInputItem(item)
	}
	switch input := inputValue.(type) {
	case []any:
		// Compact on the existing backing array after each retention decision;
		// callers needing an independent history must isolate slice and map storage first.
		filtered := input[:0]
		for _, item := range input {
			nextItem, changed, keep := stripItem(item)
			if changed {
				stripped++
			}
			if !keep {
				continue
			}
			filtered = append(filtered, nextItem)
		}
		if stripped == 0 {
			return 0
		}
		if len(filtered) == 0 {
			delete(reqBody, "input")
			return stripped
		}
		reqBody["input"] = filtered
		return stripped
	case map[string]any:
		nextItem, changed, keep := stripItem(input)
		if !changed {
			return 0
		}
		if !keep {
			delete(reqBody, "input")
			return 1
		}
		nextMap, ok := nextItem.(map[string]any)
		if !ok {
			return 0
		}
		reqBody["input"] = nextMap
		return 1
	default:
		return 0
	}
}

// OpenAIRawPayloadHasInvalidEncryptedContent reports whether input contains a
// nonempty ciphertext matching invalid. It probes arrays or one object without
// filtering item types; keep payload immutable while its zero-copy view is
// parsed.
func OpenAIRawPayloadHasInvalidEncryptedContent(payload []byte, invalid map[string]struct{}) bool {
	if len(payload) == 0 || len(invalid) == 0 {
		return false
	}
	input := gjson.Get(OpenAIWSPayloadStringView(payload), "input")
	if !input.Exists() {
		return false
	}
	hit := false
	checkItem := func(item gjson.Result) bool {
		encrypted := item.Get("encrypted_content")
		if encrypted.Type != gjson.String || encrypted.String() == "" {
			return false
		}
		_, matched := invalid[OpenAIEncryptedContentDigest(encrypted.String())]
		return matched
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if checkItem(item) {
				hit = true
				return false
			}
			return true
		})
		return hit
	}
	if input.IsObject() {
		return checkItem(input)
	}
	return false
}

// StripOpenAIInvalidEncryptedContentFromReplayItems returns sanitized replay
// bytes and a changed-item count. Input bodies are read through zero-copy views
// and must remain immutable during the call. Changed items receive encoded
// replacement bytes, unmatched bodies remain shared, and a no-change result
// returns the input slice.
//
// A digest probe avoids decoding when no item can match. Candidate maps are
// decoded with number preservation before the shared sanitizer decides
// retention. Decode or encode failures keep that individual body and exclude it
// from the changed count; successful compaction removal and reasoning rewriting
// can still proceed for other items.
func StripOpenAIInvalidEncryptedContentFromReplayItems(items []json.RawMessage, invalid map[string]struct{}) ([]json.RawMessage, int) {
	if len(items) == 0 || len(invalid) == 0 {
		return items, 0
	}
	hit := false
	for _, item := range items {
		encrypted := gjson.Get(OpenAIWSPayloadStringView(item), "encrypted_content")
		if encrypted.Type != gjson.String || encrypted.String() == "" {
			continue
		}
		if _, matched := invalid[OpenAIEncryptedContentDigest(encrypted.String())]; matched {
			hit = true
			break
		}
	}
	if !hit {
		return items, 0
	}
	stripped := 0
	next := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var decoded map[string]any
		// Keep unreadable replay bytes in sequence so optional ciphertext recovery
		// does not discard client history that the shared sanitizer cannot interpret.
		if err := wirejson.DecodeUseNumber(item, &decoded); err != nil {
			next = append(next, item)
			continue
		}
		encrypted, ok := decoded["encrypted_content"].(string)
		if !ok || encrypted == "" {
			next = append(next, item)
			continue
		}
		if _, matched := invalid[OpenAIEncryptedContentDigest(encrypted)]; !matched {
			next = append(next, item)
			continue
		}
		nextItem, changed, keep := wire.SanitizeEncryptedReasoningInputItem(decoded)
		if !changed {
			next = append(next, item)
			continue
		}
		stripped++
		if !keep {
			continue
		}
		rebuilt, err := wirejson.Marshal(nextItem)
		// A failed replacement encoding keeps the original item and rolls back
		// its changed count, while other successfully sanitized replay items remain installed.
		if err != nil {
			next = append(next, item)
			stripped--
			continue
		}
		next = append(next, json.RawMessage(rebuilt))
	}
	if stripped == 0 {
		return items, 0
	}
	return next, stripped
}

// StripOpenAIInvalidEncryptedContentRaw rewrites a complete request only after
// its input contains an invalid digest. Decoding preserves numbers; unchanged
// requests and failed rebuilding return the supplied payload, with failures
// reported separately and successful results reporting changed items.
func StripOpenAIInvalidEncryptedContentRaw(payload []byte, invalid map[string]struct{}) ([]byte, int, error) {
	if !OpenAIRawPayloadHasInvalidEncryptedContent(payload, invalid) {
		return payload, 0, nil
	}
	var decoded map[string]any
	if err := wirejson.DecodeUseNumber(payload, &decoded); err != nil {
		return payload, 0, err
	}
	stripped := StripOpenAIInvalidEncryptedContentItems(decoded, invalid)
	if stripped == 0 {
		return payload, 0, nil
	}
	rebuilt, err := wirejson.Marshal(decoded)
	if err != nil {
		return payload, 0, err
	}
	return rebuilt, stripped, nil
}
