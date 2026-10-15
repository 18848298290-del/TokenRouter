package gemini

import "encoding/json"

// empty_parts.go removes Gemini conversation entries whose parts arrays are
// empty.
// Gemini requests can contain conversation entries whose parts array is
// empty after compatibility conversion. Empty-array entries are removed before
// forwarding, while absent or differently shaped parts fields remain available
// for upstream validation rather than being treated as empty conversation turns.

// FilterEmptyParts removes content entries with an explicitly empty parts
// array, retaining the order of other entries. JSON decoding and encoding errors
// are returned; unchanged valid requests retain their supplied bytes.
//
// Missing or nonarray contents, nonobject entries, and absent or nonarray parts
// remain untouched. A successful repair reserializes the complete decoded
// object through encoding/json, including unrelated numeric values in that
// object. Callers decide how to handle a returned error before forwarding the
// original or repaired request.
func FilterEmptyParts(body []byte) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	contents, ok := payload["contents"].([]any)
	if !ok || len(contents) == 0 {
		return body, nil
	}

	filtered := make([]any, 0, len(contents))
	modified := false

	for _, c := range contents {
		contentMap, ok := c.(map[string]any)
		if !ok {
			filtered = append(filtered, c)
			continue
		}

		parts, hasParts := contentMap["parts"]
		if !hasParts {
			filtered = append(filtered, c)
			continue
		}

		partsSlice, ok := parts.([]any)
		if !ok {
			filtered = append(filtered, c)
			continue
		}

		if len(partsSlice) == 0 {
			// An explicit empty parts array cannot supply Gemini conversation
			// content. Remove that entry while leaving unknown parts representations for upstream validation.
			modified = true
			continue
		}

		filtered = append(filtered, c)
	}

	if !modified {
		return body, nil
	}

	payload["contents"] = filtered
	return json.Marshal(payload)
}
