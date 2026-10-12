package session

import (
	"strings"
	"unicode/utf8"
)

// Client session IDs enter correlation storage and may later be placed in
// headers. Validation rejects malformed UTF-8 before trimming, then rejects ASCII
// controls inside the remaining value and applies the database limit in Unicode
// characters.

// MaxPersistedSessionIDLength sets the database correlation-column capacity in Unicode
// characters; validation rejects longer values rather than storing a truncated identity.
const MaxPersistedSessionIDLength = 255

// SanitizeClientSessionID returns a trimmed, valid correlation ID or empty on
// rejection. It rejects malformed UTF-8 before trimming, ASCII controls within the
// trimmed value, and values exceeding the database's Unicode-character capacity.
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
			// Reject the entire ID because storing a filtered value would change its
			// correlation identity and retain attacker-selected header or log controls.
			return ""
		}
		count++
		// Reject excess characters so distinct long IDs cannot collapse into
		// the same database-sized prefix during later persistence or correlation.
		if count > MaxPersistedSessionIDLength {
			return ""
		}
	}
	return trimmed
}

// ExtractClientSessionID returns the first valid session ID in the supplied header
// order. Invalid values allow later names to be tried; grok enables the final
// X-Grok-Conv-Id candidate, and header must be a nonnil reader.
func ExtractClientSessionID(header func(string) string, names []string, grok bool) string {
	// A malformed higher-priority header provides no usable correlation ID;
	// continue through the caller's order before trying the Grok-specific name.
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
