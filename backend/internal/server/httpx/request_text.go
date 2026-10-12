package httpx

import (
	"strings"
	"unicode/utf8"
)

// NormalizePersistentText trims whitespace, removes invalid UTF-8, and truncates at a rune boundary.
// maxBytes limits the result in bytes; a nonpositive value disables truncation.
func NormalizePersistentText(value string, maxBytes int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
