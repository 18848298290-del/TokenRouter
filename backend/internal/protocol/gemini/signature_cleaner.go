package gemini

import (
	"encoding/json"
)

// signature_cleaner.go replaces Gemini thought signatures throughout decoded
// request history for provider-switch recovery.
// Gemini replay signatures belong to the upstream state that produced their
// history. Provider-switch preparation can replace thoughtSignature fields
// with a caller-selected placeholder, including fields nested inside arrays
// or objects, before the request enters another provider's replay rules.
//
// Replacement matches that exact field name and leaves differently named
// signature fields untouched. The recursive helper copies map[string]any and
// []any containers while retaining scalar values; other Go values pass through.
// The byte-oriented entry point first decodes ordinary JSON, then serializes
// the complete result even if no matching field existed. Consequently JSON
// formatting and map-decoded numeric representation can change on success,
// while decoding or encoding failure returns the supplied request body.

// CleanNativeThoughtSignatures replaces every exact thoughtSignature field
// with placeholder in a decoded request. Empty bodies and decoding or encoding
// failures return the supplied bytes; successful calls reserialize the whole
// value even when no matching signature field existed.
//
// The caller chooses the placeholder and determines whether another provider
// accepts that replay strategy. This transformation replaces field values,
// including nonstring ones, without establishing cryptographic validity.
// Ordinary map decoding can round unrelated large numbers before serialization,
// so callers use this operation for the intended native replay-recovery path.
func CleanNativeThoughtSignatures(body []byte, placeholder string) []byte {
	if len(body) == 0 {
		return body
	}

	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		// A failed JSON decode leaves no traversable request value to repair.
		// Return the supplied bytes so the caller retains the complete body for other recovery handling.
		return body
	}

	replaced := replaceThoughtSignaturesRecursive(data, placeholder)

	result, err := json.Marshal(replaced)
	if err != nil {
		// Failed final encoding leaves the replacement value unpublished.
		// Return the supplied request body so the caller receives a complete representation for further handling.
		return body
	}

	return result
}

// replaceThoughtSignaturesRecursive copies map[string]any and []any containers
// while replacing exact thoughtSignature keys with placeholder. Other field
// names retain their recursively processed values, so a field named signature
// remains distinct from the Gemini replay field.
//
// Nested supported containers are copied before their values are replaced,
// allowing the supplied maps and slices to retain their membership and values.
// Scalars and unsupported Go container types return unchanged, which can
// retain sharing for manually constructed inputs. Callers pass decoded JSON
// when they need every object and array traversed by this helper.
func replaceThoughtSignaturesRecursive(data any, placeholder string) any {
	switch v := data.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, value := range v {
			if key == "thoughtSignature" {
				// Field-name matching confines the placeholder to Gemini replay
				// data; a neighboring signature field can belong to another format and keeps its value.
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
