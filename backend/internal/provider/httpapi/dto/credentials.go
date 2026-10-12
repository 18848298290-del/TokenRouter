package dto

import (
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Administrator credential responses expose permitted configuration and
// sensitive-key presence separately. The provider classifier removes known
// secret keys even when unconfigured, and presence flags use value-specific
// rules so empty strings and false do not appear as configured secrets.

// RedactCredentials returns permitted credential values and flags for
// configured sensitive keys. Nil input returns two nil maps, and sensitive keys
// are removed regardless of presence. Flags are allocated only for present
// sensitive values.
//
// Each retained value is copied with CloneValues, whose concrete JSON map and
// slice cases support independent container edits. Unsupported mutable values
// can remain shared, and callers keep those values immutable through
// serialization. The input map is not changed by redaction; arbitrary
// unclassified keys remain available in the output.
func RedactCredentials(in map[string]any) (out map[string]any, status map[string]bool) {
	if in == nil {
		return nil, nil
	}
	out = make(map[string]any, len(in))
	for k, v := range in {
		if provider.IsSensitiveCredentialKey(k) {
			if isCredentialValuePresent(v) {
				if status == nil {
					status = make(map[string]bool, 4)
				}
				status["has_"+k] = true
			}
			// Known secret keys are omitted even when their values count as absent.
			// The response exposes presence only for configured values through the separate flag map.
			continue
		}
		out[k] = provider.CloneValues(map[string]any{k: v})[k]
	}
	return out, status
}

// isCredentialValuePresent reports whether a sensitive credential value counts
// as configured. Nil, an exactly empty string and false report absent;
// whitespace strings, numeric zero, empty containers and other values report
// present.
func isCredentialValuePresent(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	default:
		return true
	}
}
