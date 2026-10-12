package gemini

import "encoding/json"

// FilterEmptyParts removes content entries whose parts field is an empty array.
// Gemini rejects empty parts; absent or non-array fields are left for upstream validation.
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
