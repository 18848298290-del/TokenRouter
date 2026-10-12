package dto

import (
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// RedactCredentials copies in and removes keys classified as sensitive by provider.IsSensitiveCredentialKey.
// The status map records has_<key> for sensitive values considered present by isCredentialValuePresent.
//
// A nil input returns nil, nil so the response can omit both objects.
// Non-sensitive values are cloned for serialization; the input is unchanged.
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
			continue
		}
		out[k] = provider.CloneValues(map[string]any{k: v})[k]
	}
	return out, status
}

// isCredentialValuePresent treats nil, empty strings, and false as unconfigured.
// All other values count as present, including numeric zero and empty containers.
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
