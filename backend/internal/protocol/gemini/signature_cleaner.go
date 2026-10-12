package gemini

import (
	"encoding/json"
)

// CleanNativeThoughtSignatures replaces thoughtSignature fields with the supplied placeholder.
// After switching providers, callers use a dummy signature to bypass validation of the previous provider signature.
func CleanNativeThoughtSignatures(body []byte, placeholder string) []byte {
	if len(body) == 0 {
		return body
	}

	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		// Leave malformed or non-JSON bodies unchanged.
		return body
	}

	replaced := replaceThoughtSignaturesRecursive(data, placeholder)

	result, err := json.Marshal(replaced)
	if err != nil {
		return body
	}

	return result
}

// replaceThoughtSignaturesRecursive copies JSON containers while replacing thoughtSignature fields.
func replaceThoughtSignaturesRecursive(data any, placeholder string) any {
	switch v := data.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, value := range v {
			if key == "thoughtSignature" {
				result[key] = placeholder
				continue
			}
			result[key] = replaceThoughtSignaturesRecursive(value, placeholder)
		}
		return result

	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = replaceThoughtSignaturesRecursive(item, placeholder)
		}
		return result

	default:
		return v
	}
}
