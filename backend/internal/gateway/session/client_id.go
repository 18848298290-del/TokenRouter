package session

import (
	"strings"
	"unicode/utf8"
)

// MaxPersistedSessionIDLength is the database column limit in Unicode characters; longer IDs are rejected.
const MaxPersistedSessionIDLength = 255

// SanitizeClientSessionID trims surrounding whitespace and rejects invalid UTF-8,
// ASCII control characters within the trimmed ID, and values exceeding the database limit.
// Rejection prevents log or header injection through correlation data and returns an empty string.
func SanitizeClientSessionID(raw string) string {
	if !utf8.ValidString(raw) {
		return ""
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	count := 0
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			// Reject the whole correlation ID when it contains a control character.
			return ""
		}
		count++
		if count > MaxPersistedSessionIDLength {
			return ""
		}
	}
	return trimmed
}

// ExtractClientSessionID checks headers in the supplied order, then the Grok header when enabled.
func ExtractClientSessionID(header func(string) string, names []string, grok bool) string {
	for _, name := range names {
		if value := SanitizeClientSessionID(header(name)); value != "" {
			return value
		}
	}
	if grok {
		return SanitizeClientSessionID(header("X-Grok-Conv-Id"))
	}
	return ""
}
