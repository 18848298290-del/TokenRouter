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

func OpenAIEncryptedContentDigest(encrypted string) string {
	sum := sha256.Sum256([]byte(encrypted))
	return hex.EncodeToString(sum[:])
}

// OpenAIEncryptedLineageItemType matches the types handled by wire.SanitizeEncryptedReasoningInputItem.
// Collecting digests for other types would repeatedly trigger decoding of content that cannot be stripped.
func OpenAIEncryptedLineageItemType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "reasoning", "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

// CollectOpenAIEncryptedContentDigestsRaw collects ciphertext digests from supported input types.
// Keep payload immutable during the call: parsing uses a zero-copy string view.
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

// StripOpenAIInvalidEncryptedContentItems sanitizes input items whose ciphertext digest is invalid.
// Reasoning loses encrypted_content, null content and empty shells; compaction and
// compaction_summary items are removed. The return value counts changed items.
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
		return wire.SanitizeEncryptedReasoningInputItem(item)
	}
	switch input := inputValue.(type) {
	case []any:
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

// OpenAIRawPayloadHasInvalidEncryptedContent checks input ciphertext against invalid digests.
// Keep payload immutable during the call: parsing uses a zero-copy string view.
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

// StripOpenAIInvalidEncryptedContentFromReplayItems sanitizes matching replay items.
// Changes produce a new slice and replacement bodies; unmatched bodies are shared.
// It returns the original slice when nothing changes and never mutates input bodies.
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

// StripOpenAIInvalidEncryptedContentRaw probes for invalid ciphertext before decoding and rewriting.
// It returns the changed-item count, or the original payload when nothing changes.
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
