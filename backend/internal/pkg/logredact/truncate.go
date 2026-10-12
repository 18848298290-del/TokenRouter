package logredact

import (
	"strings"
	"unicode/utf8"
)

// TruncateUTF8Value trims whitespace and applies a byte limit without splitting UTF-8 characters.
func TruncateUTF8Value(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	return TruncateUTF8(value, maxLen)
}

// TruncateUTF8 limits UTF-8 text by bytes without trimming whitespace.
// A nonpositive limit returns empty; input within the limit is returned unchanged.
func TruncateUTF8(value string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(value) <= maxLen {
		return value
	}
	value = value[:maxLen]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// TruncateLine limits input bytes, then escapes CR and LF for single-line logging.
// A nonpositive limit uses 2048 bytes; escaping may make the result longer than the limit.
func TruncateLine(b []byte, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 2048
	}
	if len(b) > maxBytes {
		b = b[:maxBytes]
	}
	s := string(b)
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	return s
}
