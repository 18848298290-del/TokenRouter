package provider

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Responses replay has distinct item-ID and call-ID namespaces that can
// contain equal strings. A rejected item ID should not erase an output paired
// through call_id or an opaque item reference; sanitation therefore changes
// fields on individual items while keeping every item in the array.
//
// Classification records which fields need removal using the upstream
// type-specific rules. Reconstruction edits each raw item and replaces the
// complete input array only after those edits succeed. Unaffected field
// representations remain in raw item bytes, and bodies with no edits return
// unchanged. Callers receive nil on a deletion or final replacement error
// rather than a partially assembled body.

// SanitizeOpenAIResponsesInputItemIDs removes known invalid item IDs and
// non-pair call IDs from array input. Other input shapes or arrays requiring
// no cleanup return the original body with false; edit errors return nil and
// false.
//
// Each item is classified before reconstruction, then only its rejected fields
// are deleted from raw JSON. Item IDs and paired call IDs have separate
// namespaces, so removing an invalid item ID does not delete outputs or
// references bearing the same text. Raw item bytes retain their other values,
// and the rebuilt array replaces input after all item edits succeed.
func SanitizeOpenAIResponsesInputItemIDs(body []byte) ([]byte, bool, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false, nil
	}

	// inputItem holds one raw replay object and the field removals selected for it.
	type inputItem struct {
		body        []byte // raw replay item JSON
		stripID     bool   // item identity requires removal
		stripCallID bool   // non-pair call identity removal
	}

	items := make([]inputItem, 0)
	input.ForEach(func(_, item gjson.Result) bool {
		parsed := inputItem{body: []byte(item.Raw)}
		if item.IsObject() {
			itemType := item.Get("type")
			id := item.Get("id")
			trimmedItemType := strings.TrimSpace(itemType.String())
			parsed.stripCallID = item.Get("call_id").Exists() && openaiprotocol.ShouldStripNonPairCallID(trimmedItemType)
			if id.Type == gjson.String {
				parsed.stripID = openai.ShouldStripOpenAIResponsesInputItemID(trimmedItemType, id.String())
			}
		}
		items = append(items, parsed)
		return true
	})
	hasSanitization := false
	for _, item := range items {
		if item.stripID || item.stripCallID {
			hasSanitization = true
			break
		}
	}
	if !hasSanitization {
		return body, false, nil
	}

	// Edit each raw object independently so equal text in another identity namespace
	// cannot trigger removal of a paired output or an opaque reference elsewhere.
	rebuiltItems := make([][]byte, 0, len(items))
	for index, item := range items {
		itemBody := item.body
		if item.stripID {
			var err error
			itemBody, err = sjson.DeleteBytes(itemBody, "id")
			if err != nil {
				return nil, false, fmt.Errorf("delete input.%d.id: %w", index, err)
			}
		}
		if item.stripCallID {
			var err error
			itemBody, err = sjson.DeleteBytes(itemBody, "call_id")
			if err != nil {
				return nil, false, fmt.Errorf("delete input.%d.call_id: %w", index, err)
			}
		}
		rebuiltItems = append(rebuiltItems, itemBody)
	}

	rebuiltInput := make([]byte, 0, len(input.Raw))
	rebuiltInput = append(rebuiltInput, '[')
	for i, item := range rebuiltItems {
		if i > 0 {
			rebuiltInput = append(rebuiltInput, ',')
		}
		rebuiltInput = append(rebuiltInput, item...)
	}
	rebuiltInput = append(rebuiltInput, ']')

	// Replace input after every requested deletion succeeds; return no partial body
	// when final replacement fails, leaving caller adoption conditional on the error result.
	sanitized, err := sjson.SetRawBytes(body, "input", rebuiltInput)
	if err != nil {
		return nil, false, fmt.Errorf("replace sanitized input: %w", err)
	}
	return sanitized, true, nil
}
